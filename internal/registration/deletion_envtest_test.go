package registration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	deletionDenied = "A Device can be deleted only when it is Retired, when its namespace is being deleted, or by a configured cleanup identity"
	// Synthetic identities. The policy compares usernames only, so each actor
	// also holds system:masters to pass RBAC and prove groups do not matter.
	syntheticCleanupUser = "synthetic-cleanup"
	syntheticPurgeUser   = "system:synthetic-purger"
	cleanupConfig        = "synthetic-device-cleanup"
	missingParamsDenied  = "no params found for policy binding with `Deny` parameterNotFoundAction"
)

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
	t.Run("without a paramRef the cleanup username is not exempt", func(t *testing.T) {
		// The shipped binding has no paramRef, so params is null. A null
		// params must evaluate, not error: the denial carries the policy's
		// own message, and the Retired subtest below is admitted.
		cleanup := api.actor(syntheticCleanupUser, "system:masters")
		if err := cleanup.Delete(ctx, active); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), deletionDenied) {
			t.Fatalf("cleanup username deleting an Active Device without a paramRef: %v", err)
		}
		if !api.exists(active) {
			t.Fatal("Active Device removed without a paramRef")
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

// setParamRef does what a deploying repository does to turn on the cleanup
// identity: it points the binding at a ConfigMap in a fixed namespace with
// parameterNotFoundAction: Deny. The ConfigMap is created separately, so a
// test can also point the binding at one that does not exist.
func (api *liveAPI) setParamRef(name string) {
	api.t.Helper()
	ctx := context.Background()
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	if err := api.admin.Get(ctx, client.ObjectKey{Name: "fleet-device-deletion-protection"}, binding); err != nil {
		api.t.Fatal(err)
	}
	deny := admissionregistrationv1.DenyAction
	binding.Spec.ParamRef = &admissionregistrationv1.ParamRef{Name: name, Namespace: cleanupConfig, ParameterNotFoundAction: &deny}
	if err := api.admin.Update(ctx, binding); err != nil {
		api.t.Fatal(err)
	}
}

// missingParams reports whether err is the API server's denial for a binding
// whose parameter is missing under parameterNotFoundAction: Deny.
func missingParams(err error) bool {
	return apierrors.IsInvalid(err) && strings.Contains(err.Error(), missingParamsDenied)
}

func requireDeletionDenied(t *testing.T, err error, message string) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), message) {
		t.Fatalf("expected a Device deletion denial containing %q, got %v", message, err)
	}
}

