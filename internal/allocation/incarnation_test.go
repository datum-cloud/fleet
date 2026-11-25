package allocation

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// No spec holds an object UID, so these tests pin each same-name
// recreation guard: an object recreated under the same name never inherits
// what was recorded for the incarnation before it.

// TestAnAllocationForAnotherMachineIncarnationIsAConflict covers a Machine
// and a consumer project recreated under the same names: the allocation's
// labels name the incarnation it was made for.
func TestAnAllocationForAnotherMachineIncarnationIsAConflict(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	for label, name := range map[string]string{fleetv1.LabelMachineUID: "Machine", fleetv1.LabelConsumerProjectUID: "consumer project"} {
		stored := &fleetv1.MachineAllocation{}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), stored); err != nil {
			t.Fatal(err)
		}
		original := stored.Labels[label]
		stored.Labels[label] = uid(999)
		if err := e.admin.Update(e.ctx, stored); err != nil {
			t.Fatal(err)
		}
		if _, err := e.allocator.Get(e.ctx, machine(1)); !errors.Is(err, ErrConflict) {
			t.Fatalf("an allocation for another %s incarnation: %v", name, err)
		}
		stored.Labels[label] = original
		if err := e.admin.Update(e.ctx, stored); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := e.allocator.Get(e.ctx, machine(1)); err != nil || found == nil {
		t.Fatalf("the allocation's own Machine: %v, %v", found, err)
	}
}

// TestAnOperationForAnotherDeviceIncarnationIsAConflict covers a Device
// recreated under the same name: an existing operation with the same name
// and spec, recorded for the earlier Device, is never taken for a new one.
func TestAnOperationForAnotherDeviceIncarnationIsAConflict(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	installation := e.installation(1, "first", "")
	operation, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation})
	if err != nil {
		t.Fatal(err)
	}
	stored := &ops.DeviceInstallation{}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), stored); err != nil {
		t.Fatal(err)
	}
	stored.Labels[ops.LabelDeviceUID] = uid(998)
	if err := e.admin.Update(e.ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an operation recorded for another Device incarnation: %v", err)
	}
	if stored.Spec.DeviceRef.Name != device.Name {
		t.Fatalf("operation names %s", stored.Spec.DeviceRef.Name)
	}
}

// TestCustodyForAnotherDeviceIncarnationIsNotFree covers a Device recreated
// under the same name: custody recorded for the earlier Device is not free
// capacity for the new one.
func TestCustodyForAnotherDeviceIncarnationIsNotFree(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	state := e.custody(device.Name)
	if !Free(state, device) {
		t.Fatalf("custody of the Device's own incarnation is not free: %+v", state.Spec)
	}
	recreated := *device
	recreated.UID = types.UID(uid(997))
	if Free(state, &recreated) {
		t.Fatal("custody of an earlier Device incarnation is free for a recreated Device")
	}
}
