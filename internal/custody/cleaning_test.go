package custody

import (
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	t.Run("a pinned drive discovery no longer reports holds the server until staff dispose of it", func(t *testing.T) {
		release(782, "alloc-dm")
		device := e.read("server-example-782")
		saved := device.Status.Discovery.Inventory.Systems[0].Storage.Drives
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
		// Admitted and held in one update: the fence rose, nothing is
		// active, so nothing was delivered.
		e.requirePhase(wipe, ops.PhaseOutcomeUnknown, ReasonDriveMissing)
		state := e.custody(device.Name)
		if state.Spec.ActiveOperation != nil || state.Spec.Fence <= fence || state.Spec.RecoveryHold == nil || state.Spec.RecoveryHold.Name != wipe.Name || state.Spec.RecoveryHold.Reason != ReasonDriveMissing {
			t.Fatalf("custody = %+v", state.Spec)
		}
		hold := *state.Spec.RecoveryHold
		// A retried wipe would plan the same drive.
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.requirePhase(retry, ops.PhaseFailed, ReasonNotRetryable)
		// Only a drive the held wipe could not clean can be disposed of.
		wrong := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionDisposeDrive, Hold: heldRef(hold), Drive: &ops.DriveDisposal{Name: "disk-7", Method: ops.DisposalDestroyed, Reference: "ticket-1"}})
		e.settle(device.Name)
		e.requirePhase(wrong, ops.PhaseFailed, ReasonDisposalInvalid)
		dispose := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionDisposeDrive, Hold: heldRef(hold), Drive: &ops.DriveDisposal{Name: "disk-0", Method: ops.DisposalRemoved, Reference: "ticket-2"}})
		e.settle(device.Name)
		// Like every recovery, it applies once the site reports its fence.
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(dispose, ops.PhaseSucceeded, "")
		state = e.custody(device.Name)
		disposed := state.Spec.Holder.DisposedDrives
		if state.Spec.RecoveryHold != nil || state.Spec.Holder.Type != ops.HolderRelease || len(disposed) != 1 || disposed[0].Name != "disk-0" || disposed[0].SerialNumber != saved[0].SerialNumber || disposed[0].Recovery != dispose.Name {
			t.Fatalf("custody after DisposeDrive = %+v", state.Spec)
		}
		// The disposed drive is not planned again.
		next := e.planWith(e.read(device.Name), "alloc-dm")
		if len(next) != 0 {
			t.Fatalf("the next plan must omit the disposed drive: %+v", next)
		}
	})

	t.Run("a held wipe recreated under the same name cannot be disposed of", func(t *testing.T) {
		release(785, "alloc-dr")
		device := e.read("server-example-785")
		device.Status.Discovery.Inventory.Systems[0].Storage.Drives = nil
		if err := e.admin.Status().Update(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		plan := e.planWith(device, "alloc-dr")
		newWipe := func() *ops.DeviceWipe {
			wipe := &ops.DeviceWipe{}
			wipe.Name, wipe.Namespace = WipeName("alloc-dr", 1), "default"
			wipe.Spec = ops.DeviceWipeSpec{OperationEnvelope: envelope(device, releaseUser, allocated("alloc-dr")), Step: 1,
				Policy: ops.CleaningPolicyReference{Name: "tenant-reuse-v1", Generation: 1, Digest: policyDigest}, Assurance: ops.AssuranceClear, Drives: plan}
			return wipe
		}
		held := newWipe()
		e.create(held)
		e.settle(device.Name)
		e.requirePhase(held, ops.PhaseOutcomeUnknown, ReasonDriveMissing)
		hold := *e.custody(device.Name).Spec.RecoveryHold
		// The held wipe is deleted and a new one takes its name. The new one
		// was never held, so its plan must not authorize a disposal.
		if err := e.admin.Delete(e.ctx, held); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(held), &ops.DeviceWipe{}); !apierrors.IsNotFound(err) {
			t.Fatalf("the held wipe must be gone: %v", err)
		}
		e.create(newWipe())
		dispose := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionDisposeDrive, Hold: heldRef(hold), Drive: &ops.DriveDisposal{Name: "disk-0", Method: ops.DisposalRemoved, Reference: "ticket-4"}})
		e.settle(device.Name)
		e.requirePhase(dispose, ops.PhaseFailed, ReasonDisposalInvalid)
		if state := e.custody(device.Name); state.Spec.RecoveryHold == nil || len(state.Spec.Holder.DisposedDrives) != 0 {
			t.Fatalf("custody = %+v", state.Spec)
		}
	})

	t.Run("an uncovered drive still in the server cannot be disposed of", func(t *testing.T) {
		release(784, "alloc-du")
		device := e.read("server-example-784")
		device.Status.Discovery.Inventory.Systems[0].Storage.Drives[0].Firmware = "UNQUALIFIED"
		device.Status.Discovery.Inventory.Systems[0].Storage.Drives[0].Protocol = "SAS"
		if err := e.admin.Status().Update(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		plan := e.planWith(device, "alloc-du")
		if len(plan) != 1 || plan[0].State != ops.DriveUncovered {
			t.Fatalf("plan = %+v", plan)
		}
		wipe := &ops.DeviceWipe{}
		wipe.Name, wipe.Namespace = WipeName("alloc-du", 1), "default"
		wipe.Spec = ops.DeviceWipeSpec{OperationEnvelope: envelope(device, releaseUser, allocated("alloc-du")), Step: 1,
			Policy: ops.CleaningPolicyReference{Name: "tenant-reuse-v1", Generation: 1, Digest: policyDigest}, Assurance: ops.AssuranceClear, Drives: plan}
		e.create(wipe)
		e.settle(device.Name)
		e.requirePhase(wipe, ops.PhaseOutcomeUnknown, ReasonDriveUncovered)
		hold := *e.custody(device.Name).Spec.RecoveryHold
		dispose := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionDisposeDrive, Hold: heldRef(hold), Drive: &ops.DriveDisposal{Name: "disk-0", Method: ops.DisposalDestroyed, Reference: "ticket-3"}})
		e.settle(device.Name)
		e.requirePhase(dispose, ops.PhaseFailed, ReasonDisposalInvalid)
		if e.custody(device.Name).Spec.RecoveryHold == nil {
			t.Fatal("the hold must stay while the uncovered drive is in the server")
		}
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
	plan, err := cleaning.Plan(pinned.Spec.CleaningPolicy.Spec, pinned.Spec.Drives, cleaning.StorageOf(device), DisposedDriveNames(e.custody(device.Name)))
	if err != nil {
		e.t.Fatal(err)
	}
	return plan
}
