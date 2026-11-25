package custody

import (
	"fmt"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/cleaning"
)

// TestWipeAdmissionChecksThePlan admits a wipe only with the plan the shared
// planner makes now, and refuses one whose pinned drive is gone.
func TestWipeAdmissionChecksThePlan(t *testing.T) {
	e := startEnv(t)
	release := func(n int, allocation string) {
		name := fmt.Sprintf("server-example-%d", n)
		t.Helper()
		device := e.verified(name, uid(n), fmt.Sprintf("%064x", n))
		e.allocation(device, allocation, "Releasing")
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, allocation); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, allocation); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a plan that differs from the planner's is refused before anything is sent", func(t *testing.T) {
		release(781, "alloc-pm")
		device := e.read("server-example-781")
		wipe := &ops.DeviceWipe{}
		wipe.Name, wipe.Namespace = WipeName("alloc-pm", 1), "default"
		wipe.Spec = ops.DeviceWipeSpec{OperationEnvelope: envelope(device, releaseUser, allocated("alloc-pm")), Step: 1,
			Policy: ops.CleaningPolicyReference{Name: "tenant-reuse-v1", Generation: 1, Digest: policyDigest}, Assurance: ops.AssuranceClear, Drives: e.plan(device, "alloc-pm")}
		// Drop the check the pinned rule requires.
		wipe.Spec.Drives[0].Verify = nil
		e.create(wipe)
		fence := e.custody(device.Name).Spec.Fence
		e.settle(device.Name)
		e.requirePhase(wipe, ops.PhaseFailed, ReasonPlanMismatch)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a pinned drive discovery no longer reports is refused", func(t *testing.T) {
		release(782, "alloc-dm")
		device := e.read("server-example-782")
		device.Status.Discovery.Inventory.Systems[0].Storage.Drives = nil
		if err := e.admin.Status().Update(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		plan := e.planWith(device, "alloc-dm")
		if len(plan) != 1 || plan[0].State != ops.DriveMissing {
			t.Fatalf("plan = %+v", plan)
		}
		wipe := &ops.DeviceWipe{}
		wipe.Name, wipe.Namespace = WipeName("alloc-dm", 1), "default"
		wipe.Spec = ops.DeviceWipeSpec{OperationEnvelope: envelope(device, releaseUser, allocated("alloc-dm")), Step: 1,
			Policy: ops.CleaningPolicyReference{Name: "tenant-reuse-v1", Generation: 1, Digest: policyDigest}, Assurance: ops.AssuranceClear, Drives: plan}
		e.create(wipe)
		fence := e.custody(device.Name).Spec.Fence
		e.settle(device.Name)
		e.requirePhase(wipe, ops.PhaseFailed, ReasonDriveMissing)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("the planner's plan is admitted", func(t *testing.T) {
		release(783, "alloc-ok")
		device := e.read("server-example-783")
		wipe := e.wipe(device, "alloc-ok", 1)
		e.settle(device.Name)
		if active := e.custody(device.Name).Spec.ActiveOperation; active == nil || active.Name != wipe.Name {
			t.Fatalf("active = %+v, want the wipe admitted", active)
		}
	})
}

// read returns the Device as stored now.
func (e *env) read(name string) *inventory.Device {
	e.t.Helper()
	device := &inventory.Device{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: "default", Name: name}, device); err != nil {
		e.t.Fatal(err)
	}
	return device
}

// planWith plans the allocation's wipe from the Device's discovery as stored
// now, as admission does.
func (e *env) planWith(device *inventory.Device, allocation string) []ops.PlannedDrive {
	e.t.Helper()
	pinned := &metal.MachineAllocation{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: "default", Name: allocation}, pinned); err != nil {
		e.t.Fatal(err)
	}
	plan, err := cleaning.Plan(pinned.Spec.CleaningPolicy.Spec, pinned.Spec.Drives, cleaning.StorageOf(device), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return plan
}
