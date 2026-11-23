package v1alpha1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// syntheticPatchPanel is passive equipment with no serial or system UUID on
// record, registered with the explicit marker so Cables can reference it.
func syntheticPatchPanel() Device {
	device := syntheticDevice()
	device.Name = "rack-01-u40-patch-panel"
	device.Spec.ClaimRef = LocalReference{Name: "rack-01-u40"}
	device.Spec.Identity = DeviceIdentity{Manufacturer: "Example Vendor", Model: "Example 24-port panel", Unidentified: true, UnidentifiedReason: "passive patch panel; no serial on the chassis"}
	device.Spec.BMC, device.Spec.Interfaces, device.Spec.UnmappedMACAddresses = nil, nil, nil
	device.Spec.TopologyAuthority = "Cable"
	device.Spec.Components = []Component{{ID: "front", Name: "Front ports", Type: "FrontPort", Positions: 24}, {ID: "rear", Name: "Rear ports", Type: "RearPort", Positions: 24}}
	return device
}

func rejectedWith(errs field.ErrorList, message string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), message) {
			return true
		}
	}
	return false
}

func TestUnidentifiedMarkerAdmitsDeviceWithoutSerial(t *testing.T) {
	if errs := admit(t, "Device", object(t, syntheticPatchPanel()), nil); len(errs) > 0 {
		t.Fatalf("unidentified device rejected: %v", errs)
	}
}

func TestUnidentifiedMarkerRules(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(*DeviceIdentity)
		message string
	}{
		"marker with serial":      {func(i *DeviceIdentity) { i.Serial = "SYNTHETIC-0040" }, "cannot carry a serial or system UUID"},
		"marker with system UUID": {func(i *DeviceIdentity) { i.SystemUUID = "00000000-0000-4000-8000-000000000040" }, "cannot carry a serial or system UUID"},
		"neither marker nor identity": {func(i *DeviceIdentity) { i.Unidentified, i.UnidentifiedReason = false, "" },
			"a device needs a serial or system UUID"},
		"marker without reason": {func(i *DeviceIdentity) { i.UnidentifiedReason = "" }, "must be set together"},
		"blank reason":          {func(i *DeviceIdentity) { i.UnidentifiedReason = " \t " }, "unidentifiedReason"},
		"reason without marker": {func(i *DeviceIdentity) { i.Unidentified, i.Serial = false, "SYNTHETIC-0040" }, "must be set together"},
		"overlong reason":       {func(i *DeviceIdentity) { i.UnidentifiedReason = strings.Repeat("x", 1025) }, "unidentifiedReason"},
	} {
		device := syntheticPatchPanel()
		tc.mutate(&device.Spec.Identity)
		if errs := admit(t, "Device", object(t, device), nil); !rejectedWith(errs, tc.message) {
			t.Errorf("%s: want rejection containing %q, got %v", name, tc.message, errs)
		}
	}
	explicitFalse := object(t, syntheticDevice())
	explicitFalse["spec"].(map[string]any)["identity"].(map[string]any)["unidentified"] = false
	if errs := admit(t, "Device", explicitFalse, nil); !rejectedWith(errs, "unidentified may only be true") {
		t.Errorf("unidentified: false accepted: %v", errs)
	}
}

func TestUnidentifiedIdentityIsImmutable(t *testing.T) {
	old := object(t, syntheticPatchPanel())
	identified := syntheticPatchPanel()
	identified.Spec.Identity.Unidentified, identified.Spec.Identity.UnidentifiedReason, identified.Spec.Identity.Serial = false, "", "SYNTHETIC-0040"
	for name, mutate := range map[string]func(*DeviceIdentity){
		"add serial":    func(i *DeviceIdentity) { *i = identified.Spec.Identity },
		"change reason": func(i *DeviceIdentity) { i.UnidentifiedReason = "cable manager" },
	} {
		device := syntheticPatchPanel()
		mutate(&device.Spec.Identity)
		if errs := admit(t, "Device", object(t, device), old); !rejectedWith(errs, "identity is immutable") {
			t.Errorf("%s: want immutability rejection, got %v", name, errs)
		}
	}
	marked := syntheticDevice()
	marked.Spec.BMC = nil
	marked.Spec.Identity = syntheticPatchPanel().Spec.Identity
	if errs := admit(t, "Device", object(t, marked), object(t, syntheticDevice())); !rejectedWith(errs, "identity is immutable") {
		t.Errorf("marking an identified device unidentified: want immutability rejection, got %v", errs)
	}
	renamed := syntheticPatchPanel()
	renamed.Spec.DisplayName = "Rack 01 patch panel"
	if errs := admit(t, "Device", object(t, renamed), old); len(errs) > 0 {
		t.Errorf("non-identity edit of an unidentified device rejected: %v", errs)
	}
}

