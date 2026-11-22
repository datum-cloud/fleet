package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	metavalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCatalogNamespaceBoundaries(t *testing.T) {
	for _, namespace := range []string{"a", strings.Repeat("a", 63), "has.dot", strings.Repeat("a", 64)} {
		t.Run(namespace, func(t *testing.T) {
			manufacturer, model := catalogRecords()
			for _, record := range []client.Object{manufacturer, model} {
				record.SetNamespace(namespace)
				metadata := metav1.ObjectMeta{Name: record.GetName(), Namespace: namespace}
				errors := metavalidation.ValidateObjectMeta(&metadata, true, metavalidation.NameIsDNSSubdomain, field.NewPath("metadata"))
				valid := namespace == "a" || namespace == strings.Repeat("a", 63)
				if (len(errors) == 0) != valid {
					t.Fatalf("unexpected metadata validation: %v", errors)
				}
				if valid {
					candidate := object(t, record)
					if errors := admit(t, candidate["kind"].(string), candidate, nil); len(errors) != 0 {
						t.Fatal(errors)
					}
				}
			}
		})
	}
}

func TestImportedCatalogCandidatesPassAdmission(t *testing.T) {
	data, err := os.ReadFile("../../../../hack/testdata/catalog/candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	var candidates struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates.Items) != 2 {
		t.Fatal("expected both catalog kinds")
	}
	for _, raw := range candidates.Items {
		candidate := object(t, raw)
		kind := candidate["kind"].(string)
		for _, old := range []map[string]any{nil, candidate} {
			if errs := admit(t, kind, candidate, old); len(errs) != 0 {
				t.Fatal(errs)
			}
		}
	}
}

func catalogRecords() (*Manufacturer, *DeviceType) {
	metadata := PhysicalMetadata{DisplayName: "Example", Provenance: provenance(recordedAt)}
	manufacturer := &Manufacturer{TypeMeta: metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Manufacturer"}, ObjectMeta: metav1.ObjectMeta{Name: "vendor-stable", Namespace: "fleet-system"}, Spec: ManufacturerSpec{PhysicalMetadata: metadata, CanonicalName: "Example Vendor", Aliases: []string{"Old Vendor"}}}
	model := &DeviceType{TypeMeta: metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "DeviceType"}, ObjectMeta: metav1.ObjectMeta{Name: "model-stable", Namespace: "fleet-system"}, Spec: DeviceTypeSpec{PhysicalMetadata: metadata, ManufacturerRef: LocalReference{Name: manufacturer.Name}, Model: "example-1u", ComponentTemplates: []ComponentTemplate{{ID: "port-1", Name: "Port 1", Type: "NetworkPort"}}}}
	return manufacturer, model
}

