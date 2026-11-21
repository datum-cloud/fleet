package v1alpha1

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPlacementJSONPatchPreconditionsBeforeAdmission(t *testing.T) {
	baseline := reportedDevice()
	baseline.UID = "device-uid"
	baseline.ResourceVersion = "7"
	report := &ReportedPlacement{Revision: 2, Method: "Manual", ObservedAt: later(time.Minute), Reason: "Correct record", Reference: "ticket/example"}
	request, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(baseline.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": baseline.ResourceVersion},
		{"op": "add", "path": "/spec/reportedPlacement", "value": report},
		{"op": "replace", "path": "/spec/provenance/recordedAt", "value": later(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	patch, err := jsonpatch.DecodePatch(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, conflict := range []string{"", "uid", "resourceVersion"} {
		t.Run(conflict, func(t *testing.T) {
			current := baseline.DeepCopyObject().(*Device)
			if conflict == "uid" {
				current.UID = "replacement"
			}
			if conflict == "resourceVersion" {
				current.ResourceVersion = "8"
			}
			before, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			retained := bytes.Clone(before)
			candidate, err := patch.Apply(before)
			if !bytes.Equal(before, retained) {
				t.Fatal("mutated input before commit")
			}
			if conflict != "" {
				if err == nil || candidate != nil {
					t.Fatal("precondition did not reject before mutation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var changed Device
			if err := json.Unmarshal(candidate, &changed); err != nil {
				t.Fatal(err)
			}
			if errs := admit(t, "Device", object(t, changed), object(t, current)); len(errs) != 0 {
				t.Fatal(errs)
			}
			beforeSpec := object(t, baseline)["spec"].(map[string]any)
			afterSpec := object(t, changed)["spec"].(map[string]any)
			if !reflect.DeepEqual(afterSpec["provenance"].(map[string]any)["source"], beforeSpec["provenance"].(map[string]any)["source"]) || !reflect.DeepEqual(afterSpec["identity"], beforeSpec["identity"]) || changed.Spec.ClaimRef != baseline.Spec.ClaimRef {
				t.Fatal("changed protected fields")
			}
		})
	}
}

func reportedDevice() Device {
	device := syntheticDevice()
	source := ExternalReference{System: "example-netbox", Type: "dcim/devices", ID: "1"}
	device.Spec.Identity.SourceIdentity = &source
	device.Spec.Provenance.Method = "NetBoxImport"
	device.Spec.Provenance.Source = &SourceReference{ExternalReference: source, SnapshotDigest: "sha256:" + strings.Repeat("a", 64), MappingRevision: "example-1", ObservedAt: metav1.NewTime(recordedAt)}
	device.Spec.ReportedPlacement = &ReportedPlacement{Revision: 1, ObservedAt: metav1.NewTime(recordedAt), SnapshotDigest: device.Spec.Provenance.Source.SnapshotDigest, RackRef: &LocalReference{Name: "rack-a"}}
	return device
}

func TestReportedPlacementAdmission(t *testing.T) {
	for _, device := range []Device{syntheticDevice(), reportedDevice()} {
		for _, previous := range []map[string]any{nil, object(t, device)} {
			if errs := admit(t, "Device", object(t, device), previous); len(errs) != 0 {
				t.Fatal(errs)
			}
		}
	}
	for _, height := range []float64{0.5, 1, 4} {
		device := reportedDevice()
		position := 12.5
		device.Spec.ReportedPlacement.Height = &height
		device.Spec.ReportedPlacement.Position = &position
		if errs := admit(t, "Device", object(t, device), nil); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	for name, mutate := range map[string]func(*Device){
		"negative":       func(device *Device) { value := -1.0; device.Spec.ReportedPlacement.Position = &value },
		"position range": func(device *Device) { value := 101.0; device.Spec.ReportedPlacement.Position = &value },
		"height range":   func(device *Device) { value := 101.0; device.Spec.ReportedPlacement.Height = &value },
		"end range": func(device *Device) {
			position, height := 100.0, 2.0
			device.Spec.ReportedPlacement.Position = &position
			device.Spec.ReportedPlacement.Height = &height
		},
		"zero height":  func(device *Device) { value := 0.0; device.Spec.ReportedPlacement.Height = &value },
		"invalid face": func(device *Device) { device.Spec.ReportedPlacement.Face = "unknown" },
		"geometry without rack": func(device *Device) {
			device.Spec.ReportedPlacement.RackRef = nil
			device.Spec.ReportedPlacement.Face = "front"
		},
		"mount and units": func(device *Device) {
			device.Spec.ReportedPlacement.Mount = "side"
			device.Spec.ReportedPlacement.Face = "front"
		},
		"snapshot": func(device *Device) {
			device.Spec.ReportedPlacement.SnapshotDigest = "sha256:" + strings.Repeat("b", 64)
		},
		"time":     func(device *Device) { device.Spec.ReportedPlacement.ObservedAt = later(time.Hour) },
		"revision": func(device *Device) { device.Spec.ReportedPlacement.Revision = 2 },
		"manual":   func(device *Device) { device.Spec.Provenance.Method = "Manual" },
	} {
		t.Run(name, func(t *testing.T) {
			device := reportedDevice()
			mutate(&device)
			if errs := admit(t, "Device", object(t, device), nil); len(errs) == 0 {
				t.Fatal("accepted invalid report")
			}
		})
	}
}

func TestPlacementCorrectionProtectsIdentityAndRevision(t *testing.T) {
	for name, mutate := range map[string]func(*Device){
		"stale revision enrichment": func(device *Device) { value := 12.0; device.Spec.ReportedPlacement.Position = &value },
		"stale revision rack move":  func(device *Device) { device.Spec.ReportedPlacement.RackRef.Name = "rack-b" },
		"removal":                   func(device *Device) { device.Spec.ReportedPlacement = nil },
		"rebinding":                 func(device *Device) { device.Spec.ClaimRef.Name = "other" },
		"identity":                  func(device *Device) { device.Spec.Identity.Serial = "other" },
		"source": func(device *Device) {
			device.Spec.Provenance.Source.ID = "other"
			device.Spec.Identity.SourceIdentity.ID = "other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			old := reportedDevice()
			device := old.DeepCopyObject().(*Device)
			mutate(device)
			device.Spec.DisplayName = "bundled edit"
			device.Spec.Provenance.RecordedAt = later(time.Hour)
			device.Spec.Provenance.RecordedBy = "forged-administrator"
			device.Labels = map[string]string{"owner": "operator", "audit-approved": "true"}
			device.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "fleet-staff-inventory-ui", Operation: metav1.ManagedFieldsOperationUpdate}}
			if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) == 0 {
				t.Fatal("direct update bypassed gate")
			}
		})
	}
	device := reportedDevice()
	old := device.DeepCopyObject().(*Device)
	old.Spec.ReportedPlacement = nil
	device.Spec.Provenance.RecordedAt = later(time.Hour)
	if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) != 0 {
		t.Fatal(errs)
	}
	old = device.DeepCopyObject().(*Device)
	device.Spec.DisplayName = "unrelated recordkeeping"
	device.Spec.Provenance.RecordedAt = later(2 * time.Hour)
	if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestManualPlacementCorrections(t *testing.T) {
	for _, imported := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			old := syntheticDevice()
			if imported {
				old = reportedDevice()
			}
			if existing && !imported {
				old.Spec.ReportedPlacement = &ReportedPlacement{Revision: 1, Method: "Manual", ObservedAt: metav1.NewTime(recordedAt), Reason: "Initial report", Reference: "ticket/initial"}
			}
			if !existing {
				old.Spec.ReportedPlacement = nil
			}
			device := old.DeepCopyObject().(*Device)
			revision := int32(1)
			if old.Spec.ReportedPlacement != nil {
				revision++
			}
			device.Spec.ReportedPlacement = &ReportedPlacement{Revision: revision, Method: "Manual", ObservedAt: later(time.Minute), Reason: "Correct record", Reference: "ticket/example", RackRef: &LocalReference{Name: "other-rack"}}
			device.Spec.Provenance.RecordedAt = later(time.Hour)
			if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) != 0 {
				t.Fatal(errs)
			}
			for name, mutate := range map[string]func(*Device){
				"missing reason":   func(record *Device) { record.Spec.ReportedPlacement.Reason = "" },
				"blank reason":     func(record *Device) { record.Spec.ReportedPlacement.Reason = "  " },
				"missing evidence": func(record *Device) { record.Spec.ReportedPlacement.Reference = "" },
				"false snapshot attribution": func(record *Device) {
					record.Spec.ReportedPlacement.SnapshotDigest = "sha256:" + strings.Repeat("a", 64)
				},
				"skipped revision": func(record *Device) { record.Spec.ReportedPlacement.Revision += 2 },
				"claim binding":    func(record *Device) { record.Spec.ClaimRef.Name = "other" },
				"identity":         func(record *Device) { record.Spec.Identity.Serial = "other" },
			} {
				t.Run(name, func(t *testing.T) {
					invalid := device.DeepCopyObject().(*Device)
					mutate(invalid)
					if errs := admit(t, "Device", object(t, invalid), object(t, old)); len(errs) == 0 {
						t.Fatal("accepted invalid correction")
					}
				})
			}
			if imported {
				for _, field := range []string{"method", "system", "type", "id", "removal"} {
					invalid := device.DeepCopyObject().(*Device)
					switch field {
					case "method":
						invalid.Spec.Provenance.Method = "Manual"
					case "system":
						invalid.Spec.Provenance.Source.System = "other"
					case "type":
						invalid.Spec.Provenance.Source.Type = "other"
					case "id":
						invalid.Spec.Provenance.Source.ID = "other"
					case "removal":
						invalid.Spec.Provenance.Source = nil
					}
					if errs := admit(t, "Device", object(t, invalid), object(t, old)); len(errs) == 0 {
						t.Fatalf("accepted changed import %s", field)
					}
				}
			}
			unknown := device.DeepCopyObject().(*Device)
			unknown.Spec.ReportedPlacement.RackRef = nil
			unknown.Spec.ReportedPlacement.Revision++
			unknown.Spec.Provenance.RecordedAt = later(2 * time.Hour)
			if errs := admit(t, "Device", object(t, unknown), object(t, device)); len(errs) != 0 {
				t.Fatal(errs)
			}
		}
	}
}
