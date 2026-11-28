package registration

import (
	"context"
	"errors"
	"strings"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func setLifecycle(t *testing.T, handler *Handler, device *inventory.Device, lifecycle inventory.DeviceLifecycle) {
	t.Helper()
	device.Spec.Lifecycle = lifecycle
	if err := providerClient(t, handler).Update(context.Background(), device); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationRequiresAnActiveDevice(t *testing.T) {
	for _, lifecycle := range []inventory.DeviceLifecycle{inventory.DeviceDecommissioning, inventory.DeviceRetired} {
		t.Run(string(lifecycle), func(t *testing.T) {
			handler, device := fixture(t)
			setLifecycle(t, handler, device, lifecycle)
			response := handler.Handle(context.Background(), registrationRequest(t, device))
			if response.Allowed || !strings.Contains(response.Result.Message, "selected Device is "+string(lifecycle)) {
				t.Fatalf("registration of a %s Device: %+v", lifecycle, response.Result)
			}
		})
	}
	t.Run("explicit Active", func(t *testing.T) {
		handler, device := fixture(t)
		setLifecycle(t, handler, device, inventory.DeviceActive)
		if response := handler.Handle(context.Background(), registrationRequest(t, device)); !response.Allowed {
			t.Fatalf("registration of an Active Device denied: %s", response.Result.Message)
		}
	})
}

// TestRetiredDeviceStillBlocksDuplicates pins that a Retired Device keeps its
// identity in the duplicate check for as long as it exists.
func TestRetiredDeviceStillBlocksDuplicates(t *testing.T) {
	handler, device := fixture(t)
	retired := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "retired", Namespace: "default", UID: "device-2"}, Spec: inventory.DeviceSpec{Lifecycle: inventory.DeviceRetired, Identity: inventory.DeviceIdentity{Serial: " serial-1", SystemUUID: "22222222-2222-2222-2222-222222222222", Model: "test"}}}
	if err := providerClient(t, handler).Create(context.Background(), retired); err != nil {
		t.Fatal(err)
	}
	response := handler.Handle(context.Background(), registrationRequest(t, device))
	if response.Allowed || !strings.Contains(response.Result.Message, "shares recorded hardware identity") {
		t.Fatalf("registration beside a Retired duplicate: %+v", response.Result)
	}
}

func capacityUpdate(t *testing.T, device *inventory.Device, policy string) admission.Request {
	t.Helper()
	request := registrationRequest(t, device)
	old := request.Object
	changeCapacity(&request, func(capacity *metal.MachineCapacity) { capacity.Spec.AllocationPolicy = policy })
	request.Operation = admissionv1.Update
	request.OldObject = old
	return request
}

func TestCapacityCannotLeaveDisabledForInactiveDevice(t *testing.T) {
	for _, lifecycle := range []inventory.DeviceLifecycle{inventory.DeviceDecommissioning, inventory.DeviceRetired} {
		t.Run(string(lifecycle), func(t *testing.T) {
			handler, device := fixture(t)
			setLifecycle(t, handler, device, lifecycle)
			response := handler.Handle(context.Background(), capacityUpdate(t, device, "Enabled"))
			if response.Allowed || !strings.Contains(response.Result.Message, "its Device is "+string(lifecycle)) {
				t.Fatalf("enabling capacity of a %s Device: %+v", lifecycle, response.Result)
			}
		})
	}
	// An Active Device's capacity is enabled only with a pool label and a
	// resolving cleaning policy; an update that stays Disabled is allowed.
	t.Run("Active enabled without a pool", func(t *testing.T) {
		handler, device := fixture(t)
		response := handler.Handle(context.Background(), capacityUpdate(t, device, "Enabled"))
		if response.Allowed || !strings.Contains(response.Result.Message, "capacity-pool label") {
			t.Fatalf("capacity update: %+v", response.Result)
		}
	})
	for name, lifecycle := range map[string]inventory.DeviceLifecycle{"Retired staying Disabled": inventory.DeviceRetired, "Decommissioning unchanged": inventory.DeviceDecommissioning} {
		t.Run(name, func(t *testing.T) {
			handler, device := fixture(t)
			setLifecycle(t, handler, device, lifecycle)
			if response := handler.Handle(context.Background(), capacityUpdate(t, device, "Disabled")); !response.Allowed {
				t.Fatalf("capacity update: %+v", response.Result)
			}
		})
	}
	t.Run("caller without Device read learns nothing", func(t *testing.T) {
		handler, device := fixture(t)
		setLifecycle(t, handler, device, inventory.DeviceRetired)
		handler.Authorize = func(_ context.Context, _ Project, _ authenticationv1.UserInfo, _, _, resource, _ string) error {
			if resource == "devices" {
				return errors.New("get permission on devices is required")
			}
			return nil
		}
		response := handler.Handle(context.Background(), capacityUpdate(t, device, "Enabled"))
		if response.Allowed || strings.Contains(response.Result.Message, "Retired") {
			t.Fatalf("capacity update without Device read: %+v", response.Result)
		}
	})
}
