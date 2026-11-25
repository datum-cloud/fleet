package custody

import (
	"fmt"
	"strings"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestExtensionSeams covers what a new kind, such as power, relies on.
func TestExtensionSeams(t *testing.T) {
	e := startEnv(t)
	n := 900
	fresh := func() *inventory.Device {
		n++
		return e.verified(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
	}

	t.Run("checks see the Device the snapshot records; a Device change before the update decides again", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		var seen []string
		var intervals []time.Duration
		touched := false
		hook := func(admission *Admission) {
			seen = append(seen, admission.Device.ResourceVersion)
			intervals = append(intervals, admission.DiscoveryInterval)
			if !touched {
				// Discovery writes the Device after the check read it.
				touched = true
				e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now().Add(time.Second))
			}
		}
		checkHook.Store(&hook)
		defer checkHook.Store(nil)
		e.staffOp(device, "ok")
		e.settle(device.Name)
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 2 || seen[0] == seen[1] || seen[1] != device.ResourceVersion {
			t.Fatalf("checks saw Device versions %v; the current version is %s", seen, device.ResourceVersion)
		}
		if intervals[0] != DefaultDiscoveryInterval {
			t.Fatalf("discovery interval = %s", intervals[0])
		}
		if active := e.custody(device.Name).Spec.ActiveOperation; active == nil || active.Snapshot.DeviceGeneration != device.Generation {
			t.Fatalf("active = %+v", active)
		}
	})

	t.Run("a newer pending operation replaces only its own kind under the same authority", func(t *testing.T) {
		e := e.sub(t)
		// Two kinds that supersede share one allocation, as power and a
		// future provider kind would.
		e.coord.Registry.Kind("DeviceInstallation").Supersedes = true
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-scope"); err != nil {
			t.Fatal(err)
		}
		running := e.installation(device, "alloc-scope", "request-running")
		e.settle(device.Name)
		e.requirePhase(running, ops.PhaseAdmitted, "")
		olderInstall := e.installation(device, "alloc-scope", "request-older")
		time.Sleep(1100 * time.Millisecond)
		older := e.providerOp(device, "alloc-scope", "older")
		time.Sleep(1100 * time.Millisecond)
		newerInstall := e.installation(device, "alloc-scope", "request-newer")
		time.Sleep(1100 * time.Millisecond)
		newer := e.providerOp(device, "alloc-scope", "newer")
		e.settle(device.Name)
		// Each kind's newest pending operation replaces only that kind's
		// older one.
		e.requirePhase(olderInstall, ops.PhaseCancelled, ReasonSuperseded)
		e.requirePhase(newerInstall, ops.PhasePending, ReasonOperationInProgress)
		e.requirePhase(older, ops.PhaseCancelled, ReasonSuperseded)
		e.requirePhase(newer, ops.PhasePending, ReasonOperationInProgress)
	})

	t.Run("kinds add bounded snapshot fields that the hold and the settle hook keep", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		operation := e.staffOp(device, "extra")
		e.settle(device.Name)
		active := e.custody(device.Name).Spec.ActiveOperation
		if active == nil || active.Snapshot.Extra["resetTypes"] != "On,ForceOff" || active.Snapshot.Extra["discoveryInterval"] != DefaultDiscoveryInterval.String() {
			t.Fatalf("active snapshot = %+v", active)
		}
		if status := e.status(operation); status.Snapshot == nil || status.Snapshot.Extra["resetTypes"] != "On,ForceOff" {
			t.Fatalf("status snapshot = %+v", status.Snapshot)
		}
		if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		hold := *e.custody(device.Name).Spec.RecoveryHold
		if hold.Snapshot == nil || hold.Snapshot.Extra["resetTypes"] != "On,ForceOff" || hold.Deadline == nil || !hold.Deadline.Equal(&active.Deadline) {
			t.Fatalf("hold = %+v", hold)
		}
		// The settle hook requires the hold's deadline and snapshot.
		e.clock.Advance(time.Minute)
		settle := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: heldRef(hold), Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now()), Summary: "s", Data: map[string]string{"state": "On"}}})
		e.settle(device.Name)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(settle, ops.PhaseSucceeded, "")

		bounded := fresh()
		tooMuch := e.staffOp(bounded, "too-much")
		e.settle(bounded.Name)
		e.requirePhase(tooMuch, ops.PhaseFailed, ReasonSnapshotInvalid)
	})

	t.Run("a row may admit from Service only under a hold its kind settles", func(t *testing.T) {
		e := e.sub(t)
		// The staff power row: None, or Service under a power hold.
		if err := e.coord.Registry.AddRule(Rule{Kind: testStaffKind, Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderService}, Hold: HoldSettlesRequired}); err != nil {
			t.Fatal(err)
		}
		device := fresh()
		hold := e.holdOn(device)
		if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderService {
			t.Fatalf("holder = %s", holder)
		}
		settling := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(settling, ops.PhaseAdmitted, "")
		if active := e.custody(device.Name).Spec.ActiveOperation; active.Settles == nil || active.Settles.UID != hold.UID {
			t.Fatalf("active = %+v", active)
		}
		if err := e.result(device.Name, ops.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		if custody := e.custody(device.Name); custody.Spec.RecoveryHold != nil || custody.Spec.Holder.Type != ops.HolderNone {
			t.Fatalf("after settling: %+v", custody.Spec)
		}
	})

	t.Run("a refusal with a reason the kind never declared is recorded as UndeclaredReason", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		operation := e.staffOp(device, "undeclared")
		e.settle(device.Name)
		status := e.requirePhase(operation, ops.PhaseFailed, ReasonUndeclared)
		if !strings.Contains(status.Message, "Unlisted") {
			t.Fatalf("message = %s", status.Message)
		}
	})

	t.Run("Retry applies only to a kind that declares it", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		hold := e.holdOn(device)
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		e.settle(device.Name)
		e.requirePhase(retry, ops.PhaseFailed, ReasonNotRetryable)
		// A kind that another kind settles after a retry still refuses Retry
		// unless it declares itself Retryable.
		e.coord.Registry.Kind("DeviceWipe").Retryable = false
		wiped := fresh()
		if err := e.coord.Claim(e.ctx, wiped.Name, "alloc-nr"); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, wiped.Name, "alloc-nr"); err != nil {
			t.Fatal(err)
		}
		e.wipe(wiped, "alloc-nr", 1)
		e.settle(wiped.Name)
		if err := e.result(wiped.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(wiped.Name)
		wipeHold := *e.custody(wiped.Name).Spec.RecoveryHold
		refused := e.recovery(wiped, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(wipeHold)})
		e.settle(wiped.Name)
		e.requirePhase(refused, ops.PhaseFailed, ReasonNotRetryable)
	})

	t.Run("a worker refusal with nothing sent ends Failed and returns a Service holder to None", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		operation := e.staffOp(device, "ok")
		e.settle(device.Name)
		active := e.custody(device.Name).Spec.ActiveOperation
		if err := e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseFailed, Reason: "PublicKeyMismatch"}); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, "PublicKeyMismatch")
		if custody := e.custody(device.Name); custody.Spec.Holder.Type != ops.HolderNone || custody.Spec.RecoveryHold != nil || custody.Spec.Fence != active.Fence {
			t.Fatalf("custody = %+v", custody.Spec)
		}
	})
}

