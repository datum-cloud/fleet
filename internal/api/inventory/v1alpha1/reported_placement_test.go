package v1alpha1

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPlacementJSONPatchPreconditionsBeforeAdmission(t *testing.T) {
	baseline := reportedDevice()
	baseline.UID = "device-uid"
	baseline.ResourceVersion = "7"
	report := &ReportedPlacement{Revision: 2, ObservedAt: later(time.Minute), Reason: "Correct record", Reference: "ticket/example"}
	request, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(baseline.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": baseline.ResourceVersion},
		{"op": "add", "path": "/spec/reportedPlacement", "value": report},
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
			if !reflect.DeepEqual(afterSpec["identity"], beforeSpec["identity"]) || changed.Spec.ClaimRef != baseline.Spec.ClaimRef {
				t.Fatal("changed protected fields")
			}
		})
	}
}

func reportedDevice() Device {
	device := syntheticDevice()
	device.Spec.ReportedPlacement = &ReportedPlacement{Revision: 1, ObservedAt: metav1.NewTime(baseTime), Reason: "Initial report", Reference: "ticket/initial", RackRef: &LocalReference{Name: "rack-a"}}
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
		"missing reason":    func(device *Device) { device.Spec.ReportedPlacement.Reason = "" },
		"missing reference": func(device *Device) { device.Spec.ReportedPlacement.Reference = "" },
		"revision":          func(device *Device) { device.Spec.ReportedPlacement.Revision = 2 },
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
	} {
		t.Run(name, func(t *testing.T) {
			old := reportedDevice()
			device := old.DeepCopyObject().(*Device)
			mutate(device)
			device.Spec.DisplayName = "bundled edit"
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
	if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) != 0 {
		t.Fatal(errs)
	}
	old = device.DeepCopyObject().(*Device)
	device.Spec.DisplayName = "unrelated recordkeeping"
	if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestPlacementCorrections(t *testing.T) {
	for _, existing := range []bool{false, true} {
		old := reportedDevice()
		if !existing {
			old.Spec.ReportedPlacement = nil
		}
		device := old.DeepCopyObject().(*Device)
		revision := int32(1)
		if old.Spec.ReportedPlacement != nil {
			revision++
		}
		device.Spec.ReportedPlacement = &ReportedPlacement{Revision: revision, ObservedAt: later(time.Minute), Reason: "Correct record", Reference: "ticket/example", RackRef: &LocalReference{Name: "other-rack"}}
		if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) != 0 {
			t.Fatal(errs)
		}
		for name, mutate := range map[string]func(*Device){
			"missing reason":   func(record *Device) { record.Spec.ReportedPlacement.Reason = "" },
			"blank reason":     func(record *Device) { record.Spec.ReportedPlacement.Reason = "  " },
			"missing evidence": func(record *Device) { record.Spec.ReportedPlacement.Reference = "" },
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
		unknown := device.DeepCopyObject().(*Device)
		unknown.Spec.ReportedPlacement.RackRef = nil
		unknown.Spec.ReportedPlacement.Revision++
		if errs := admit(t, "Device", object(t, unknown), object(t, device)); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
}
