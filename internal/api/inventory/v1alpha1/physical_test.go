package v1alpha1

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const manualInventoryExample = "../../../../hack/testdata/physical/manual.yaml"

var inventoryRecords = flag.String("inventory-records", manualInventoryExample, "private YAML records to validate offline; never applies records")

func TestManualInventoryRecordsPassAdmission(t *testing.T) {
	data, err := os.ReadFile(*inventoryRecords)
	if err != nil {
		t.Fatal("cannot read inventory records")
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	crds := loadCRDs(t)
	seen := map[string]bool{}
	count := 0
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal("cannot decode inventory records")
		}
		if len(record) == 0 {
			continue
		}
		kind, ok := record["kind"].(string)
		if !ok || crds[kind] == nil {
			t.Fatal("expected one of the six physical kinds")
		}
		seen[kind] = true
		if *inventoryRecords == manualInventoryExample {
			spec := record["spec"].(map[string]any)
			provenance := spec["provenance"].(map[string]any)
			if provenance["method"] != "Manual" || provenance["source"] != nil || record["status"] != nil {
				t.Fatal("manual example must have no source dependency or status assertion")
			}
			if kind == "DeviceClaim" && spec["lifecycle"] != "Installation" || kind == "Device" && spec["topologyAuthority"] != "Cable" {
				t.Fatal("new manual records must use typed installation and Cable topology")
			}
		}
		for _, previous := range []map[string]any{nil, object(t, record)} {
			if errs := admit(t, kind, object(t, record), previous); len(errs) != 0 {
				t.Fatalf("record %d (%s) failed admission with %d errors; inspect privately", count+1, kind, len(errs))
			}
		}
		count++
	}
	if count == 0 {
		t.Fatal("no inventory records validated")
	}
	if *inventoryRecords == manualInventoryExample && len(seen) != 6 {
		t.Fatal("manual example must exercise all six physical kinds")
	}
	t.Logf("validated %d records for create and unchanged update; no API writes", count)
}

func TestSyntheticNetBoxHandoffPassesAdmission(t *testing.T) {
	data, err := os.ReadFile("../../../../hack/testdata/netbox/candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	for _, raw := range list.Items {
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		kind := record["kind"].(string)
		if errs := admit(t, kind, object(t, record), nil); len(errs) != 0 {
			t.Fatalf("%s: %v", kind, errs)
		}
	}
}

