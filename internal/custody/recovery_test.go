package custody

import (
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

// holdOn runs a test staff operation to OutcomeUnknown and returns the hold.
func (e *env) holdOn(device *inventory.Device) ops.RecoveryHold {
	e.t.Helper()
	e.staffOp(device, "ok")
	e.settle(device.Name)
	if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
		e.t.Fatal(err)
	}
	e.settle(device.Name)
	hold := e.custody(device.Name).Spec.RecoveryHold
	if hold == nil {
		e.t.Fatal("no hold")
	}
	return *hold
}

func heldRef(hold ops.RecoveryHold) *ops.HeldOperationReference {
	return &ops.HeldOperationReference{Kind: hold.Kind, Name: hold.Name, Fence: hold.Fence}
}

func TestRecovery(t *testing.T) {
	e := startEnv(t)
	n := 200
	fresh := func() *inventory.Device {
		n++
		return e.verified(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
	}

	t.Run("a hold settles only after the site confirms the raised fence", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		hold := e.holdOn(device)
		e.clock.Advance(time.Minute)
		settle := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: heldRef(hold), Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now()), Summary: "discovery read the state", Data: map[string]string{"state": "On"}}})
		e.settle(device.Name)
		custody := e.custody(device.Name)
		if custody.Spec.ActiveOperation == nil || custody.Spec.ActiveOperation.Fence != hold.Fence+1 || custody.Spec.RecoveryHold == nil {
			t.Fatalf("recovery not admitted under the hold: %+v", custody.Spec)
		}
		// The site still runs the held worker at the old fence.
		e.sites.report(device.Name, hold.Fence)
		e.settle(device.Name)
		e.requirePhase(settle, ops.PhaseAdmitted, "")
		if e.custody(device.Name).Spec.RecoveryHold == nil {
			t.Fatal("hold settled before the site stopped the held worker")
		}
		e.sites.report(device.Name, hold.Fence+1)
		e.settle(device.Name)
		status := e.requirePhase(settle, ops.PhaseSucceeded, "")
		custody = e.custody(device.Name)
		if custody.Spec.RecoveryHold != nil || custody.Spec.Holder.Type != ops.HolderNone {
			t.Fatalf("after Settle: %+v", custody.Spec)
		}
		var evidence ops.RecoveryResultEvidence
		if err := jsonUnmarshal(status.Evidence.Raw, &evidence); err != nil || evidence.Decision != ops.DecisionSettle || evidence.ConfirmedFence != hold.Fence+1 {
			t.Fatalf("recovery evidence = %s", status.Evidence.Raw)
		}
	})

	t.Run("settle evidence is validated by the held kind", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		hold := e.holdOn(device)
		// Evidence observed before the hold began proves nothing.
		stale := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: heldRef(hold), Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now().Add(-time.Hour)), Summary: "old", Data: map[string]string{"state": "On"}}})
		e.settle(device.Name)
		e.requirePhase(stale, ops.PhaseFailed, "EvidenceNotCurrent")
		if e.custody(device.Name).Spec.RecoveryHold == nil {
			t.Fatal("hold cleared by stale evidence")
		}
	})

	t.Run("a recovery that does not name the hold is refused and changes nothing", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		hold := e.holdOn(device)
		fence := e.custody(device.Name).Spec.Fence
		wrong := heldRef(hold)
		wrong.Fence--
		refused := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: wrong, Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now().Add(time.Minute)), Summary: "s", Data: map[string]string{"state": "On"}}})
		e.settle(device.Name)
		e.requirePhase(refused, ops.PhaseFailed, ReasonHoldMismatch)
		e.requireUntouched(device.Name, fence)
		// A recovery with no hold to decide is refused too.
		free := fresh()
		none := e.recovery(free, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: wrong})
		e.settle(free.Name)
		e.requirePhase(none, ops.PhaseFailed, ReasonRecoveryHold)
		// A staff operation that cannot settle the hold is refused.
		blocked := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(blocked, ops.PhaseFailed, ReasonHolderMismatch)
	})

	t.Run("only Retire and Resolve skip current discovery and the shared-key check", func(t *testing.T) {
		e := e.sub(t)
		// The held kind accepts a Retry, so the kind's own checks pass and
		// the discovery check is what refuses it.
		staff := e.coord.Registry.Kind(testStaffKind)
		staff.Settles, staff.Retryable = []SettleRule{{Held: testStaffKind}, {Held: testStaffKind, RequiresRetry: true}}, true
		device := fresh()
		hold := e.holdOn(device)
		key := fmt.Sprintf("%064x", n)
		// The BMC stopped answering: discovery is old, and another live
		// Device now records the same key.
		e.discover(device, key, e.clock.Now().Add(-DefaultDiscoveryMaxAge-time.Minute))
		fence := e.custody(device.Name).Spec.Fence
		settle := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: heldRef(hold), Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now().Add(time.Minute)), Summary: "s", Data: map[string]string{"state": "On"}}})
		e.settle(device.Name)
		e.requirePhase(settle, ops.PhaseFailed, ReasonDeviceIdentityMismatch)
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.requirePhase(retry, ops.PhaseFailed, ReasonDeviceIdentityMismatch)
		e.requireUntouched(device.Name, fence)
		e.device(fmt.Sprintf("server-twin-%03d", n), uid(900+n), key)
		retire := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(retire, ops.PhaseSucceeded, "")
		if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderRetired {
			t.Fatalf("holder = %s", holder)
		}
	})

	t.Run("a recovery whose sites never confirm ends Failed and keeps the hold", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		hold := e.holdOn(device)
		e.clock.Advance(time.Minute)
		settle := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: heldRef(hold), Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now()), Summary: "s", Data: map[string]string{"state": "On"}}})
		e.settle(device.Name)
		e.clock.Advance(RecoveryDeadline + DefaultSkewMargin)
		e.settle(device.Name)
		e.requirePhase(settle, ops.PhaseFailed, ReasonSiteNotConfirmed)
		if got := e.custody(device.Name).Spec.RecoveryHold; got == nil || got.UID != hold.UID {
			t.Fatalf("hold = %+v", got)
		}
	})

	t.Run("a wipe after release settles an installation hold", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-i"); err != nil {
			t.Fatal(err)
		}
		install := e.installation(device, "alloc-i", "request-1")
		e.settle(device.Name)
		// The Machine is deleted during the installer phase.
		if err := e.result(device.Name, ops.PhaseDispatched, ""); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-i"); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		// Release waits for the installation's outcome or its deadline.
		e.clock.Advance(InstallationDeadline + DefaultSkewMargin)
		e.settle(device.Name)
		e.requirePhase(install, ops.PhaseOutcomeUnknown, ReasonDeadlineExceeded)
		custody := e.custody(device.Name)
		if custody.Spec.Holder.Type != ops.HolderRelease || custody.Spec.RecoveryHold == nil || custody.Spec.RecoveryHold.Kind != "DeviceInstallation" {
			t.Fatalf("after the install deadline: %+v", custody.Spec)
		}
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		wipe := e.wipe(device, "alloc-i", 1)
		e.settle(device.Name)
		e.requirePhase(wipe, ops.PhaseAdmitted, "")
		if active := e.custody(device.Name).Spec.ActiveOperation; active.Settles == nil || active.Settles.UID != string(install.UID) {
			t.Fatalf("wipe does not record what it settles: %+v", active)
		}
		if err := e.result(device.Name, ops.PhaseSucceeded, `{"wipedDisks":["disk-0"],"policyDigest":"`+policyDigest+`"}`); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(wipe, ops.PhaseSucceeded, "")
		if hold := e.custody(device.Name).Spec.RecoveryHold; hold != nil {
			t.Fatalf("hold survived the wipe: %+v", hold)
		}
		for _, gate := range ops.ReleaseGates {
			if err := e.coord.CloseGate(e.ctx, device.Name, "alloc-i", gate); err != nil {
				t.Fatal(err)
			}
		}
		e.settle(device.Name)
		if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderNone {
			t.Fatalf("holder = %s", holder)
		}
	})

	t.Run("a held wipe settles only with a wipe named for the Retry, and retries count on", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-w"); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-w"); err != nil {
			t.Fatal(err)
		}
		first := e.wipe(device, "alloc-w", 1)
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(first, ops.PhaseOutcomeUnknown, "")
		hold := *e.custody(device.Name).Spec.RecoveryHold
		// Other operations wait until the hold is settled; their names are
		// deterministic, so they could not be created again.
		later := e.wipe(device, "alloc-w", 2)
		e.settle(device.Name)
		e.requirePhase(later, ops.PhasePending, ReasonRecoveryHold)
		// The retried wipe takes the held wipe's name, and waits for the Retry.
		retried := e.namedWipe(device, "alloc-w", RetryName(hold, 1), 1)
		e.settle(device.Name)
		e.requirePhase(retried, ops.PhasePending, ReasonRecoveryHold)
		// Settle is not how a wipe hold ends.
		settle := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.requirePhase(settle, ops.PhaseFailed, ReasonNotSettleable)
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(retry, ops.PhaseSucceeded, "")
		// The retried wipe goes first, ahead of the older waiting one, and
		// uses the Retry.
		e.requirePhase(retried, ops.PhaseAdmitted, "")
		e.requirePhase(later, ops.PhasePending, ReasonRecoveryHold)
		custody := e.custody(device.Name)
		if got := custody.Spec.RecoveryHold; got == nil || got.RetryAuthorizedBy != nil || got.Retries != 1 || custody.Spec.ActiveOperation.Settles == nil || custody.Spec.ActiveOperation.Settles.UID != hold.UID {
			t.Fatalf("the retry was not used by the retried wipe: %+v", custody.Spec)
		}
		// The retried wipe ends unknown too: the next retry keeps the
		// original name and counts on.
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		second := *e.custody(device.Name).Spec.RecoveryHold
		if second.UID != string(retried.UID) || second.RetryName != hold.Name || second.Retries != 1 {
			t.Fatalf("second hold = %+v", second)
		}
		again := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(second)})
		e.settle(device.Name)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(again, ops.PhaseSucceeded, "")
		if name := RetryName(*e.custody(device.Name).Spec.RecoveryHold, 2); name != hold.Name+"-retry-2" {
			t.Fatalf("next retry name = %s", name)
		}
		retriedAgain := e.namedWipe(device, "alloc-w", hold.Name+"-retry-2", 1)
		e.settle(device.Name)
		e.requirePhase(retriedAgain, ops.PhaseAdmitted, "")
		if err := e.result(device.Name, ops.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		// The hold is settled, and the waiting wipe runs next.
		if got := e.custody(device.Name).Spec.RecoveryHold; got != nil {
			t.Fatalf("hold survived the retried wipe: %+v", got)
		}
		e.requirePhase(later, ops.PhaseAdmitted, "")
	})

	t.Run("pending replacement never cancels an operation that settles a hold", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-ps"); err != nil {
			t.Fatal(err)
		}
		e.providerOp(device, "alloc-ps", "held")
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		// Both settle the held provider operation, and the kind supersedes:
		// neither replaces the other while the hold exists.
		older := e.providerOp(device, "alloc-ps", "older")
		time.Sleep(1100 * time.Millisecond)
		newer := e.providerOp(device, "alloc-ps", "newer")
		e.settle(device.Name)
		e.requirePhase(older, ops.PhaseAdmitted, "")
		e.requirePhase(newer, ops.PhasePending, ReasonOperationInProgress)
	})

	t.Run("a second Retry for the same hold is refused", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-rr"); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-rr"); err != nil {
			t.Fatal(err)
		}
		e.wipe(device, "alloc-rr", 1)
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		hold := *e.custody(device.Name).Spec.RecoveryHold
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(retry, ops.PhaseSucceeded, "")
		if got := e.custody(device.Name).Spec.RecoveryHold; got == nil || got.RetryAuthorizedBy == nil || got.RetryAuthorizedBy.UID != string(retry.UID) || got.Retries != 1 {
			t.Fatalf("retry not recorded: %+v", got)
		}
		second := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.requirePhase(second, ops.PhaseFailed, ReasonRetryAuthorized)
	})

	t.Run("retiring from Release records disk disposal", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-r"); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-r"); err != nil {
			t.Fatal(err)
		}
		e.wipe(device, "alloc-r", 1)
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		hold := *e.custody(device.Name).Spec.RecoveryHold
		bare := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.requirePhase(bare, ops.PhaseFailed, ReasonDisposalRequired)
		retire := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(hold), DiskDisposal: &ops.DiskDisposal{Method: ops.DisposalDestroyed, Reference: "ticket-example-1"}})
		e.settle(device.Name)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(retire, ops.PhaseSucceeded, "")
		if custody := e.custody(device.Name); custody.Spec.Holder.Type != ops.HolderRetired || custody.Spec.RecoveryHold != nil {
			t.Fatalf("after Retire: %+v", custody.Spec)
		}
		// An allocated server cannot be retired.
		allocated := fresh()
		if err := e.coord.Claim(e.ctx, allocated.Name, "alloc-live"); err != nil {
			t.Fatal(err)
		}
		e.providerOp(allocated, "alloc-live", "a")
		e.settle(allocated.Name)
		if err := e.result(allocated.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(allocated.Name)
		held := *e.custody(allocated.Name).Spec.RecoveryHold
		refused := e.recovery(allocated, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(held), DiskDisposal: &ops.DiskDisposal{Method: ops.DisposalRemoved, Reference: "ticket-example-2"}})
		e.settle(allocated.Name)
		e.requirePhase(refused, ops.PhaseFailed, ReasonHolderMismatch)
	})

	t.Run("finished operations are kept 30 days, held ones until settled", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		done := e.staffOp(device, "ok")
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		hold := e.holdOn(device)
		e.clock.Advance(DefaultRetention - time.Hour)
		if _, err := e.coord.SweepRetention(e.ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(done), done); err != nil {
			t.Fatalf("deleted before 30 days: %v", err)
		}
		e.clock.Advance(2 * time.Hour)
		if _, err := e.coord.SweepRetention(e.ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(done), done); !apierrors.IsNotFound(err) {
			t.Fatalf("kept after 30 days: %v", err)
		}
		held := &DeviceTestStaffOperation{}
		if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: "default", Name: hold.Name}, held); err != nil {
			t.Fatalf("held operation deleted: %v", err)
		}
	})
}

