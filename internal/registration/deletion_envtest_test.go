package registration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const deletionDenied = "Only a Retired Device can be deleted"

func (api *liveAPI) namespace(name string) *corev1.Namespace {
	api.t.Helper()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := api.admin.Create(context.Background(), namespace); err != nil {
		api.t.Fatal(err)
	}
	return namespace
}

// TestDeviceLifecycleTransitionsAgainstLiveAPI runs the CRD transition rules
// in a real API server.
func TestDeviceLifecycleTransitionsAgainstLiveAPI(t *testing.T) {
	api := startAPI(t)
	device := api.device("lifecycle", "default", "lifecycle-serial", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1")
	if device.Spec.Lifecycle != inventory.DeviceActive {
		t.Fatalf("created Device lifecycle = %q, want the Active default", device.Spec.Lifecycle)
	}
	rejected := func(to inventory.DeviceLifecycle, message string) {
		t.Helper()
		if err := api.updateLifecycle(device, to); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), message) {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	rejected(inventory.DeviceRetired, "move an Active Device to Decommissioning before Retired")
	api.setLifecycle(device, inventory.DeviceDecommissioning)
	api.setLifecycle(device, inventory.DeviceActive)
	api.setLifecycle(device, inventory.DeviceDecommissioning)
	api.setLifecycle(device, inventory.DeviceRetired)
	rejected(inventory.DeviceActive, "Retired is terminal")
	rejected(inventory.DeviceDecommissioning, "Retired is terminal")
	rejected("", "Retired is terminal")
	api.setLifecycle(device, inventory.DeviceRetired)
}

// TestDeviceDeletionProtection applies the component's manifests as a
// deploying repository that opts in would, and checks every deletion path.
func TestDeviceDeletionProtection(t *testing.T) {
	api := startAPI(t)
	ctx := context.Background()
	active := api.device("active", "default", "active-serial", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1")
	decommissioning := api.device("decommissioning", "default", "decommissioning-serial", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2")
	api.setLifecycle(decommissioning, inventory.DeviceDecommissioning)
	retired := api.device("retired", "default", "retired-serial", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb3")
	api.retire(retired)

	api.namespace("mixed")
	mixed := map[string]inventory.DeviceLifecycle{"mixed-active": inventory.DeviceActive, "mixed-decommissioning": inventory.DeviceDecommissioning, "mixed-retired-a": inventory.DeviceRetired, "mixed-retired-b": inventory.DeviceRetired}
	mixedDevices := map[string]*inventory.Device{}
	suffix := 0
	for name, lifecycle := range mixed {
		suffix++
		device := api.device(name, "mixed", name+"-serial", fmt.Sprintf("cccccccc-cccc-4ccc-8ccc-%012d", suffix))
		switch lifecycle {
		case inventory.DeviceDecommissioning:
			api.setLifecycle(device, lifecycle)
		case inventory.DeviceRetired:
			api.retire(device)
		}
		mixedDevices[name] = device
	}

	teardown := api.namespace("teardown")
	teardownActive := api.device("teardown-active", "teardown", "teardown-active-serial", "dddddddd-dddd-4ddd-8ddd-ddddddddddd1")
	teardownDecommissioning := api.device("teardown-decommissioning", "teardown", "teardown-decommissioning-serial", "dddddddd-dddd-4ddd-8ddd-ddddddddddd2")
	api.setLifecycle(teardownDecommissioning, inventory.DeviceDecommissioning)

	api.installDeletionProtection(active)
	operator := api.actor("operator", "system:masters")

	t.Run("Active and Decommissioning Devices cannot be deleted by any caller", func(t *testing.T) {
		for _, device := range []*inventory.Device{active, decommissioning} {
			for _, caller := range []client.Client{api.admin, operator} {
				if err := caller.Delete(ctx, device); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), deletionDenied) || !strings.Contains(err.Error(), "Decommissioning, then Retired") {
					t.Fatalf("deleting %s: %v", device.Name, err)
				}
			}
			if !api.exists(device) {
				t.Fatalf("%s removed despite deletion protection", device.Name)
			}
		}
	})
	t.Run("a Retired Device can be deleted", func(t *testing.T) {
		if err := operator.Delete(ctx, retired); err != nil {
			t.Fatalf("deleting a Retired Device: %v", err)
		}
		if api.exists(retired) {
			t.Fatal("Retired Device survived deletion")
		}
	})
	t.Run("deletecollection denies the Devices that are not Retired", func(t *testing.T) {
		if err := api.admin.DeleteAllOf(ctx, &inventory.Device{}, client.InNamespace("mixed")); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), deletionDenied) {
			t.Fatalf("deletecollection over mixed Devices: %v", err)
		}
		// The API server stops a collection delete at its first denial, so
		// each Retired Device is either already gone or deletable now.
		for name, lifecycle := range mixed {
			device := mixedDevices[name]
			err := api.admin.Delete(ctx, device)
			switch {
			case lifecycle == inventory.DeviceRetired && err != nil && !apierrors.IsNotFound(err):
				t.Fatalf("deleting Retired %s after deletecollection: %v", name, err)
			case lifecycle != inventory.DeviceRetired && (!apierrors.IsForbidden(err) || !api.exists(device)):
				t.Fatalf("%s (%s) is not protected after deletecollection: %v", name, lifecycle, err)
			case lifecycle == inventory.DeviceRetired && api.exists(device):
				t.Fatalf("Retired %s survived", name)
			}
		}
	})
	t.Run("deleting the namespace lets its Devices be deleted", func(t *testing.T) {
		if err := api.admin.Delete(ctx, teardownActive); !apierrors.IsForbidden(err) {
			t.Fatalf("Device deletion before namespace deletion: %v", err)
		}
		if err := api.admin.Delete(ctx, teardown); err != nil {
			t.Fatal(err)
		}
		if !api.exists(teardown) || teardown.DeletionTimestamp.IsZero() {
			t.Fatal("namespace is not marked for deletion")
		}
		// The policy reads namespaceObject from the API server's namespace
		// cache, which observes the deletion mark shortly after it is set.
		api.eventually("namespace deletion mark", func() bool {
			return api.admin.Delete(ctx, teardownActive, client.DryRunAll) == nil
		})
		// envtest runs no namespace controller. Issue the content deletions
		// the controller would: deletecollection, then per item.
		if err := api.admin.DeleteAllOf(ctx, &inventory.Device{}, client.InNamespace("teardown")); err != nil {
			t.Fatalf("namespace cleanup deletecollection: %v", err)
		}
		for _, device := range []*inventory.Device{teardownActive, teardownDecommissioning} {
			if err := api.admin.Delete(ctx, device); err != nil && !apierrors.IsNotFound(err) {
				t.Fatalf("namespace cleanup of %s: %v", device.Name, err)
			}
			if api.exists(device) {
				t.Fatalf("%s survived namespace cleanup", device.Name)
			}
		}
	})
	t.Run("Devices in other namespaces stay protected", func(t *testing.T) {
		if err := api.admin.Delete(ctx, active); !apierrors.IsForbidden(err) || !api.exists(active) {
			t.Fatalf("Active Device outside the deleting namespace: %v", err)
		}
	})
}
