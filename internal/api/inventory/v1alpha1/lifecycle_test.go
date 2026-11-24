package v1alpha1

import "testing"

func lifecycleDevice(t *testing.T, lifecycle DeviceLifecycle) map[string]any {
	t.Helper()
	device := syntheticDevice()
	device.Spec.Lifecycle = lifecycle
	return object(t, device)
}

func TestDeviceLifecycleDefaultsToActive(t *testing.T) {
	device := lifecycleDevice(t, "")
	if errs := admit(t, "Device", device, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := device["spec"].(map[string]any)["lifecycle"]; got != string(DeviceActive) {
		t.Fatalf("unset lifecycle defaulted to %v, want Active", got)
	}
	if got := LifecycleOf(&Device{}); got != DeviceActive {
		t.Fatalf("LifecycleOf(unset) = %q, want Active", got)
	}
	if errs := admit(t, "Device", lifecycleDevice(t, "Unknown"), nil); len(errs) == 0 {
		t.Fatal("an unknown lifecycle value was accepted")
	}
}

func TestDeviceLifecycleTransitions(t *testing.T) {
	for _, test := range []struct {
		from, to DeviceLifecycle
		allowed  bool
	}{
		{DeviceActive, DeviceActive, true},
		{DeviceActive, DeviceDecommissioning, true},
		{DeviceDecommissioning, DeviceActive, true},
		{DeviceDecommissioning, DeviceDecommissioning, true},
		{DeviceDecommissioning, DeviceRetired, true},
		{DeviceRetired, DeviceRetired, true},
		// An unset value is Active, before and after.
		{"", DeviceDecommissioning, true},
		{DeviceDecommissioning, "", true},
		{DeviceActive, DeviceRetired, false},
		{"", DeviceRetired, false},
		{DeviceRetired, DeviceActive, false},
		{DeviceRetired, DeviceDecommissioning, false},
		// Removing the field defaults it to Active, so it is not a way out.
		{DeviceRetired, "", false},
	} {
		errs := admit(t, "Device", lifecycleDevice(t, test.to), lifecycleDevice(t, test.from))
		if allowed := len(errs) == 0; allowed != test.allowed {
			t.Errorf("%q -> %q: allowed = %v, want %v (%v)", test.from, test.to, allowed, test.allowed, errs)
		}
	}
}

// TestRetiredDeviceStillAcceptsOtherEdits pins that the terminal rule only
// freezes lifecycle, so a Retired record can still be corrected.
func TestRetiredDeviceStillAcceptsOtherEdits(t *testing.T) {
	old := syntheticDevice()
	old.Spec.Lifecycle = DeviceRetired
	edited := syntheticDevice()
	edited.Spec.Lifecycle = DeviceRetired
	edited.Spec.AssetTag = "asset-0001"
	if errs := admit(t, "Device", object(t, edited), object(t, old)); len(errs) > 0 {
		t.Fatalf("edit of a Retired Device rejected: %v", errs)
	}
}

// TestDeviceMayBeCreatedInAnyLifecycle pins that transition rules apply to
// updates only: a record of hardware already out of service may be created
// as such.
func TestDeviceMayBeCreatedInAnyLifecycle(t *testing.T) {
	for _, lifecycle := range []DeviceLifecycle{DeviceActive, DeviceDecommissioning, DeviceRetired} {
		if errs := admit(t, "Device", lifecycleDevice(t, lifecycle), nil); len(errs) > 0 {
			t.Errorf("create as %s rejected: %v", lifecycle, errs)
		}
	}
}
