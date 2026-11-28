package v1alpha1

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
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
	if len(crds) != 9 || crds["Manufacturer"] == nil || crds["DeviceType"] == nil || crds["Site"] == nil || crds["DeviceClaim"] == nil || crds["Device"] == nil {
		t.Fatalf("got %d CRDs, want six physical kinds, Site and two catalog kinds", len(crds))
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
		status := version.Schema.OpenAPIV3Schema.Properties["status"]
		if status.XPreserveUnknownFields != nil || (kind != "Device" && len(status.Properties) != 0) {
			t.Fatalf("%s status must stay empty until a collector or verifier owns it", kind)
		}
		if kind == "Device" && (len(status.Properties) != 3 || status.Properties["discovery"].Type != "object" || status.Properties["conditions"].Type != "array") {
			t.Fatalf("Device status must hold exactly observedGeneration, discovery and conditions")
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

var baseTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func syntheticClaim() DeviceClaim {
	return DeviceClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "DeviceClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: "rack-01-u12", Namespace: "fleet-system"},
		Spec: DeviceClaimSpec{
			Lifecycle:        ClaimInstallation,
			ExpectedIdentity: ExpectedIdentity{Manufacturer: "Example Vendor", Model: "Example 1U"},
			Placement:        Placement{Scope: "RackUnits", RackRef: &LocalReference{Name: "rack-01"}, Position: 12, Height: 1, Face: "front"},
		},
	}
}

func syntheticDevice() Device {
	return Device{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Device"},
		ObjectMeta: metav1.ObjectMeta{Name: "rack-01-u12-synthetic-0001", Namespace: "fleet-system"},
		Spec: DeviceSpec{
			ClaimRef: LocalReference{Name: "rack-01-u12"},
			Identity: DeviceIdentity{Manufacturer: "Example Vendor", Model: "Example 1U", Serial: "SYNTHETIC-0001", SystemUUID: "00000000-0000-4000-8000-000000000001"},
			BMC:      &BMC{Endpoint: "https://192.0.2.100:443"},
			Interfaces: []Interface{
				{Name: "eth0", MACAddress: "00:00:5e:00:53:01"},
				{Name: "bmc0", MACAddress: "00:00:5e:00:53:02"},
			},
			UnmappedMACAddresses: []string{"00:00:5e:00:53:03"},
		},
	}
}

func later(d time.Duration) metav1.Time { return metav1.NewTime(baseTime.Add(d)) }

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

func TestClaimDoesNotRequireSerialOrInventHeight(t *testing.T) {
	membership := syntheticClaim()
	membership.Spec.Placement = Placement{Scope: "RackMembership", RackRef: &LocalReference{Name: "rack-01"}}
	claim := object(t, membership)
	if errs := admit(t, "DeviceClaim", claim, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	if height := claim["spec"].(map[string]any)["placement"].(map[string]any)["height"]; height != nil {
		t.Fatalf("height must not invent a partial-placement footprint: %v", height)
	}
}

func TestInvalidClaimsAreRejected(t *testing.T) {
	for name, mutate := range map[string]func(*DeviceClaim){
		"missing model":     func(c *DeviceClaim) { c.Spec.ExpectedIdentity.Model = "" },
		"missing lifecycle": func(c *DeviceClaim) { c.Spec.Lifecycle = "" },
		"unknown lifecycle": func(c *DeviceClaim) { c.Spec.Lifecycle = "Slot" },
		"missing scope":     func(c *DeviceClaim) { c.Spec.Placement.Scope = "" },
		"unknown scope":     func(c *DeviceClaim) { c.Spec.Placement.Scope = "Row" },
		"missing rack":      func(c *DeviceClaim) { c.Spec.Placement.RackRef = nil },
		"facility and rack": func(c *DeviceClaim) { c.Spec.Placement.FacilityRef = &LocalReference{Name: "facility-01"} },
		"zero position":     func(c *DeviceClaim) { c.Spec.Placement.Position = 0 },
		"missing height":    func(c *DeviceClaim) { c.Spec.Placement.Height = 0 },
		"unknown face":      func(c *DeviceClaim) { c.Spec.Placement.Face = "side" },
		"mount on units":    func(c *DeviceClaim) { c.Spec.Placement.Mount = "side-a" },
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

func TestDeviceIdentityNeedsSerialOrSystemUUID(t *testing.T) {
	serialOnly := syntheticDevice()
	serialOnly.Spec.Identity.SystemUUID = ""
	serialOnly.Spec.BMC, serialOnly.Spec.Interfaces, serialOnly.Spec.UnmappedMACAddresses = nil, nil, nil
	record := object(t, serialOnly)
	if _, ok := record["spec"].(map[string]any)["provenance"]; ok {
		t.Fatal("device fixture must not carry provenance")
	}
	if errs := admit(t, "Device", record, nil); len(errs) > 0 {
		t.Fatalf("device with only a serial rejected: %v", errs)
	}
	neither := serialOnly
	neither.Spec.Identity.Serial = ""
	if errs := admit(t, "Device", object(t, neither), nil); len(errs) == 0 {
		t.Fatal("device with neither serial nor system UUID accepted")
	}
}

// TestEditsDoNotNeedARecordingTimestamp pins that spec changes carry no
// attribution field; audit logs record who changed a record.
func TestEditsDoNotNeedARecordingTimestamp(t *testing.T) {
	claim := syntheticClaim()
	claim.Spec.DisplayName = "Example server"
	if errs := admit(t, "DeviceClaim", object(t, claim), object(t, syntheticClaim())); len(errs) > 0 {
		t.Fatalf("claim edit rejected: %v", errs)
	}
	device := syntheticDevice()
	device.Spec.BMC.Endpoint = "https://192.0.2.101"
	if errs := admit(t, "Device", object(t, device), object(t, syntheticDevice())); len(errs) > 0 {
		t.Fatalf("device edit rejected: %v", errs)
	}
}

// TestRetiredFieldsArePruned shows that objects stored with the retired
// provenance, source identity, component source and report import fields
// lose them when read or written through the structural schema, and that
// what remains is admitted when it still has a serial or system UUID.
func TestRetiredFieldsArePruned(t *testing.T) {
	legacy := object(t, reportedDevice())
	spec := legacy["spec"].(map[string]any)
	spec["provenance"] = map[string]any{"method": "Manual", "recordedBy": "operator@example.com", "recordedAt": "2026-01-02T03:04:05Z", "reference": "rack plan 1234"}
	spec["identity"].(map[string]any)["sourceIdentity"] = map[string]any{"system": "example", "type": "device", "id": "1"}
	report := spec["reportedPlacement"].(map[string]any)
	report["method"] = "Manual"
	report["snapshotDigest"] = "sha256:" + strings.Repeat("a", 64)
	spec["topologyAuthority"] = "Cable"
	delete(spec, "interfaces")
	spec["components"] = []any{map[string]any{"id": "front", "name": "front", "type": "FrontPort", "sourceRef": map[string]any{"system": "example", "type": "port", "id": "1"}}}
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(loadCRDs(t)["Device"].Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	pruning.Prune(legacy, structural, true)
	spec = legacy["spec"].(map[string]any)
	if _, ok := spec["provenance"]; ok {
		t.Fatal("provenance survived pruning")
	}
	if _, ok := spec["identity"].(map[string]any)["sourceIdentity"]; ok {
		t.Fatal("sourceIdentity survived pruning")
	}
	if _, ok := spec["components"].([]any)[0].(map[string]any)["sourceRef"]; ok {
		t.Fatal("component sourceRef survived pruning")
	}
	report = spec["reportedPlacement"].(map[string]any)
	if _, ok := report["method"]; ok {
		t.Fatal("report method survived pruning")
	}
	if _, ok := report["snapshotDigest"]; ok {
		t.Fatal("report snapshotDigest survived pruning")
	}
	if errs := admit(t, "Device", legacy, nil); len(errs) > 0 {
		t.Fatalf("pruned legacy device rejected: %v", errs)
	}
	for _, kind := range []string{"DeviceClaim", "Facility", "Site", "Rack", "Cable", "PowerFeed", "Manufacturer", "DeviceType"} {
		var schema apiextensions.JSONSchemaProps
		if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(loadCRDs(t)[kind].Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
			t.Fatal(err)
		}
		structural, err := structuralschema.NewStructural(&schema)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := structural.Properties["spec"].Properties["provenance"]; ok || structural.Properties["spec"].XPreserveUnknownFields {
			t.Fatalf("%s spec must not declare or preserve provenance", kind)
		}
	}
}

// TestSourceOnlyIdentityDoesNotSurvivePruning pins the case that does need
// action: a Device whose only identity was sourceIdentity prunes to an
// identity without a serial or system UUID, which is no longer valid. Because
// identity is immutable, such a Device has to be deleted and recreated.
func TestSourceOnlyIdentityDoesNotSurvivePruning(t *testing.T) {
	legacy := object(t, syntheticDevice())
	spec := legacy["spec"].(map[string]any)
	identity := spec["identity"].(map[string]any)
	delete(identity, "serial")
	delete(identity, "systemUUID")
	identity["sourceIdentity"] = map[string]any{"system": "example", "type": "device", "id": "1"}
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(loadCRDs(t)["Device"].Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	pruning.Prune(legacy, structural, true)
	if errs := admit(t, "Device", legacy, nil); len(errs) == 0 {
		t.Fatal("a Device with only a pruned source identity was admitted")
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
		if errs := admit(t, "Device", object(t, device), old); len(errs) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	unserialed := syntheticDevice()
	unserialed.Spec.Identity.Serial = ""
	added := syntheticDevice()
	if errs := admit(t, "Device", object(t, added), object(t, unserialed)); len(errs) == 0 {
		t.Error("adding a serial to a registered device accepted; a new identity needs a new Device")
	}
}

func TestReplacementBindsANewDeviceToTheSameClaim(t *testing.T) {
	replacement := syntheticDevice()
	replacement.Name = "rack-01-u12-synthetic-0002"
	replacement.Spec.Identity.Serial = "SYNTHETIC-0002"
	replacement.Spec.Identity.SystemUUID = "00000000-0000-4000-8000-000000000002"
	if errs := admit(t, "Device", object(t, replacement), nil); len(errs) > 0 {
		t.Fatalf("replacement device for the same claim rejected: %v", errs)
	}
}

func discoveredDevice() Device {
	device := syntheticDevice()
	at := metav1.NewTime(baseTime)
	nic := []ObservedNetworkInterface{{ID: "1", MACAddress: "00:00:5e:00:53:01", LinkStatus: "LinkUp", SpeedMbps: 25000}}
	device.Status = DeviceStatus{
		ObservedGeneration: 1,
		Discovery: &DeviceDiscovery{
			LastAttemptAt: &at, LastAttemptEndpoint: "https://192.0.2.100:443",
			TLS:         &ObservedTLS{ObservedAt: at, Endpoint: "https://192.0.2.100:443", PublicKeySHA256: strings.Repeat("ab", 32), NotAfter: &at, SelfSigned: true},
			ServiceRoot: &ObservedServiceRoot{ObservedAt: at, Endpoint: "https://192.0.2.100:443", RedfishVersion: "1.15.0"},
			Inventory: &ObservedInventory{ObservedAt: at, Endpoint: "https://192.0.2.100:443",
				Managers: []ObservedManager{{ID: "BMC", Model: "Example BMC", FirmwareVersion: "1.0", NetworkInterfaces: nic}},
				Systems: []ObservedSystem{{ID: "1", UUID: "00000000-0000-4000-8000-000000000001", Model: "Example 1U", SerialNumber: "SYNTHETIC-0001",
					PowerState: "On", NetworkInterfaces: nic}},
				Chassis: []ObservedChassis{{ID: "1", NetworkAdapters: []ObservedNetworkAdapter{{ID: "NIC.Slot.1", Model: "Example 2x25G", FirmwareVersion: "1.2.3",
					Ports: []ObservedAdapterPort{{ID: "1", PhysicalPortNumber: "1", MACAddresses: []string{"02:00:00:00:01:01"}, LinkStatus: "LinkUp", SpeedMbps: 25000,
						ServiceLabel: "NIC Slot 1 Port 1", LLDP: &ObservedLLDPNeighbor{ChassisID: "02:00:00:00:ff:01", ChassisIDSubtype: "MacAddr", PortID: "Ethernet1/1",
							PortIDSubtype: "IfName", SystemName: "example-switch-01", ManagementAddressIPv4: "192.0.2.250", ManagementAddressIPv6: "2001:db8::fa"}}}}}}}},
		},
		Conditions: []metav1.Condition{{Type: ConditionDiscovered, Status: metav1.ConditionTrue, Reason: "InventoryRead", Message: "read", LastTransitionTime: at}},
	}
	return device
}

func TestDeviceStatusSchema(t *testing.T) {
	if errs := admit(t, "Device", object(t, discoveredDevice()), nil); len(errs) > 0 {
		t.Fatalf("discovered status rejected: %v", errs)
	}
	for name, mutate := range map[string]func(*Device){
		"pin not a sha256": func(d *Device) { d.Status.Discovery.TLS.PublicKeySHA256 = "AB" },
		"uppercase uuid":   func(d *Device) { d.Status.Discovery.Inventory.Systems[0].UUID = "00000000-0000-4000-8000-00000000000A" },
		"mac not canonical": func(d *Device) {
			d.Status.Discovery.Inventory.Systems[0].NetworkInterfaces[0].MACAddress = "00-00-5E-00-53-01"
		},
		"port mac not canonical": func(d *Device) {
			d.Status.Discovery.Inventory.Chassis[0].NetworkAdapters[0].Ports[0].MACAddresses = []string{"02:00:00:00:01:0A"}
		},
		"too many port macs": func(d *Device) {
			d.Status.Discovery.Inventory.Chassis[0].NetworkAdapters[0].Ports[0].MACAddresses = slices.Repeat([]string{"02:00:00:00:01:01"}, 17)
		},
		"lldp management address not ipv4": func(d *Device) {
			d.Status.Discovery.Inventory.Chassis[0].NetworkAdapters[0].Ports[0].LLDP.ManagementAddressIPv4 = "2001:db8::fa"
		},
		"port without id": func(d *Device) { d.Status.Discovery.Inventory.Chassis[0].NetworkAdapters[0].Ports[0].ID = "" },
		"adapter read failure too long": func(d *Device) {
			d.Status.Discovery.Inventory.NetworkAdapterReadFailure = strings.Repeat("x", 254)
		},
		"too many chassis": func(d *Device) {
			d.Status.Discovery.Inventory.Chassis = slices.Repeat(d.Status.Discovery.Inventory.Chassis, 17)
		},
		"condition reason":     func(d *Device) { d.Status.Conditions[0].Reason = "" },
		"duplicate conditions": func(d *Device) { d.Status.Conditions = append(d.Status.Conditions, d.Status.Conditions[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			device := discoveredDevice()
			mutate(&device)
			if errs := admit(t, "Device", object(t, device), nil); len(errs) == 0 {
				t.Fatal("accepted")
			}
		})
	}
	// Status cannot carry identity: an unknown status field is pruned.
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(loadCRDs(t)["Device"].Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	device := object(t, discoveredDevice())
	device["status"].(map[string]any)["identity"] = map[string]any{"serial": "SYNTHETIC-0009"}
	pruning.Prune(device, structural, true)
	if _, ok := device["status"].(map[string]any)["identity"]; ok {
		t.Fatal("status identity was not pruned")
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
