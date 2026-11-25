package custody

import (
	"fmt"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// These tests guard the coordinator's use of site fence reports: a recovery
// applies only once every serving site confirms its fence, the fence it
// records is the lowest any site confirmed, and a Resolve raises custody's
// fence above every fence a site has seen. Each one fails if its guard is
// removed.

// reportAt stores the stopped fence one named site reports for a Device.
func (s *fakeSites) reportAt(site, device string, fence int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fences[site+"/"+device] = fence
}

// serve sets the sites that serve a Device.
func (s *fakeSites) serve(device string, sites ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serving[device] = sites
}

func TestARecoveryOnADeviceNoSiteServesIsNeverConfirmed(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-830", uid(830), fmt.Sprintf("%064x", 830))
	hold := e.holdOn(device)
	e.sites.serve(device.Name)
	retire := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(hold)})
	e.settle(device.Name)
	if status := e.status(retire); status.Phase == ops.PhaseSucceeded {
		t.Fatalf("a recovery applied with no serving site: %+v", status)
	}
	e.clock.Advance(RecoveryDeadline + DefaultSkewMargin)
	e.settle(device.Name)
	if status := e.status(retire); status.Phase == ops.PhaseSucceeded {
		t.Fatalf("a recovery applied with no serving site after its deadline: %+v", status)
	}
	if custody := e.custody(device.Name); custody.Spec.RecoveryHold == nil || *heldRef(*custody.Spec.RecoveryHold) != *heldRef(hold) || custody.Spec.Holder.Type == ops.HolderRetired {
		t.Fatalf("custody after an unconfirmed recovery = %+v", custody.Spec)
	}
}

func TestARecoveryConfirmsTheLowestFenceOfItsSites(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-831", uid(831), fmt.Sprintf("%064x", 831))
	hold := e.holdOn(device)
	e.sites.serve(device.Name, testSite, "site-example-b")
	e.sites.reportAt("site-example-b", device.Name, hold.Fence)
	retire := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetire, Hold: heldRef(hold)})
	e.settle(device.Name)
	active := e.custody(device.Name).Spec.ActiveOperation
	if active == nil {
		t.Fatal("the recovery was not admitted")
	}
	// One site confirms; the other has not stopped its older worker yet.
	e.sites.reportAt(testSite, device.Name, active.Fence)
	e.settle(device.Name)
	e.requirePhase(retire, ops.PhaseAdmitted, "")
	// Both confirm, at different fences: the lower one is recorded.
	e.sites.reportAt(testSite, device.Name, active.Fence+3)
	e.sites.reportAt("site-example-b", device.Name, active.Fence)
	e.settle(device.Name)
	status := e.requirePhase(retire, ops.PhaseSucceeded, "")
	var evidence ops.RecoveryResultEvidence
	if err := jsonUnmarshal(status.Evidence.Raw, &evidence); err != nil || evidence.ConfirmedFence != active.Fence {
		t.Fatalf("recovery evidence = %s, want the lowest confirmed fence %d", status.Evidence.Raw, active.Fence)
	}
}

func TestAResolveRaisesTheFenceAboveEverySeenFence(t *testing.T) {
	e := startEnv(t)
	device := e.device("server-example-832", uid(832), fmt.Sprintf("%064x", 832))
	allocation := "alloc-s-" + device.Name
	e.allocation(device, allocation, "Active")
	// Two unproven installations make the rebuild need a Resolve.
	for index, fence := range []int64{2, 5} {
		install := e.installation(device, allocation, fmt.Sprintf("request-%d", index))
		install.Status = ops.OperationStatus{Admission: admitted(fence), Phase: ops.PhaseOutcomeUnknown, Fence: fence, FinishedAt: ptrTime(e.clock.Now())}
		if err := e.admin.Status().Update(e.ctx, install); err != nil {
			t.Fatal(err)
		}
	}
	e.sites.report(device.Name, 5)
	e.settle(device.Name)
	if state := e.custody(device.Name).Spec.State; state != ops.CustodyUnverified {
		t.Fatalf("rebuilt custody is %s, want Unverified", state)
	}
	resolve := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionResolve, Resolve: &ops.ResolveInput{Holder: ops.HolderAllocation, AllocationRef: &ops.LocalReference{Name: allocation}}})
	e.settle(device.Name)
	active := e.custody(device.Name).Spec.ActiveOperation
	if active == nil {
		t.Fatal("the Resolve was not admitted")
	}
	// The site confirms the Resolve's fence, and has seen a higher one from
	// an assignment custody never recorded.
	e.sites.report(device.Name, active.Fence)
	e.sites.reportSeen(device.Name, active.Fence+40)
	e.settle(device.Name)
	e.requirePhase(resolve, ops.PhaseSucceeded, "")
	if custody := e.custody(device.Name); custody.Spec.State != ops.CustodyVerified || custody.Spec.Fence < active.Fence+40 {
		t.Fatalf("resolved custody = %+v, want Verified at a fence of at least %d", custody.Spec, active.Fence+40)
	}
}
