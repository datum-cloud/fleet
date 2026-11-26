package custody

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestImportedDeviceGetsUnverifiedCustodyAndSendsNothing(t *testing.T) {
	e := startEnv(t)
	device := e.device("server-example-001", uid(1), testKeyA)
	e.settle(device.Name)
	custody := e.custody(device.Name)
	if custody.Spec.State != ops.CustodyUnverified || custody.Spec.Holder.Type != ops.HolderNone || custody.Spec.ActiveOperation != nil || custody.Labels[ops.LabelDeviceUID] != string(device.UID) {
		t.Fatalf("new custody = %+v", custody.Spec)
	}
	if len(custody.Finalizers) != 1 || custody.Finalizers[0] != ops.CustodyFinalizer {
		t.Fatalf("finalizers = %v", custody.Finalizers)
	}
	if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-example"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Unverified custody was claimed: %v", err)
	}
	conditions := ProjectCapacity(CapacityInputs{Custody: &custody.Spec, AllocationPolicyEnabled: true, DiscoveryCurrent: true})
	if conditions[2].Status != metav1.ConditionFalse {
		t.Fatal("imported Device became Available")
	}
	// No configured site: nothing can fence the Device yet.
	e.sites.mu.Lock()
	e.sites.serving[device.Name] = nil
	e.sites.mu.Unlock()
	e.sites.report(device.Name, 7)
	e.settle(device.Name)
	if state := e.custody(device.Name).Spec.State; state != ops.CustodyUnverified {
		t.Fatalf("custody verified with no serving site: %s", state)
	}
	e.sites.mu.Lock()
	delete(e.sites.serving, device.Name)
	e.sites.mu.Unlock()
	e.settle(device.Name)
	custody = e.custody(device.Name)
	if custody.Spec.State != ops.CustodyVerified || custody.Spec.Fence != 8 {
		t.Fatalf("verified custody = %+v", custody.Spec)
	}
}

