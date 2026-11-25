package custody

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// dropCustody removes the finalizer as the harness admin and deletes
// custody, as a restore that lost it would.
func (e *env) dropCustody(name string) {
	e.t.Helper()
	custody := e.custody(name)
	custody.Finalizers = nil
	if err := e.admin.Update(e.ctx, custody); err != nil {
		e.t.Fatal(err)
	}
	if err := e.admin.Delete(e.ctx, custody); err != nil {
		e.t.Fatal(err)
	}
}

// TestRebuildKeepsUnprovenOutcomes covers rebuild with the built-in
// provider and release kinds: an unproven outcome stays held and its
// operation is never admitted again.
func TestRebuildKeepsUnprovenOutcomes(t *testing.T) {
	e := startEnv(t)
	n := 700
	next := func() *inventory.Device {
		n++
		return e.device(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
	}
	verify := func(e *env, device *inventory.Device, fence int64) *ops.DeviceCustody {
		e.t.Helper()
		e.sites.report(device.Name, fence)
		e.settle(device.Name)
		return e.custody(device.Name)
	}

	t.Run("an installation that ended OutcomeUnknown is held again after custody is lost", func(t *testing.T) {
		e := e.sub(t)
		device := next()
		e.allocation(device, "alloc-u-"+device.Name, "Active")
		verify(e, device, 0)
		install := e.installation(device, "alloc-u-"+device.Name, "request-1")
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		fence := e.custody(device.Name).Spec.Fence
		e.dropCustody(device.Name)
		custody := verify(e, device, fence)
		if custody.Spec.State != ops.CustodyVerified || custody.Spec.Holder.Type != ops.HolderAllocation || custody.Spec.RecoveryHold == nil || custody.Spec.RecoveryHold.Name != install.Name || custody.Spec.ActiveOperation != nil {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-other"); err == nil {
			t.Fatal("a held server was claimed")
		}
	})

	t.Run("an unfinished installation is held, ended unknown, and never admitted again", func(t *testing.T) {
		e := e.sub(t)
		device := next()
		e.allocation(device, "alloc-p-"+device.Name, "Active")
		install := e.installation(device, "alloc-p-"+device.Name, "request-1")
		// It may have run before custody was lost: its status says Admitted.
		deadline := metav1.NewTime(e.clock.Now().Add(InstallationDeadline))
		recorded := ops.DeviceSnapshot{BMCEndpoint: device.Spec.BMC.Endpoint, SystemUUID: device.Spec.Identity.SystemUUID, BMCPublicKeySHA256: fmt.Sprintf("%064x", n), Extra: map[string]string{"resetTypes": "On"}}
		install.Status = ops.OperationStatus{Admission: admitted(4), Phase: ops.PhaseAdmitted, Fence: 4, Deadline: &deadline, Snapshot: &recorded}
		if err := e.admin.Status().Update(e.ctx, install); err != nil {
			t.Fatal(err)
		}
		custody := verify(e, device, 4)
		e.settle(device.Name)
		custody = e.custody(device.Name)
		if hold := custody.Spec.RecoveryHold; hold == nil || hold.Name != install.Name || custody.Spec.ActiveOperation != nil || custody.Spec.Fence <= 4 {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
		// The rebuilt hold keeps the deadline and snapshot the status recorded.
		if hold := custody.Spec.RecoveryHold; hold.Deadline == nil || !hold.Deadline.Equal(&deadline) || hold.Snapshot == nil || !reflect.DeepEqual(*hold.Snapshot, recorded) {
			t.Fatalf("rebuilt hold = %+v", hold)
		}
		e.requirePhase(install, ops.PhaseOutcomeUnknown, ReasonRebuilt)
	})

	t.Run("a pending wipe found during a releasing rebuild is held, not run", func(t *testing.T) {
		e := e.sub(t)
		device := next()
		e.allocation(device, "alloc-r-"+device.Name, "Releasing")
		wipe := e.wipe(device, "alloc-r-"+device.Name, 1)
		// Its admission was recorded, then custody was lost before the rest
		// of its status was copied.
		wipe.Status = ops.OperationStatus{Admission: admitted(1), Phase: ops.PhasePending, Reason: ReasonOperationInProgress}
		if err := e.admin.Status().Update(e.ctx, wipe); err != nil {
			t.Fatal(err)
		}
		custody := verify(e, device, 0)
		if custody.Spec.Holder.Type != ops.HolderRelease || custody.Spec.RecoveryHold == nil || custody.Spec.RecoveryHold.Name != wipe.Name || custody.Spec.ActiveOperation != nil {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
		// A Pending status recorded no deadline or snapshot, so the hold has
		// none.
		if hold := custody.Spec.RecoveryHold; hold.Deadline != nil || hold.Snapshot != nil {
			t.Fatalf("rebuilt hold = %+v", hold)
		}
		e.requirePhase(wipe, ops.PhaseOutcomeUnknown, ReasonRebuilt)
	})

	t.Run("a settled unknown outcome is not held again", func(t *testing.T) {
		e := e.sub(t)
		device := next()
		allocation := "alloc-s-" + device.Name
		e.allocation(device, allocation, "Releasing")
		verify(e, device, 0)
		install := e.installation(device, allocation, "request-1")
		install.Status = ops.OperationStatus{Admission: admitted(1), Phase: ops.PhaseOutcomeUnknown, Fence: 1, FinishedAt: ptrTime(e.clock.Now())}
		if err := e.admin.Status().Update(e.ctx, install); err != nil {
			t.Fatal(err)
		}
		wipe := e.wipe(device, allocation, 1)
		wipe.Status = ops.OperationStatus{Admission: admitted(2), Phase: ops.PhaseSucceeded, Fence: 2, FinishedAt: ptrTime(e.clock.Now())}
		if err := e.admin.Status().Update(e.ctx, wipe); err != nil {
			t.Fatal(err)
		}
		e.dropCustody(device.Name)
		custody := verify(e, device, 2)
		if custody.Spec.RecoveryHold != nil || len(custody.Spec.Conflicts) != 0 || custody.Spec.Fence != 3 {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
	})

	t.Run("a rebuilt hold on a retry keeps the retry lineage", func(t *testing.T) {
		e := e.sub(t)
		device := next()
		allocation := "alloc-l-" + device.Name
		e.allocation(device, allocation, "Releasing")
		original := e.wipe(device, allocation, 1)
		original.Status = ops.OperationStatus{Admission: admitted(1), Phase: ops.PhaseOutcomeUnknown, Fence: 1, FinishedAt: ptrTime(e.clock.Now())}
		if err := e.admin.Status().Update(e.ctx, original); err != nil {
			t.Fatal(err)
		}
		retried := e.namedWipe(device, allocation, original.Name+"-retry-2", 1)
		retried.Status = ops.OperationStatus{Admission: admitted(3), Phase: ops.PhaseOutcomeUnknown, Fence: 3, FinishedAt: ptrTime(e.clock.Now())}
		if err := e.admin.Status().Update(e.ctx, retried); err != nil {
			t.Fatal(err)
		}
		custody := verify(e, device, 3)
		hold := custody.Spec.RecoveryHold
		if hold == nil || hold.Name != retried.Name || hold.RetryName != original.Name || hold.Retries != 2 || RetryName(*hold, 3) != original.Name+"-retry-3" || len(custody.Spec.Conflicts) != 0 {
			t.Fatalf("rebuilt hold = %+v", hold)
		}
	})

	t.Run("several unproven outcomes keep the newest held and need a Resolve", func(t *testing.T) {
		e := e.sub(t)
		device := next()
		allocation := "alloc-m-" + device.Name
		e.allocation(device, allocation, "Active")
		var newest *ops.DeviceInstallation
		for index, fence := range []int64{2, 5} {
			install := e.installation(device, allocation, fmt.Sprintf("request-%d", index))
			install.Status = ops.OperationStatus{Admission: admitted(fence), Phase: ops.PhaseOutcomeUnknown, Fence: fence, FinishedAt: ptrTime(e.clock.Now())}
			if err := e.admin.Status().Update(e.ctx, install); err != nil {
				t.Fatal(err)
			}
			newest = install
		}
		custody := verify(e, device, 5)
		if custody.Spec.State != ops.CustodyUnverified || !slices.Contains(custody.Spec.Conflicts, ConflictMultipleUnfinished) || custody.Spec.RecoveryHold == nil || custody.Spec.RecoveryHold.Name != newest.Name {
			t.Fatalf("rebuilt = %+v", custody.Spec)
		}
		// Resolve must agree with the live allocation, and keeps the hold.
		wrong := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionResolve, Resolve: &ops.ResolveInput{Holder: ops.HolderNone}})
		e.settle(device.Name)
		e.requirePhase(wrong, ops.PhaseFailed, ReasonInvalidResolve)
		other := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionResolve, Resolve: &ops.ResolveInput{Holder: ops.HolderAllocation, AllocationRef: &ops.LocalReference{Name: "alloc-invented"}}})
		e.settle(device.Name)
		e.requirePhase(other, ops.PhaseFailed, ReasonInvalidResolve)
		resolve := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionResolve, Resolve: &ops.ResolveInput{Holder: ops.HolderAllocation, AllocationRef: &ops.LocalReference{Name: allocation}}})
		e.settle(device.Name)
		admitted := e.custody(device.Name).Spec.ActiveOperation
		if admitted == nil || admitted.Fence <= 5 {
			t.Fatalf("Resolve admitted at %+v, at or below the site's fence 5", admitted)
		}
		// A report of the site's old fence does not confirm the Resolve.
		e.settle(device.Name)
		e.requirePhase(resolve, ops.PhaseAdmitted, "")
		e.sites.report(device.Name, admitted.Fence)
		e.settle(device.Name)
		e.requirePhase(resolve, ops.PhaseSucceeded, "")
		custody = e.custody(device.Name)
		if custody.Spec.State != ops.CustodyVerified || custody.Spec.Holder.Type != ops.HolderAllocation || custody.Spec.RecoveryHold == nil || custody.Spec.RecoveryHold.Name != newest.Name {
			t.Fatalf("after Resolve: %+v", custody.Spec)
		}
	})

	t.Run("a succeeded Retire is kept past 30 days until inventory retires the Device", func(t *testing.T) {
		e := e.sub(t)
		device := next()
		verify(e, device, 0)
		e.staffOp(device, "ok")
		e.settle(device.Name)
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		hold := *e.custody(device.Name).Spec.RecoveryHold
		retire := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(retire, ops.PhaseSucceeded, "")
		e.clock.Advance(DefaultRetention + time.Hour)
		if _, err := e.coord.SweepRetention(e.ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(retire), retire); err != nil {
			t.Fatalf("the only record of the retirement was deleted: %v", err)
		}
		fence := e.custody(device.Name).Spec.Fence
		e.dropCustody(device.Name)
		if custody := verify(e, device, fence); custody.Spec.Holder.Type != ops.HolderRetired {
			t.Fatalf("rebuilt holder = %s", custody.Spec.Holder.Type)
		}
	})
}