// TestDeviceDeletionCleanupIdentity enables the cleanup identity as a
// deploying repository would and checks who it admits, that the other two
// exceptions still hold, that it lets a Milo-style purge drain `default`, and
// that a missing ConfigMap fails closed.
func TestDeviceDeletionCleanupIdentity(t *testing.T) {
	api := startAPI(t)
	ctx := context.Background()
	suffix := 0
	lifecycled := func(name, namespace string, lifecycle inventory.DeviceLifecycle) *inventory.Device {
		suffix++
		device := api.device(name, namespace, fmt.Sprintf("%s-serial-%d", name, suffix), fmt.Sprintf("eeeeeeee-eeee-4eee-8eee-%012d", suffix))
		switch lifecycle {
		case inventory.DeviceDecommissioning:
			api.setLifecycle(device, lifecycle)
		case inventory.DeviceRetired:
			api.retire(device)
		}
		return device
	}
	active := lifecycled("cleanup-active", "default", inventory.DeviceActive)
	decommissioning := lifecycled("cleanup-decommissioning", "default", inventory.DeviceDecommissioning)
	guarded := lifecycled("guarded-active", "default", inventory.DeviceActive)
	retired := lifecycled("cleanup-retired", "default", inventory.DeviceRetired)
	api.namespace("collection")
	collection := []*inventory.Device{
		lifecycled("collection-active", "collection", inventory.DeviceActive),
		lifecycled("collection-decommissioning", "collection", inventory.DeviceDecommissioning),
		lifecycled("collection-retired", "collection", inventory.DeviceRetired),
	}
	api.namespace("workloads")
	purged := []*inventory.Device{
		lifecycled("purge-default-active", "default", inventory.DeviceActive),
		lifecycled("purge-default-decommissioning", "default", inventory.DeviceDecommissioning),
		lifecycled("purge-default-retired", "default", inventory.DeviceRetired),
		lifecycled("purge-workloads-active", "workloads", inventory.DeviceActive),
		lifecycled("purge-workloads-decommissioning", "workloads", inventory.DeviceDecommissioning),
		lifecycled("purge-workloads-retired", "workloads", inventory.DeviceRetired),
	}
	teardown := api.namespace("teardown")
	teardownFirst := lifecycled("teardown-first", "teardown", inventory.DeviceActive)
	teardownSecond := lifecycled("teardown-second", "teardown", inventory.DeviceActive)

	api.installDeletionProtection(guarded)
	cleanup := api.actor(syntheticCleanupUser, "system:masters")
	purger := api.actor(syntheticPurgeUser, "system:masters")
	operator := api.actor("synthetic-operator", "system:masters")

	t.Run("without a paramRef the cleanup username is denied", func(t *testing.T) {
		requireDeletionDenied(t, cleanup.Delete(ctx, active), deletionDenied)
		if !api.exists(active) {
			t.Fatal("Active Device removed without a paramRef")
		}
	})

	// Entries are padded and separated by commas, newlines and CRLF, with
	// blank entries in between. Only exact, trimmed names match.
	api.namespace(cleanupConfig)
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cleanupConfig, Namespace: cleanupConfig}, Data: map[string]string{"cleanupUsernames": " " + syntheticCleanupUser + " ,\n\n , \r\n" + syntheticPurgeUser + "\r\n,"}}
	if err := api.admin.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	api.setParamRef(cleanupConfig)
	api.eventually("cleanup identity", func() bool {
		return cleanup.Delete(ctx, guarded, client.DryRunAll) == nil
	})

	t.Run("the listed username deletes Active and Decommissioning Devices", func(t *testing.T) {
		for _, device := range []*inventory.Device{active, decommissioning} {
			if err := cleanup.Delete(ctx, device); err != nil {
				t.Fatalf("cleanup identity deleting %s: %v", device.Name, err)
			}
			if api.exists(device) {
				t.Fatalf("%s survived the cleanup identity's delete", device.Name)
			}
		}
	})
	t.Run("the listed username deletes a mixed collection", func(t *testing.T) {
		if err := cleanup.DeleteAllOf(ctx, &inventory.Device{}, client.InNamespace("collection")); err != nil {
			t.Fatalf("cleanup identity deletecollection: %v", err)
		}
		for _, device := range collection {
			if api.exists(device) {
				t.Fatalf("%s survived the cleanup identity's deletecollection", device.Name)
			}
		}
	})
	t.Run("other usernames stay denied", func(t *testing.T) {
		callers := map[string]client.Client{
			"admin":                   api.admin,
			"unlisted":                operator,
			"prefix":                  api.actor("synthetic-clean", "system:masters"),
			"superset":                api.actor(syntheticCleanupUser+"-extra", "system:masters"),
			"embedded":                api.actor("x"+syntheticCleanupUser, "system:masters"),
			"case":                    api.actor("Synthetic-Cleanup", "system:masters"),
			"listed name as a group":  api.actor("synthetic-grouped", "system:masters", syntheticCleanupUser),
			"suffix of a listed name": api.actor("synthetic-purger", "system:masters"),
		}
		for name, caller := range callers {
			if err := caller.Delete(ctx, guarded); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), deletionDenied) {
				t.Fatalf("%s deleting an Active Device: %v", name, err)
			}
		}
		if !api.exists(guarded) {
			t.Fatal("Active Device removed by an unlisted caller")
		}
	})
	t.Run("the Retired exception still holds", func(t *testing.T) {
		if err := operator.Delete(ctx, retired); err != nil {
			t.Fatalf("unlisted caller deleting a Retired Device: %v", err)
		}
	})
	t.Run("the deleting-namespace exception still holds", func(t *testing.T) {
		requireDeletionDenied(t, operator.Delete(ctx, teardownFirst), deletionDenied)
		if err := api.admin.Delete(ctx, teardown); err != nil {
			t.Fatal(err)
		}
		api.eventually("namespace deletion mark", func() bool {
			return operator.Delete(ctx, teardownFirst, client.DryRunAll) == nil
		})
		if err := operator.Delete(ctx, teardownFirst); err != nil {
			t.Fatalf("unlisted caller deleting a Device in a deleting namespace: %v", err)
		}
		// teardownSecond stays behind for the fail-closed check below.
	})
	t.Run("a Milo-style purge drains default and other namespaces", func(t *testing.T) {
		// Milo v0.36.9 project purge issues DeleteCollection per namespace,
		// `default` included, before any namespace is marked for deletion.
		for _, namespace := range []string{"default", "workloads"} {
			if err := purger.DeleteAllOf(ctx, &inventory.Device{}, client.InNamespace(namespace)); err != nil {
				t.Fatalf("purge deletecollection in %s: %v", namespace, err)
			}
		}
		for _, device := range append(purged, guarded) {
			if api.exists(device) {
				t.Fatalf("%s survived the purge", device.Name)
			}
		}
	})

	// Fail closed: a paramRef to a ConfigMap that does not exist, under
	// parameterNotFoundAction: Deny, denies every Device DELETE, including a
	// Retired Device, the listed username and a deleting namespace. The API
	// server reports this binding configuration error as 422 Invalid, not as
	// the policy's 403 Forbidden.
	failClosedRetired := lifecycled("fail-closed-retired", "default", inventory.DeviceRetired)
	failClosedActive := lifecycled("fail-closed-active", "default", inventory.DeviceActive)
	api.setParamRef("absent-" + cleanupConfig)
	api.eventually("missing parameter denial", func() bool {
		return missingParams(operator.Delete(ctx, failClosedRetired, client.DryRunAll))
	})
	t.Run("a missing ConfigMap denies every delete", func(t *testing.T) {
		attempts := []struct {
			name    string
			attempt func() error
		}{
			{"Retired by an unlisted caller", func() error { return operator.Delete(ctx, failClosedRetired) }},
			{"Retired by the cleanup user", func() error { return cleanup.Delete(ctx, failClosedRetired) }},
			{"Active by the cleanup user", func() error { return cleanup.Delete(ctx, failClosedActive) }},
			{"Device in a deleting namespace", func() error { return api.admin.Delete(ctx, teardownSecond) }},
			{"purge deletecollection", func() error {
				return purger.DeleteAllOf(ctx, &inventory.Device{}, client.InNamespace("default"))
			}},
		}
		for _, test := range attempts {
			if err := test.attempt(); !missingParams(err) {
				t.Fatalf("%s: %v", test.name, err)
			}
		}
		for _, device := range []*inventory.Device{failClosedRetired, failClosedActive, teardownSecond} {
			if !api.exists(device) {
				t.Fatalf("%s removed while the parameter is missing", device.Name)
			}
		}
	})
}
