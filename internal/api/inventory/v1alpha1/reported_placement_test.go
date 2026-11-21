package v1alpha1

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
		"negative":     func(device *Device) { value := -1.0; device.Spec.ReportedPlacement.Position = &value },
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

func TestPlacementCorrectionGateCannotBeBypassed(t *testing.T) {
	for name, mutate := range map[string]func(*Device){
		"same rack enrichment": func(device *Device) { value := 12.0; device.Spec.ReportedPlacement.Position = &value },
		"rack move":            func(device *Device) { device.Spec.ReportedPlacement.RackRef.Name = "rack-b" },
		"removal":              func(device *Device) { device.Spec.ReportedPlacement = nil },
		"rebinding":            func(device *Device) { device.Spec.ClaimRef.Name = "other" },
		"identity":             func(device *Device) { device.Spec.Identity.Serial = "other" },
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
	if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) == 0 {
		t.Fatal("accepted addition to existing imported device")
	}
	old = device.DeepCopyObject().(*Device)
	device.Spec.DisplayName = "unrelated recordkeeping"
	device.Spec.Provenance.RecordedAt = later(2 * time.Hour)
	if errs := admit(t, "Device", object(t, device), object(t, old)); len(errs) != 0 {
		t.Fatal(errs)
	}
}