func TestRebuild(t *testing.T) {
	e := startEnv(t)
	n := 300
	next := func() (string, string, string) {
		n++
		return fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n)
	}

	t.Run("custody deleted and restored is unallocatable until rebuilt above the site fence", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.verified(name, id, key)
		e.staffOp(device, "ok")
		e.settle(name)
		if err := e.result(name, ops.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(name)
		used := e.custody(name).Spec.Fence
		e.sites.report(name, used)
		// The harness admin removes the finalizer and deletes custody.
		custody := e.custody(name)
		custody.Finalizers = nil
		if err := e.admin.Update(e.ctx, custody); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Delete(e.ctx, custody); err != nil {
			t.Fatal(err)
		}
		e.sites.forget(name)
		e.settle(name)
		rebuilt := e.custody(name)
		if rebuilt.Spec.State != ops.CustodyUnverified {
			t.Fatalf("rebuilt = %+v", rebuilt.Spec)
		}
		if err := e.coord.Claim(e.ctx, name, "alloc-new"); err == nil {
			t.Fatal("rebuilt custody was allocatable before verification")
		}
		e.sites.report(name, used)
		e.settle(name)
		rebuilt = e.custody(name)
		if rebuilt.Spec.State != ops.CustodyVerified || rebuilt.Spec.Fence <= used {
			t.Fatalf("verified fence %d, site stored %d", rebuilt.Spec.Fence, used)
		}
		if err := e.coord.Claim(e.ctx, name, "alloc-new"); err != nil {
			t.Fatal(err)
		}
	})

	// rebuildFrom deletes custody and rebuilds it with the site reporting.
	rebuildFrom := func(e *env, name string, siteFence int64) *ops.DeviceCustody {
		e.t.Helper()
		custody := &ops.DeviceCustody{}
		if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: "default", Name: name}, custody); err == nil {
			custody.Finalizers = nil
			if err := e.admin.Update(e.ctx, custody); err != nil {
				e.t.Fatal(err)
			}
			if err := e.admin.Delete(e.ctx, custody); err != nil {
				e.t.Fatal(err)
			}
		}
		e.sites.report(name, siteFence)
		e.settle(name)
		return e.custody(name)
	}

	t.Run("from inventory: a retired Device rebuilds Retired", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.device(name, id, key)
		for _, lifecycle := range []inventory.DeviceLifecycle{inventory.DeviceDecommissioning, inventory.DeviceRetired} {
			if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
				t.Fatal(err)
			}
			device.Spec.Lifecycle = lifecycle
			if err := e.admin.Update(e.ctx, device); err != nil {
				t.Fatal(err)
			}
		}
		if custody := rebuildFrom(e, name, 3); custody.Spec.Holder.Type != ops.HolderRetired || custody.Spec.State != ops.CustodyVerified || custody.Spec.Fence != 4 {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
	})

	t.Run("from allocations: a live allocation rebuilds Allocation, a releasing one Release with every gate open", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.device(name, id, key)
		e.allocation(device, "alloc-live-"+name, "Active")
		if custody := rebuildFrom(e, name, 0); custody.Spec.Holder.Type != ops.HolderAllocation || custody.Spec.Holder.AllocationRef.Name != "alloc-live-"+name || custody.Spec.State != ops.CustodyVerified {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
		name, id, key = next()
		device = e.device(name, id, key)
		e.allocation(device, "alloc-old-"+name, "Released")
		e.allocation(device, "alloc-rel-"+name, "Releasing")
		custody := rebuildFrom(e, name, 0)
		if custody.Spec.Holder.Type != ops.HolderRelease || !slices.Equal(custody.Spec.Holder.OpenGates, ops.ReleaseGates) || custody.Spec.State != ops.CustodyVerified {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
	})

	t.Run("from operations: an unfinished operation, even Pending, is held", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.device(name, id, key)
		operation := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: name + "-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, service("restore")), Input: "a"}}
		e.create(operation)
		operation.Status = ops.OperationStatus{Phase: ops.PhasePending, Fence: 9}
		if err := e.admin.Status().Update(e.ctx, operation); err != nil {
			t.Fatal(err)
		}
		custody := rebuildFrom(e, name, 4)
		if hold := custody.Spec.RecoveryHold; hold == nil || hold.UID != string(operation.UID) || hold.Reason != ReasonRebuilt || custody.Spec.Holder.Type != ops.HolderService || custody.Spec.Fence != 10 {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
	})

	t.Run("from recoveries: a succeeded Retire rebuilds Retired", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.device(name, id, key)
		recovery := &ops.DeviceRecovery{ObjectMeta: metav1.ObjectMeta{GenerateName: name + "-", Namespace: "default"}, Spec: ops.DeviceRecoverySpec{OperationEnvelope: envelope(device, staffUser, service("retired")), Decision: ops.DecisionRetire, Hold: &ops.HeldOperationReference{Kind: testStaffKind, Name: "x", Fence: 1}, Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now()), Summary: "s"}}}
		e.create(recovery)
		finished := metav1.NewTime(e.clock.Now())
		recovery.Status = ops.OperationStatus{Phase: ops.PhaseSucceeded, Fence: 2, FinishedAt: &finished}
		if err := e.admin.Status().Update(e.ctx, recovery); err != nil {
			t.Fatal(err)
		}
		if custody := rebuildFrom(e, name, 0); custody.Spec.Holder.Type != ops.HolderRetired || custody.Spec.Fence != 3 {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
	})

	conflict := func(t *testing.T, e *env, name, want string) {
		t.Helper()
		custody := rebuildFrom(e, name, 0)
		if custody.Spec.State != ops.CustodyUnverified || !slices.Contains(custody.Spec.Conflicts, want) {
			t.Fatalf("rebuilt = %+v, want conflict %s", custody.Spec, want)
		}
		if err := e.coord.Claim(e.ctx, name, "alloc-any"); err == nil {
			t.Fatal("conflicted custody was claimed")
		}
	}

	t.Run("conflict: two live allocations", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.device(name, id, key)
		e.allocation(device, "alloc-one-"+name, "Active")
		e.allocation(device, "alloc-two-"+name, "Active")
		conflict(t, e, name, ConflictMultipleAllocations)
	})

	t.Run("conflict: an allocation and a staff operation disagree", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.device(name, id, key)
		e.allocation(device, "alloc-"+name, "Active")
		e.create(&DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: name + "-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, service("x")), Input: "a"}})
		conflict(t, e, name, ConflictHolderSources)
	})

	t.Run("conflict: two unfinished operations", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.device(name, id, key)
		for range 2 {
			e.create(&DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: name + "-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, service("x")), Input: "a"}})
		}
		conflict(t, e, name, ConflictMultipleUnfinished)
	})

	t.Run("conflict: the Device UID differs from deviceRef.uid, then Resolve", func(t *testing.T) {
		e := e.sub(t)
		name, id, key := next()
		device := e.verified(name, id, key)
		// The Device is replaced under the same name while custody remains.
		if err := e.admin.Delete(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		replacement := e.device(name, uid(n+500), key)
		e.settle(name)
		custody := e.custody(name)
		if custody.Spec.State != ops.CustodyUnverified || !slices.Contains(custody.Spec.Conflicts, ConflictDeviceUID) {
			t.Fatalf("custody = %+v", custody.Spec)
		}
		// Only a Resolve recovery is admitted.
		staff := e.staffOp(replacement, "ok")
		e.settle(name)
		e.requirePhase(staff, ops.PhaseFailed, ReasonCustodyUnverified)
		resolve := e.recovery(replacement, ops.DeviceRecoverySpec{Decision: ops.DecisionResolve, Resolve: &ops.ResolveInput{Holder: ops.HolderNone}})
		e.settle(name)
		e.sites.report(name, e.custody(name).Spec.Fence)
		e.settle(name)
		e.requirePhase(resolve, ops.PhaseSucceeded, "")
		custody = e.custody(name)
		if custody.Spec.State != ops.CustodyVerified || custody.Spec.Holder.Type != ops.HolderNone || custody.Spec.DeviceRef.UID != string(replacement.UID) || len(custody.Spec.Conflicts) != 0 {
			t.Fatalf("after Resolve: %+v", custody.Spec)
		}
	})
}