func ptrTime(at time.Time) *metav1.Time {
	value := metav1.NewTime(at)
	return &value
}

// claimingClient claims custody and creates the installation the first time
// the coordinator lists installations, after it has read custody.
type claimingClient struct {
	client.Client
	once   sync.Once
	before func()
}

func (c *claimingClient) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	if _, ok := list.(*ops.DeviceInstallationList); ok {
		c.once.Do(c.before)
	}
	return c.Client.List(ctx, list, options...)
}

func TestARefusalNeverUsesCustodyReadBeforeAHolderChange(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-800", uid(800), fmt.Sprintf("%064x", 800))
	var install *ops.DeviceInstallation
	racing := &claimingClient{Client: e.client(), before: func() {
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-late"); err != nil {
			t.Error(err)
		}
		install = e.installation(device, "alloc-late", "request-1")
	}}
	coordinator := e.coordinator(racing)
	settleWith(t, e.ctx, coordinator, device.Name)
	e.requirePhase(install, ops.PhaseAdmitted, "")
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(install), install); apierrors.IsNotFound(err) {
		t.Fatal("installation missing")
	}
}

// TestFencesFollowTheSiteReport separates the fence a site has seen from the
// fence for which it confirmed older workers stopped.
func TestFencesFollowTheSiteReport(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-810", uid(810), fmt.Sprintf("%064x", 810))
	hold := e.holdOn(device)
	// The site has seen fence 20 from an assignment custody never recorded,
	// and has confirmed stopped workers only up to the held fence.
	e.sites.report(device.Name, hold.Fence)
	e.sites.reportSeen(device.Name, 20)
	retire := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(hold)})
	e.settle(device.Name)
	active := e.custody(device.Name).Spec.ActiveOperation
	if active == nil || active.Fence <= 20 {
		t.Fatalf("admitted at %+v, at or below the fence the site has seen", active)
	}
	// Seen alone never confirms a recovery.
	e.sites.reportSeen(device.Name, active.Fence)
	e.settle(device.Name)
	e.requirePhase(retire, ops.PhaseAdmitted, "")
	e.sites.report(device.Name, active.Fence)
	e.settle(device.Name)
	e.requirePhase(retire, ops.PhaseSucceeded, "")
}

// TestAHeldOperationIsNeverACandidate checks the exclusion on its own,
// without the status copy that also ends a held operation.
func TestAHeldOperationIsNeverACandidate(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-820", uid(820), fmt.Sprintf("%064x", 820))
	if err := e.coord.Claim(e.ctx, device.Name, "alloc-held"); err != nil {
		t.Fatal(err)
	}
	install := e.installation(device, "alloc-held", "request-1")
	custody := e.custody(device.Name)
	e.markAdmitted(install, custody.Spec.Fence+1)
	custody.Spec.RecoveryHold = &ops.RecoveryHold{OperationReference: reference("DeviceInstallation", install), Fence: custody.Spec.Fence + 1, Since: metav1.NewTime(e.clock.Now()), Reason: ReasonRebuilt}
	candidates, err := e.coord.candidates(e.ctx, custody)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.operation.GetUID() == install.UID {
			t.Fatal("the held operation is a candidate for admission")
		}
	}
}
