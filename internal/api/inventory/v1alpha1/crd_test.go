package v1alpha1

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/yaml"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
)

var (
	configRoot    = filepath.Join("..", "..", "..", "..", "config")
	inventoryRoot = filepath.Join(configRoot, "components", "inventory")
)

func loadCRDs(t *testing.T) map[string]*extensionsv1.CustomResourceDefinition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(inventoryRoot, "crds", "fleet.datumapis.com.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	out := map[string]*extensionsv1.CustomResourceDefinition{}
	for {
		var crd extensionsv1.CustomResourceDefinition
		if err := decoder.Decode(&crd); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		if crd.Name != "" {
			copied := crd
			out[crd.Spec.Names.Kind] = &copied
		}
	}
}

func TestInventoryCRDContract(t *testing.T) {
	crds := loadCRDs(t)
	if len(crds) != 2 || crds["DeviceClaim"] == nil || crds["Device"] == nil {
		t.Fatalf("got CRDs %v, want exactly DeviceClaim and Device", crds)
	}
	for kind, crd := range crds {
		if crd.Spec.Group != GroupVersion.Group || crd.Name != crd.Spec.Names.Plural+"."+GroupVersion.Group || crd.Spec.Scope != extensionsv1.NamespaceScoped {
			t.Fatalf("%s identity = %s %s", kind, crd.Name, crd.Spec.Scope)
		}
		version := crd.Spec.Versions[0]
		if len(crd.Spec.Versions) != 1 || version.Name != GroupVersion.Version || !version.Served || !version.Storage {
			t.Fatalf("%s must serve and store exactly v1alpha1", kind)
		}
		if version.Subresources == nil || version.Subresources.Status == nil {
			t.Fatalf("%s must reserve status as a separate subresource", kind)
		}
		if status := version.Schema.OpenAPIV3Schema.Properties["status"]; len(status.Properties) != 0 || status.XPreserveUnknownFields != nil {
			t.Fatalf("%s status must stay empty until a collector or verifier owns it", kind)
		}
		if crd.Annotations["discovery.miloapis.com/parent-contexts"] != "Project" {
			t.Fatalf("%s must be served in Milo project control planes", kind)
		}
		defaulted := crd.DeepCopy()
		extensionsv1.SetObjectDefaults_CustomResourceDefinition(defaulted)
		defaulted.Status.StoredVersions = []string{GroupVersion.Version}
		var internal apiextensions.CustomResourceDefinition
		if err := extensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(defaulted, &internal, nil); err != nil {
			t.Fatal(err)
		}
		if errs := crdvalidation.ValidateCustomResourceDefinition(context.Background(), &internal); len(errs) > 0 {
			t.Fatalf("%s: %v", kind, errs.ToAggregate())
		}
	}
}