// An unidentified Device cannot record a BMC, the management endpoint
// enrollment would use, and the capacity contract rejects it.
func TestUnidentifiedDeviceCannotBackCapacity(t *testing.T) {
	withBMC := syntheticPatchPanel()
	withBMC.Spec.BMC = &BMC{Endpoint: "https://192.0.2.140:443"}
	if errs := admit(t, "Device", object(t, withBMC), nil); !rejectedWith(errs, "cannot record a BMC endpoint") {
		t.Errorf("unidentified device with a BMC: want rejection, got %v", errs)
	}
	panel := syntheticPatchPanel()
	if err := CapacityBackingError(&panel); !errors.Is(err, ErrUnidentifiedDevice) {
		t.Errorf("unidentified device: got %v, want ErrUnidentifiedDevice", err)
	}
	reasonOnly := syntheticPatchPanel()
	reasonOnly.Spec.Identity.Unidentified = false
	if err := CapacityBackingError(&reasonOnly); !errors.Is(err, ErrUnidentifiedDevice) {
		t.Errorf("device with only an unidentified reason: got %v, want ErrUnidentifiedDevice", err)
	}
	pruned := syntheticDevice()
	pruned.Spec.Identity.Serial, pruned.Spec.Identity.SystemUUID = "", ""
	if err := CapacityBackingError(&pruned); !errors.Is(err, ErrDeviceIdentityMissing) {
		t.Errorf("device without identity: got %v, want ErrDeviceIdentityMissing", err)
	}
	if err := CapacityBackingError(nil); !errors.Is(err, ErrDeviceIdentityMissing) {
		t.Errorf("missing device: got %v, want ErrDeviceIdentityMissing", err)
	}
	for _, mutate := range []func(*DeviceIdentity){
		func(i *DeviceIdentity) {},
		func(i *DeviceIdentity) { i.SystemUUID = "" },
		func(i *DeviceIdentity) { i.Serial = "" },
	} {
		device := syntheticDevice()
		mutate(&device.Spec.Identity)
		if err := CapacityBackingError(&device); err != nil {
			t.Errorf("identified device %+v rejected: %v", device.Spec.Identity, err)
		}
	}
}

// TestUnidentifiedMarkerAgainstAPIServer checks the same rules in a real API
// server, including the update path. It needs envtest binaries
// (KUBEBUILDER_ASSETS) and is skipped without them.
func TestUnidentifiedMarkerAgainstAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; see docs/development/README.md")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(inventoryRoot, "crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	toUnstructured := func(device Device) *unstructured.Unstructured {
		device.Namespace = "default"
		return &unstructured.Unstructured{Object: object(t, device)}
	}

	panel := toUnstructured(syntheticPatchPanel())
	if err := c.Create(ctx, panel); err != nil {
		t.Fatalf("create unidentified device: %v", err)
	}
	for name, mutate := range map[string]func(*Device){
		"marker with serial": func(d *Device) { d.Spec.Identity.Serial = "SYNTHETIC-0041" },
		"blank reason":       func(d *Device) { d.Spec.Identity.UnidentifiedReason = " " },
		"neither": func(d *Device) {
			d.Spec.Identity.Unidentified, d.Spec.Identity.UnidentifiedReason = false, ""
		},
		"BMC": func(d *Device) { d.Spec.BMC = &BMC{Endpoint: "https://192.0.2.141:443"} },
	} {
		device := syntheticPatchPanel()
		device.Name = "rejected-" + strings.ReplaceAll(name, " ", "-")
		mutate(&device)
		if err := c.Create(ctx, toUnstructured(device)); err == nil {
			t.Errorf("%s: create accepted", name)
		}
	}

	identity := panel.Object["spec"].(map[string]any)["identity"].(map[string]any)
	delete(identity, "unidentified")
	delete(identity, "unidentifiedReason")
	identity["serial"] = "SYNTHETIC-0041"
	if err := c.Update(ctx, panel); err == nil || !strings.Contains(err.Error(), "identity is immutable") {
		t.Errorf("adding a serial to an unidentified device: got %v, want immutability rejection", err)
	}
}