func TestLifecycle(t *testing.T) {
	e := startEnv(t)
	n := 100
	fresh := func() *inventory.Device {
		n++
		return e.verified(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
	}

	t.Run("holder transitions None Allocation Release None", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-a"); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-b"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a second allocation claimed the server: %v", err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-b"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("another allocation released the server: %v", err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-a"); err != nil {
			t.Fatal(err)
		}
		custody := e.custody(device.Name)
		if custody.Spec.Holder.Type != ops.HolderRelease || !slices.Equal(custody.Spec.Holder.OpenGates, ops.ReleaseGates) {
			t.Fatalf("release holder = %+v", custody.Spec.Holder)
		}
		if err := e.coord.CloseGate(e.ctx, device.Name, "alloc-a", "Invented"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("an unknown gate closed: %v", err)
		}
		for index, gate := range ops.ReleaseGates {
			e.settle(device.Name)
			if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderRelease {
				t.Fatalf("left Release with %d gates open: %s", len(ops.ReleaseGates)-index, holder)
			}
			if err := e.coord.CloseGate(e.ctx, device.Name, "alloc-a", gate); err != nil {
				t.Fatal(err)
			}
		}
		e.settle(device.Name)
		if holder := e.custody(device.Name).Spec.Holder; holder.Type != ops.HolderNone || holder.AllocationRef != nil {
			t.Fatalf("holder after release = %+v", holder)
		}
	})

	t.Run("holder transitions None Service None and Service Retired", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		operation := e.staffOp(device, "ok")
		e.settle(device.Name)
		if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderService {
			t.Fatalf("holder = %s", holder)
		}
		if err := e.result(device.Name, ops.PhaseSucceeded, `{"before":"Off","after":"On"}`); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		custody := e.custody(device.Name)
		status := e.requirePhase(operation, ops.PhaseSucceeded, "")
		if custody.Spec.Holder.Type != ops.HolderNone || custody.Spec.LastResult == nil || evidenceOf(t, custody.Spec.LastResult.Evidence.Raw) != (testEvidence{Before: "Off", After: "On"}) || status.Evidence == nil || evidenceOf(t, status.Evidence.Raw) != (testEvidence{Before: "Off", After: "On"}) {
			t.Fatalf("after success: custody %+v status %+v", custody.Spec, status)
		}

		held := fresh()
		e.staffOp(held, "ok")
		e.settle(held.Name)
		if err := e.result(held.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(held.Name)
		hold := e.custody(held.Name).Spec.RecoveryHold
		retire := e.recovery(held, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: &ops.HeldOperationReference{Kind: hold.Kind, Name: hold.Name, Fence: hold.Fence}})
		e.settle(held.Name)
		e.sites.report(held.Name, e.custody(held.Name).Spec.Fence)
		e.settle(held.Name)
		e.requirePhase(retire, ops.PhaseSucceeded, "")
		if custody := e.custody(held.Name); custody.Spec.Holder.Type != ops.HolderRetired || custody.Spec.RecoveryHold != nil {
			t.Fatalf("after Retire: %+v", custody.Spec)
		}
	})

	t.Run("inventory retirement of a free Device, finalizer removal and deletion", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		for _, lifecycle := range []inventory.DeviceLifecycle{inventory.DeviceDecommissioning, inventory.DeviceRetired} {
			if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
				t.Fatal(err)
			}
			device.Spec.Lifecycle = lifecycle
			if err := e.admin.Update(e.ctx, device); err != nil {
				t.Fatal(err)
			}
		}
		e.settle(device.Name)
		custody := e.custody(device.Name)
		if custody.Spec.Holder.Type != ops.HolderRetired {
			t.Fatalf("holder = %s", custody.Spec.Holder.Type)
		}
		// Retired is terminal.
		custody.Spec.Holder = ops.Holder{Type: ops.HolderNone}
		if err := e.admin.Update(e.ctx, custody); !apierrors.IsInvalid(err) {
			t.Fatalf("custody left Retired: %v", err)
		}
		if err := e.admin.Delete(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(custody), &ops.DeviceCustody{}); !apierrors.IsNotFound(err) {
			t.Fatalf("custody survived its retired Device: %v", err)
		}
	})

	t.Run("custody being deleted keeps its finalizer until Retired", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		custody := e.custody(device.Name)
		if err := e.admin.Delete(e.ctx, custody); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		if custody = e.custody(device.Name); custody.DeletionTimestamp.IsZero() || !slices.Contains(custody.Finalizers, ops.CustodyFinalizer) {
			t.Fatalf("finalizer removed from a live holder: %+v", custody.ObjectMeta)
		}
	})

	t.Run("pending operations are admitted oldest first", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-q"); err != nil {
			t.Fatal(err)
		}
		e.providerOp(device, "alloc-q", "first")
		e.settle(device.Name)
		// Creation times have one-second resolution.
		time.Sleep(1100 * time.Millisecond)
		second := e.installation(device, "alloc-q", "request-second")
		time.Sleep(1100 * time.Millisecond)
		third := e.installation(device, "alloc-q", "request-third")
		e.settle(device.Name)
		e.requirePhase(second, ops.PhasePending, ReasonOperationInProgress)
		e.requirePhase(third, ops.PhasePending, ReasonOperationInProgress)
		if err := e.result(device.Name, ops.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(second, ops.PhaseAdmitted, "")
		e.requirePhase(third, ops.PhasePending, ReasonOperationInProgress)
	})

	t.Run("a newer pending operation supersedes an older one for the same authority", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-s"); err != nil {
			t.Fatal(err)
		}
		running := e.installation(device, "alloc-s", "request-running")
		e.settle(device.Name)
		e.requirePhase(running, ops.PhaseAdmitted, "")
		older := e.providerOp(device, "alloc-s", "older")
		time.Sleep(1100 * time.Millisecond)
		newer := e.providerOp(device, "alloc-s", "newer")
		e.settle(device.Name)
		e.requirePhase(older, ops.PhaseCancelled, ReasonSuperseded)
		e.requirePhase(newer, ops.PhasePending, ReasonOperationInProgress)
	})

	t.Run("a holder change cancels pending operations that no longer match", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-c"); err != nil {
			t.Fatal(err)
		}
		running := e.installation(device, "alloc-c", "request-a")
		e.settle(device.Name)
		pending := e.installation(device, "alloc-c", "request-b")
		e.settle(device.Name)
		e.requirePhase(pending, ops.PhasePending, ReasonOperationInProgress)
		// The Machine is deleted while the installation runs.
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-c"); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(pending, ops.PhaseCancelled, ReasonHolderMismatch)
		// The running installation still reports under its fence.
		if err := e.result(device.Name, ops.PhaseSucceeded, `{"steps":[{"name":"Installer","completedAt":"2026-01-02T03:04:05Z"}]}`); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(running, ops.PhaseSucceeded, "")
		if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderRelease {
			t.Fatalf("holder = %s", holder)
		}
	})

	t.Run("a result with a stale fence is rejected and recorded", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		e.staffOp(device, "ok")
		e.settle(device.Name)
		custody := e.custody(device.Name)
		active := custody.Spec.ActiveOperation
		stale := SiteResult{Site: testSite, Device: device.Name, Operation: active.OperationReference, Fence: active.Fence - 1, Phase: ops.PhaseSucceeded}
		if err := e.coord.AcceptResult(e.ctx, stale); !errors.Is(err, ErrStaleFence) {
			t.Fatalf("stale result: %v", err)
		}
		other := stale
		other.Fence = active.Fence
		other.Operation.Name = "another-operation"
		if err := e.coord.AcceptResult(e.ctx, other); !errors.Is(err, ErrStaleFence) {
			t.Fatalf("result for another operation: %v", err)
		}
		after := e.custody(device.Name)
		if after.Spec.ActiveOperation == nil || after.Spec.Holder.Type != custody.Spec.Holder.Type || after.Spec.RejectedResults == nil || after.Spec.RejectedResults.Count != 2 || after.Spec.RejectedResults.Last.Fence != active.Fence || after.Spec.RejectedResults.Last.Site != testSite {
			t.Fatalf("after stale results: %+v", after.Spec)
		}
	})

	t.Run("results move phases forward only and check evidence", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		operation := e.staffOp(device, "ok")
		e.settle(device.Name)
		for _, phase := range []ops.Phase{ops.PhaseDispatched, ops.PhaseVerifying, ops.PhaseDispatched} {
			if err := e.result(device.Name, phase, ""); err != nil {
				t.Fatal(err)
			}
		}
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseVerifying, "")
		if err := e.result(device.Name, ops.PhaseSucceeded, `{"before":"On","password":"secret"}`); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		// Evidence outside the kind's schema proves nothing.
		e.requirePhase(operation, ops.PhaseOutcomeUnknown, "InvalidEvidence")
		if hold := e.custody(device.Name).Spec.RecoveryHold; hold == nil || hold.Name != operation.Name {
			t.Fatalf("hold = %+v", hold)
		}
	})

	t.Run("a passed deadline is OutcomeUnknown and a hold, never Failed or retried", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		operation := e.staffOp(device, "ok")
		e.settle(device.Name)
		active := *e.custody(device.Name).Spec.ActiveOperation
		e.clock.Advance(testDeadline + DefaultSkewMargin - time.Second)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseAdmitted, "")
		e.clock.Advance(time.Second)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseOutcomeUnknown, ReasonDeadlineExceeded)
		custody := e.custody(device.Name)
		hold := custody.Spec.RecoveryHold
		if custody.Spec.ActiveOperation != nil || hold == nil || hold.Name != operation.Name || hold.Fence != active.Fence || custody.Spec.Holder.Type != ops.HolderService || custody.Spec.Fence != active.Fence {
			t.Fatalf("after the deadline: %+v", custody.Spec)
		}
		// A late result for the held operation proves nothing.
		late := SiteResult{Site: testSite, Device: device.Name, Operation: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseSucceeded}
		if err := e.coord.AcceptResult(e.ctx, late); !errors.Is(err, ErrStaleFence) {
			t.Fatalf("late result: %v", err)
		}
	})
}

func evidenceOf(t *testing.T, raw []byte) testEvidence {
	t.Helper()
	var evidence testEvidence
	if err := jsonUnmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence
}