// admit applies schema defaults to both objects, as stored objects already
// carry them, then the OpenAPI, list-type, and CEL
// validation the API server runs on create (old == nil) or update. Objects are
// decoded with integer-preserving JSON, as the API server decodes them.
func admit(t *testing.T, kind string, object, old map[string]any) field.ErrorList {
	t.Helper()
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(loadCRDs(t)[kind].Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	structuraldefaulting.Default(object, structural)
	validator, _, err := validation.NewSchemaValidator(&schema)
	if err != nil {
		t.Fatal(err)
	}
	errs := validation.ValidateCustomResource(nil, object, validator)
	errs = append(errs, listtype.ValidateListSetsAndMaps(nil, structural, object)...)
	var oldObject any
	if old != nil {
		structuraldefaulting.Default(old, structural)
		oldObject = old
	}
	celErrs, _ := cel.NewValidator(structural, true, celconfig.PerCallLimit).Validate(context.Background(), nil, structural, object, oldObject, celconfig.RuntimeCELCostBudget)
	return append(errs, celErrs...)
}

func object(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := utiljson.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

var recordedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func provenance(at time.Time) Provenance {
	return Provenance{Method: ProvenanceManual, RecordedBy: "operator@example.com", RecordedAt: metav1.NewTime(at), Reference: "rack plan 1234"}
}

func syntheticClaim() DeviceClaim {
	return DeviceClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "DeviceClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: "rack-01-u12", Namespace: "fleet-system"},
		Spec: DeviceClaimSpec{
			Provenance:       provenance(recordedAt),
			ExpectedIdentity: ExpectedIdentity{Manufacturer: "Example Vendor", Model: "Example 1U"},
			Placement:        Placement{Facility: "example-facility", Rack: "rack-01", Position: 12, Face: "front"},
			Ports: []ExpectedPort{
				{Name: "eth0", Type: "25gbase-x-sfp28", SpeedMbps: 25000, ExpectedPeer: &PortPeer{Device: "switch-01", Port: "Ethernet1/1"}},
				{Name: "bmc0", Type: "1000base-t", SpeedMbps: 1000, ExpectedPeer: &PortPeer{Device: "mgmt-switch-01", Port: "Ethernet5"}},
			},
		},
	}
}

func syntheticDevice() Device {
	return Device{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Device"},
		ObjectMeta: metav1.ObjectMeta{Name: "rack-01-u12-synthetic-0001", Namespace: "fleet-system"},
		Spec: DeviceSpec{
			ClaimRef:   LocalReference{Name: "rack-01-u12"},
			Provenance: provenance(recordedAt),
			Identity:   DeviceIdentity{Manufacturer: "Example Vendor", Model: "Example 1U", Serial: "SYNTHETIC-0001", SystemUUID: "00000000-0000-4000-8000-000000000001"},
			BMC:        &BMC{Endpoint: "https://192.0.2.100:443"},
			Interfaces: []Interface{
				{Name: "eth0", MACAddress: "00:00:5e:00:53:01", Peer: &PortPeer{Device: "switch-01", Port: "Ethernet1/1"}},
				{Name: "bmc0", MACAddress: "00:00:5e:00:53:02"},
			},
			UnmappedMACAddresses: []string{"00:00:5e:00:53:03"},
		},
	}
}

func later(d time.Duration) metav1.Time { return metav1.NewTime(recordedAt.Add(d)) }

func TestValidRecordsAreAccepted(t *testing.T) {
	if errs := admit(t, "DeviceClaim", object(t, syntheticClaim()), nil); len(errs) > 0 {
		t.Fatalf("claim rejected: %v", errs)
	}
	if errs := admit(t, "Device", object(t, syntheticDevice()), nil); len(errs) > 0 {
		t.Fatalf("device rejected: %v", errs)
	}
	minimal := syntheticDevice()
	minimal.Spec.Identity.Serial = ""
	minimal.Spec.BMC, minimal.Spec.Interfaces = nil, nil
	if errs := admit(t, "Device", object(t, minimal), nil); len(errs) > 0 {
		t.Fatalf("device with only a system UUID rejected: %v", errs)
	}
}

func TestClaimDoesNotRequireSerialAndDefaultsHeight(t *testing.T) {
	claim := object(t, syntheticClaim())
	if errs := admit(t, "DeviceClaim", claim, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	if height := claim["spec"].(map[string]any)["placement"].(map[string]any)["height"]; height != int64(1) {
		t.Fatalf("height defaulted to %v", height)
	}
}

func TestInvalidClaimsAreRejected(t *testing.T) {
	for name, mutate := range map[string]func(*DeviceClaim){
		"unknown method":   func(c *DeviceClaim) { c.Spec.Provenance.Method = "Discovered" },
		"missing recorder": func(c *DeviceClaim) { c.Spec.Provenance.RecordedBy = "" },
		"missing model":    func(c *DeviceClaim) { c.Spec.ExpectedIdentity.Model = "" },
		"missing facility": func(c *DeviceClaim) { c.Spec.Placement.Facility = "" },
		"zero position":    func(c *DeviceClaim) { c.Spec.Placement.Position = 0 },
		"unknown face":     func(c *DeviceClaim) { c.Spec.Placement.Face = "side" },
		"duplicate port":   func(c *DeviceClaim) { c.Spec.Ports[1].Name = c.Spec.Ports[0].Name },
		"peer without port": func(c *DeviceClaim) {
			c.Spec.Ports[0].ExpectedPeer = &PortPeer{Device: "switch-01"}
		},
	} {
		claim := syntheticClaim()
		mutate(&claim)
		if errs := admit(t, "DeviceClaim", object(t, claim), nil); len(errs) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestInvalidDevicesAreRejected(t *testing.T) {
	for name, mutate := range map[string]func(*Device){
		"missing claim":         func(d *Device) { d.Spec.ClaimRef.Name = "" },
		"no identity key":       func(d *Device) { d.Spec.Identity.Serial, d.Spec.Identity.SystemUUID = "", "" },
		"uppercase system UUID": func(d *Device) { d.Spec.Identity.SystemUUID = "00000000-0000-4000-8000-00000000000A" },
		"malformed system UUID": func(d *Device) { d.Spec.Identity.SystemUUID = "not-a-uuid" },
		"uppercase MAC":         func(d *Device) { d.Spec.Interfaces[0].MACAddress = "00:00:5E:00:53:01" },
		"malformed MAC":         func(d *Device) { d.Spec.UnmappedMACAddresses = []string{"not-a-mac"} },
		"duplicate interface":   func(d *Device) { d.Spec.Interfaces[1].Name = d.Spec.Interfaces[0].Name },
		"MAC on two interfaces": func(d *Device) { d.Spec.Interfaces[1].MACAddress = d.Spec.Interfaces[0].MACAddress },
		"MAC mapped and unmapped": func(d *Device) {
			d.Spec.UnmappedMACAddresses = []string{d.Spec.Interfaces[0].MACAddress}
		},
		"duplicate unmapped MAC": func(d *Device) {
			d.Spec.UnmappedMACAddresses = []string{"00:00:5e:00:53:03", "00:00:5e:00:53:03"}
		},
		"plain-http BMC":       func(d *Device) { d.Spec.BMC.Endpoint = "http://192.0.2.100" },
		"BMC with credentials": func(d *Device) { d.Spec.BMC.Endpoint = "https://admin:secret@192.0.2.100" },
		"BMC not a URL":        func(d *Device) { d.Spec.BMC.Endpoint = "192.0.2.100" },
	} {
		device := syntheticDevice()
		mutate(&device)
		if errs := admit(t, "Device", object(t, device), nil); len(errs) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	withoutClaim := object(t, syntheticDevice())
	delete(withoutClaim["spec"].(map[string]any), "claimRef")
	if errs := admit(t, "Device", withoutClaim, nil); len(errs) == 0 {
		t.Error("device without a claim reference accepted")
	}
}

func TestEveryEditMustAdvanceRecordedAt(t *testing.T) {
	oldClaim := object(t, syntheticClaim())
	if errs := admit(t, "DeviceClaim", object(t, syntheticClaim()), oldClaim); len(errs) > 0 {
		t.Fatalf("unchanged claim update rejected: %v", errs)
	}
	for name, at := range map[string]metav1.Time{"same time": metav1.NewTime(recordedAt), "backdated": later(-time.Hour)} {
		claim := syntheticClaim()
		claim.Spec.Placement.Position = 14
		claim.Spec.Provenance.RecordedAt = at
		if errs := admit(t, "DeviceClaim", object(t, claim), oldClaim); len(errs) == 0 {
			t.Errorf("claim edit with %s accepted", name)
		}
	}
	replanned := syntheticClaim()
	replanned.Spec.Placement.Position = 14
	replanned.Spec.Provenance.RecordedAt = later(time.Hour)
	if errs := admit(t, "DeviceClaim", object(t, replanned), oldClaim); len(errs) > 0 {
		t.Fatalf("re-recorded claim edit rejected: %v", errs)
	}

	oldDevice := object(t, syntheticDevice())
	stale := syntheticDevice()
	stale.Spec.BMC.Endpoint = "https://192.0.2.101"
	if errs := admit(t, "Device", object(t, stale), oldDevice); len(errs) == 0 {
		t.Error("device edit without a newer recordedAt accepted")
	}
	updated := syntheticDevice()
	updated.Spec.BMC.Endpoint = "https://192.0.2.101"
	updated.Spec.UnmappedMACAddresses = nil
	updated.Spec.Interfaces = append(updated.Spec.Interfaces, Interface{Name: "eth1", MACAddress: "00:00:5e:00:53:03"})
	updated.Spec.Provenance.RecordedAt = later(time.Hour)
	if errs := admit(t, "Device", object(t, updated), oldDevice); len(errs) > 0 {
		t.Fatalf("re-recorded device edit rejected: %v", errs)
	}
}

func TestClaimBindingAndIdentityAreImmutable(t *testing.T) {
	old := object(t, syntheticDevice())
	for name, mutate := range map[string]func(*Device){
		"retarget claim":     func(d *Device) { d.Spec.ClaimRef.Name = "rack-01-u14" },
		"change serial":      func(d *Device) { d.Spec.Identity.Serial = "SYNTHETIC-0002" },
		"remove serial":      func(d *Device) { d.Spec.Identity.Serial = "" },
		"change system UUID": func(d *Device) { d.Spec.Identity.SystemUUID = "00000000-0000-4000-8000-000000000002" },
		"change model":       func(d *Device) { d.Spec.Identity.Model = "Example 2U" },
	} {
		device := syntheticDevice()
		mutate(&device)
		device.Spec.Provenance.RecordedAt = later(time.Hour)
		if errs := admit(t, "Device", object(t, device), old); len(errs) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	unserialed := syntheticDevice()
	unserialed.Spec.Identity.Serial = ""
	added := syntheticDevice()
	added.Spec.Provenance.RecordedAt = later(time.Hour)
	if errs := admit(t, "Device", object(t, added), object(t, unserialed)); len(errs) == 0 {
		t.Error("adding a serial to a registered device accepted; a new identity needs a new Device")
	}
}

func TestReplacementBindsANewDeviceToTheSameClaim(t *testing.T) {
	replacement := syntheticDevice()
	replacement.Name = "rack-01-u12-synthetic-0002"
	replacement.Spec.Identity.Serial = "SYNTHETIC-0002"
	replacement.Spec.Identity.SystemUUID = "00000000-0000-4000-8000-000000000002"
	replacement.Spec.Provenance.RecordedAt = later(24 * time.Hour)
	if errs := admit(t, "Device", object(t, replacement), nil); len(errs) > 0 {
		t.Fatalf("replacement device for the same claim rejected: %v", errs)
	}
}

func TestStatusAcceptsNoFieldsYet(t *testing.T) {
	device := object(t, syntheticDevice())
	device["status"] = map[string]any{"identity": map[string]any{"serial": "SYNTHETIC-0009"}}
	if errs := admit(t, "Device", device, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(loadCRDs(t)["Device"].Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	if structural.Properties["status"].Properties != nil {
		t.Fatal("status declares fields")
	}
}

func TestSchemeRegistersBothKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	claim, device := syntheticClaim(), syntheticDevice()
	for _, obj := range []runtime.Object{claim.DeepCopyObject(), device.DeepCopyObject(), &DeviceClaimList{}, &DeviceList{}} {
		if _, _, err := scheme.ObjectKinds(obj); err != nil {
			t.Fatal(err)
		}
	}
	if copied := device.DeepCopyObject().(*Device); copied.Spec.Identity != device.Spec.Identity || &copied.Spec.Interfaces[0] == &device.Spec.Interfaces[0] {
		t.Fatal("deep copy did not copy the device")
	}
}