func TestTypedPlacementAndLegacyBinding(t *testing.T) {
	claim := syntheticClaim()
	claim.Spec.Lifecycle = "Installation"
	claim.Spec.Ports = nil
	claim.Spec.Placement = Placement{Scope: "RackMembership", RackRef: &LocalReference{Name: "rack-a"}}
	if errs := admit(t, "DeviceClaim", object(t, claim), nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, placement := range []Placement{
		{Scope: "Facility", FacilityRef: &LocalReference{Name: "facility-a"}},
		{Scope: "RackUnits", RackRef: &LocalReference{Name: "rack-a"}, Position: 12, Height: 1, Face: "front"},
		{Scope: "NonRackUnitMount", RackRef: &LocalReference{Name: "rack-a"}, Mount: "side-a"},
	} {
		candidate := claim
		candidate.Spec.Placement = placement
		if errs := admit(t, "DeviceClaim", object(t, candidate), nil); len(errs) != 0 {
			t.Fatal(errs)
		}
		candidate.Spec.Provenance.RecordedAt = later(time.Hour)
		if errs := admit(t, "DeviceClaim", object(t, candidate), object(t, claim)); len(errs) == 0 {
			t.Fatal("moved an installation by editing placement")
		}
	}
	candidate := claim
	candidate.Spec.Placement.Position = 1
	if errs := admit(t, "DeviceClaim", object(t, candidate), nil); len(errs) == 0 {
		t.Fatal("accepted synthetic units on rack membership")
	}
	candidate = claim
	candidate.Spec.Provenance.RecordedAt = later(time.Hour)
	if errs := admit(t, "DeviceClaim", object(t, candidate), object(t, syntheticClaim())); len(errs) == 0 {
		t.Fatal("converted a legacy slot in place")
	}
	candidate.Spec.Ports = syntheticClaim().Spec.Ports
	if errs := admit(t, "DeviceClaim", object(t, candidate), nil); len(errs) == 0 {
		t.Fatal("accepted competing inline topology")
	}
}

func TestPhysicalKindsSchemeAndAdmission(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	common := PhysicalMetadata{DisplayName: "Example", Provenance: provenance(recordedAt)}
	metadata := metav1.ObjectMeta{Name: "example", Namespace: "inventory"}
	records := []runtime.Object{
		&Facility{ObjectMeta: metadata, Spec: FacilitySpec{PhysicalMetadata: common}},
		&Rack{ObjectMeta: metadata, Spec: RackSpec{PhysicalMetadata: common, FacilityRef: LocalReference{Name: "example"}, HeightUnits: 42}},
		&PowerFeed{ObjectMeta: metadata, Spec: PowerFeedSpec{PhysicalMetadata: common, FacilityRef: LocalReference{Name: "example"}, Terminations: []Component{{ID: "output", Name: "output", Type: "PowerOutlet"}}}},
		&Cable{ObjectMeta: metadata, Spec: CableSpec{PhysicalMetadata: common, Medium: "Power", Lanes: []CableLane{{ID: "one", A: CableEndpoint{PowerFeedRef: &LocalReference{Name: "example"}, ComponentID: "output", Position: 1}, B: CableEndpoint{DeviceRef: &LocalReference{Name: "server"}, ComponentID: "inlet", Position: 1}}}}},
	}
	for _, record := range records {
		kinds, _, err := scheme.ObjectKinds(record)
		if err != nil {
			t.Fatal(err)
		}
		kind := kinds[0].Kind
		if errs := admit(t, kind, object(t, record.DeepCopyObject()), nil); len(errs) != 0 {
			t.Fatalf("%s: %v", kind, errs)
		}
	}
	cable := records[3].(*Cable)
	cable.Spec.Lanes[0].A.DeviceRef = &LocalReference{Name: "other"}
	if errs := admit(t, "Cable", object(t, cable), nil); len(errs) == 0 {
		t.Fatal("accepted ambiguous endpoint owner")
	}
	cable.Spec.Lanes[0].A.DeviceRef = nil
	cable.Spec.Lanes = append(cable.Spec.Lanes, cable.Spec.Lanes[0])
	cable.Spec.Lanes[1].ID = "two"
	if errs := admit(t, "Cable", object(t, cable), nil); len(errs) == 0 {
		t.Fatal("accepted duplicate lane endpoint")
	}
}

func TestObservationalIdentityAndComponentSafety(t *testing.T) {
	device := syntheticDevice()
	device.Spec.Interfaces = nil
	device.Spec.TopologyAuthority = "Cable"
	device.Spec.Identity.Serial, device.Spec.Identity.SystemUUID = "", ""
	source := ExternalReference{System: "example-netbox", Type: "dcim/devices", ID: "1"}
	device.Spec.Identity.SourceIdentity = &source
	device.Spec.Provenance.Method = "NetBoxImport"
	device.Spec.Provenance.Source = &SourceReference{ExternalReference: source, SnapshotDigest: "sha256:" + strings.Repeat("a", 64), MappingRevision: "example-1", ObservedAt: metav1.NewTime(recordedAt)}
	device.Spec.Components = []Component{{ID: "front", Name: "front", Type: "FrontPort"}, {ID: "rear", Name: "rear", Type: "RearPort", Positions: 2}}
	device.Spec.InternalMappings = []InternalMapping{{From: ComponentPosition{ComponentID: "front", Position: 1}, To: ComponentPosition{ComponentID: "rear", Position: 2}}}
	if errs := admit(t, "Device", object(t, device), nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	candidate := device.DeepCopyObject().(*Device)
	candidate.Spec.InternalMappings[0].To.Position = 3
	if errs := admit(t, "Device", object(t, candidate), nil); len(errs) == 0 {
		t.Fatal("accepted nonexistent panel position")
	}
	candidate = device.DeepCopyObject().(*Device)
	candidate.Spec.Components = append(candidate.Spec.Components, candidate.Spec.Components[0])
	if errs := admit(t, "Device", object(t, candidate), nil); len(errs) == 0 {
		t.Fatal("accepted duplicate component ID")
	}
	candidate = device.DeepCopyObject().(*Device)
	candidate.Spec.Components[1].Type = "NetworkPort"
	if errs := admit(t, "Device", object(t, candidate), nil); len(errs) == 0 {
		t.Fatal("accepted incompatible pass-through component types")
	}
	candidate = device.DeepCopyObject().(*Device)
	candidate.Spec.Provenance.Source.ID = "2"
	candidate.Spec.Provenance.RecordedAt = later(time.Hour)
	if errs := admit(t, "Device", object(t, candidate), object(t, device)); len(errs) == 0 {
		t.Fatal("accepted source identity takeover")
	}
	candidate = device.DeepCopyObject().(*Device)
	candidate.Spec.Interfaces = syntheticDevice().Spec.Interfaces
	if errs := admit(t, "Device", object(t, candidate), nil); len(errs) == 0 {
		t.Fatal("accepted Cable and inline topology writers")
	}
}