func TestCatalogAdmission(t *testing.T) {
	manufacturer, model := catalogRecords()
	manufacturer.Spec.CanonicalName = "Example Vendor, Inc."
	manufacturer.Spec.Aliases = []string{"Old Vendor", "Example Vendor"}
	for _, record := range []any{manufacturer, model} {
		candidate := object(t, record)
		if errs := admit(t, candidate["kind"].(string), candidate, nil); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	manufacturer, _ = catalogRecords()
	for _, aliases := range [][]string{{"Old Vendor", "old vendor"}, {"Example Vendor"}, {" leading"}, {"trailing "}, {""}} {
		candidate := manufacturer.DeepCopyObject().(*Manufacturer)
		candidate.Spec.Aliases = aliases
		if errs := admit(t, "Manufacturer", object(t, candidate), nil); len(errs) == 0 {
			t.Fatalf("accepted aliases %v", aliases)
		}
	}
	for _, key := range []string{"Example-1u", "example 1u", "example--1u", "", "example_1u"} {
		candidate := model.DeepCopyObject().(*DeviceType)
		candidate.Spec.Model = key
		if errs := admit(t, "DeviceType", object(t, candidate), nil); len(errs) == 0 {
			t.Fatalf("accepted non-normalized key %q", key)
		}
	}
	for _, name := range []string{"", "Upper", "vendor..one", "vendor/one", "vendor.-one"} {
		candidate := model.DeepCopyObject().(*DeviceType)
		candidate.Spec.ManufacturerRef.Name = name
		if errs := admit(t, "DeviceType", object(t, candidate), nil); len(errs) == 0 {
			t.Fatalf("accepted invalid reference %q", name)
		}
	}
	duplicate := model.DeepCopyObject().(*DeviceType)
	duplicate.Spec.ComponentTemplates = append(duplicate.Spec.ComponentTemplates, duplicate.Spec.ComponentTemplates[0])
	if errs := admit(t, "DeviceType", object(t, duplicate), nil); len(errs) == 0 {
		t.Fatal("accepted duplicate template ID")
	}
	duplicate = model.DeepCopyObject().(*DeviceType)
	duplicate.Name = "different-resource-name"
	if errs := admit(t, "DeviceType", object(t, duplicate), nil); len(errs) != 0 {
		t.Fatal("local admission must not claim cross-object uniqueness", errs)
	}
}

func TestCatalogSourceIdentityAndScheme(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Manufacturer", "ManufacturerList", "DeviceType", "DeviceTypeList"} {
		if _, err := scheme.New(GroupVersion.WithKind(kind)); err != nil {
			t.Fatal(err)
		}
	}
	manufacturer, _ := catalogRecords()
	source := ExternalReference{System: "synthetic-netbox", Type: "dcim/manufacturers", ID: "1"}
	manufacturer.Spec.SourceIdentity = &source
	manufacturer.Spec.Provenance.Method = "NetBoxImport"
	manufacturer.Spec.Provenance.Source = &SourceReference{ExternalReference: source, SnapshotDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MappingRevision: "reviewed-1", ObservedAt: later(-time.Hour)}
	if errs := admit(t, "Manufacturer", object(t, manufacturer), nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, mode := range []string{"rebind", "remove", "provenance-mismatch"} {
		changed := manufacturer.DeepCopyObject().(*Manufacturer)
		changed.Spec.Provenance.RecordedAt = later(time.Hour)
		switch mode {
		case "rebind":
			changed.Spec.SourceIdentity.ID = "2"
			changed.Spec.Provenance.Source.ID = "2"
		case "remove":
			changed.Spec.SourceIdentity = nil
			changed.Spec.Provenance.Source = nil
			changed.Spec.Provenance.Method = "Manual"
		default:
			changed.Spec.Provenance.Source.ID = "2"
		}
		if errs := admit(t, "Manufacturer", object(t, changed), object(t, manufacturer)); len(errs) == 0 {
			t.Fatal("accepted imported catalog identity mutation", mode)
		}
	}
}

func TestCatalogUpdatesAndDeviceCompatibility(t *testing.T) {
	_, model := catalogRecords()
	changed := model.DeepCopyObject().(*DeviceType)
	changed.Spec.DisplayName = "New human label"
	changed.Spec.ComponentTemplates[0].Name = "Renamed template only"
	if errs := admit(t, "DeviceType", object(t, changed), object(t, model)); len(errs) == 0 {
		t.Fatal("accepted edit without provenance advance")
	}
	changed.Spec.Provenance.RecordedAt = later(time.Second)
	if errs := admit(t, "DeviceType", object(t, changed), object(t, model)); len(errs) != 0 {
		t.Fatal(errs)
	}
	changed.Spec.Model = "different-model"
	if errs := admit(t, "DeviceType", object(t, changed), object(t, model)); len(errs) == 0 {
		t.Fatal("accepted model identity mutation")
	}
	withoutProvenance := syntheticDevice()
	withoutProvenance.Spec.Provenance = nil
	for _, baseline := range []Device{syntheticDevice(), reportedDevice(), withoutProvenance} {
		attached := baseline.DeepCopyObject().(*Device)
		attached.Spec.DeviceTypeRef = &LocalReference{Name: model.Name}
		if errs := admit(t, "Device", object(t, attached), object(t, baseline)); len(errs) != 0 {
			t.Fatal(errs)
		}
		for _, reference := range []*LocalReference{nil, {Name: "another-model"}} {
			rebound := attached.DeepCopyObject().(*Device)
			rebound.Spec.DeviceTypeRef = reference
			if errs := admit(t, "Device", object(t, rebound), object(t, attached)); len(errs) == 0 {
				t.Fatal("accepted catalog removal or rebind")
			}
		}
		attached.Spec.DeviceTypeRef = nil
		attached.Spec.Provenance = baseline.Spec.Provenance
		if !reflect.DeepEqual(object(t, attached.Spec), object(t, baseline.Spec)) {
			t.Fatal("catalog attachment changed an observation or physical identity")
		}
	}
}

type deniedCatalogReader struct{ client.Reader }

func (deniedCatalogReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return fmt.Errorf("denied secret manufacturer and model")
}

func TestCatalogResolutionAndDeepCopy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	manufacturer, model := catalogRecords()
	device := syntheticDevice()
	device.Spec.DeviceTypeRef = &LocalReference{Name: model.Name}
	before := object(t, device)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(manufacturer, model).Build()
	resolved, vendor, err := ReadDeviceCatalog(context.Background(), reader, &device)
	if err != nil || vendor.Name != manufacturer.Name || resolved.Name != model.Name {
		t.Fatal("same-namespace reference failed", err)
	}
	resolved.Spec.ComponentTemplates[0].Name = "mutated copy"
	if model.Spec.ComponentTemplates[0].Name != "Port 1" || !reflect.DeepEqual(before, object(t, device)) {
		t.Fatal("resolution mutated source or device")
	}
	for _, namespace := range []string{"", "another-namespace"} {
		other := device.DeepCopyObject().(*Device)
		other.Namespace = namespace
		if model, vendor, err := ReadDeviceCatalog(context.Background(), reader, other); err == nil || model != nil || vendor != nil {
			t.Fatal("resolved across namespace boundary")
		}
	}
	if model, vendor, err := ReadDeviceCatalog(context.Background(), deniedCatalogReader{}, &device); err == nil || err.Error() != "hardware catalog unavailable" || model != nil || vendor != nil {
		t.Fatal("denied reference disclosed details")
	}
	if err := reader.Delete(context.Background(), manufacturer); err != nil {
		t.Fatal(err)
	}
	if model, vendor, err := ReadDeviceCatalog(context.Background(), reader, &device); err == nil || model != nil || vendor != nil {
		t.Fatal("dangling reference returned partial catalog")
	}
	replacement := manufacturer.DeepCopyObject().(*Manufacturer)
	replacement.ResourceVersion = ""
	replacement.UID = "replacement"
	replacement.Spec.CanonicalName = "Replacement Vendor"
	if err := reader.Create(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	_, vendor, err = ReadDeviceCatalog(context.Background(), reader, &device)
	if err != nil || vendor.UID != "replacement" || !reflect.DeepEqual(before, object(t, device)) {
		t.Fatal("name reuse must resolve without rewriting device", err)
	}
}