func TestRegistryValidatesDeclaredReasons(t *testing.T) {
	for name, reasons := range map[string][]Reason{
		"not CamelCase": {{Name: "not-camel", RaisedBy: RaisedByCoordinator}},
		"shared":        {{Name: ReasonHolderMismatch, RaisedBy: RaisedByCoordinator}},
		"twice":         {{Name: "Twice", RaisedBy: RaisedByCoordinator}, {Name: "Twice", RaisedBy: RaisedByWorker}},
		"no source":     {{Name: "NoSource"}},
	} {
		kind := testKinds()[0]
		kind.Reasons = reasons
		if err := NewRegistry().Register(kind); err == nil {
			t.Fatalf("%s: registered", name)
		}
	}
	registry := testRegistry(t)
	if !registry.Declared("DeviceInstallation", ReasonBootstrapUnsupported, RaisedByWorker) || !registry.Declared("DeviceInstallation", ReasonBootInterfaceUnknown, RaisedByCoordinator) || registry.Declared("DeviceInstallation", ReasonBootInterfaceUnknown, RaisedByWorker) {
		t.Fatal("declared sources are not kept")
	}
	// A kind may settle no hold itself.
	none := testKinds()[1]
	none.GVK.Kind, none.Settles = "DeviceTestUnsettled", nil
	c := &Coordinator{Registry: registry}
	if c.settlesHold(&none, ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: "DeviceTestUnsettled"}}, &DeviceTestProviderOperation{}) {
		t.Fatal("a kind with no settle rules settled a hold")
	}
}
