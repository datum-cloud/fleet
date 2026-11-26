package custody

import (
	"errors"
	"fmt"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestRowsAndReplacement covers the seams the power kind found missing.
func TestRowsAndReplacement(t *testing.T) {
	e := startEnv(t)
	n := 1000
	fresh := func() *inventory.Device {
		n++
		return e.verified(fmt.Sprintf("server-example-%04d", n), uid(n), fmt.Sprintf("%064x", n))
	}

	t.Run("a request refused its permission check replaces nothing", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-cust"); err != nil {
			t.Fatal(err)
		}
		e.installation(device, "alloc-cust", "request-running")
		e.settle(device.Name)
		customer := e.providerOp(device, "alloc-cust", "mine")
		e.settle(device.Name)
		e.requirePhase(customer, ops.PhasePending, ReasonOperationInProgress)
		time.Sleep(1100 * time.Millisecond)
		// Staff name the customer's allocation with the superseding kind.
		staff := &DeviceTestProviderOperation{ObjectMeta: metav1.ObjectMeta{Name: "alloc-cust-test-theirs", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, allocated("alloc-cust")), Input: "theirs"}}
		e.create(staff)
		e.settle(device.Name)
		e.requirePhase(staff, ops.PhaseFailed, ReasonCreatorNotPermitted)
		e.requirePhase(customer, ops.PhasePending, ReasonOperationInProgress)
	})

	t.Run("staff requests never replace one another", func(t *testing.T) {
		e := e.sub(t)
		e.coord.Registry.Kind(testStaffKind).Supersedes = true
		device := fresh()
		older := e.staffOp(device, "ok")
		time.Sleep(1100 * time.Millisecond)
		newer := e.staffOp(device, "ok")
		e.settle(device.Name)
		// Each is decided on its own: the older is admitted, and the newer
		// then finds a Service holder.
		e.requirePhase(older, ops.PhaseAdmitted, "")
		e.requirePhase(newer, ops.PhaseFailed, ReasonHolderMismatch)
	})

	t.Run("a row may name its operations differently from the kind", func(t *testing.T) {
		e := e.sub(t)
		// Provider-created operations of the staff kind are named from their
		// allocation, while staff-created ones keep generated names.
		if err := e.coord.Registry.AddRule(Rule{Kind: testStaffKind, Creator: CreatorMetalProvider, Authority: AuthorityAllocation, Holders: []ops.HolderType{ops.HolderAllocation}, Queue: true,
			Naming: DeterministicName, Name: func(operation ops.Operation) string {
				return allocationName(operation) + "-staff-" + operation.(*DeviceTestStaffOperation).Spec.Input
			}}); err != nil {
			t.Fatal(err)
		}
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-row"); err != nil {
			t.Fatal(err)
		}
		named := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{Name: "alloc-row-staff-a", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, providerUser, allocated("alloc-row")), Input: "a"}}
		e.create(named)
		e.settle(device.Name)
		e.requirePhase(named, ops.PhaseAdmitted, "")
		// A row with a deterministic rule needs its Name function.
		if err := e.coord.Registry.AddRule(Rule{Kind: testStaffKind, Creator: CreatorReleaseController, Authority: AuthorityAllocation, Naming: DeterministicName}); err == nil {
			t.Fatal("a deterministic row without a name rule was added")
		}
	})

	t.Run("a kind's own refusal comes before the shared identity check", func(t *testing.T) {
		e := e.sub(t)
		n++
		device := e.device(fmt.Sprintf("server-example-%04d", n), "", fmt.Sprintf("%064x", n))
		e.settle(device.Name)
		e.sites.report(device.Name, 0)
		e.settle(device.Name)
		operation := e.staffOp(device, "needs-uuid")
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, reasonTestUUID)
	})

	t.Run("a staff retry created before its Retry waits for it", func(t *testing.T) {
		e := e.sub(t)
		// The staff kind settles its own hold only after a Retry, from
		// Service under that hold, as staff power does.
		staff := e.coord.Registry.Kind(testStaffKind)
		staff.Settles, staff.Retryable = []SettleRule{{Held: testStaffKind, RequiresRetry: true}}, true
		if err := e.coord.Registry.AddRule(Rule{Kind: testStaffKind, Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderService}, Hold: HoldSettlesRequired}); err != nil {
			t.Fatal(err)
		}
		device := fresh()
		hold := e.holdOn(device)
		retried := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{Name: RetryName(hold, 1), Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, service("retry")), Input: "ok"}}
		e.create(retried)
		e.settle(device.Name)
		e.requirePhase(retried, ops.PhasePending, ReasonRecoveryHold)
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		e.settle(device.Name)
		// It does not expire while it waits for the Retry to be applied.
		e.clock.Advance(DefaultStaffExpiry + time.Minute)
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		e.settle(device.Name)
		e.requirePhase(retried, ops.PhasePending, ReasonRecoveryHold)
		e.sites.report(device.Name, e.custody(device.Name).Spec.Fence)
		e.settle(device.Name)
		e.requirePhase(retry, ops.PhaseSucceeded, "")
		e.requirePhase(retried, ops.PhaseAdmitted, "")
	})

	t.Run("evidence is checked against its operation and can be required", func(t *testing.T) {
		e := e.sub(t)
		e.coord.Registry.Kind(testStaffKind).EvidenceRequired = true
		bare := fresh()
		e.staffOp(bare, "ok")
		e.settle(bare.Name)
		if err := e.result(bare.Name, ops.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(bare.Name)
		if hold := e.custody(bare.Name).Spec.RecoveryHold; hold == nil || hold.Reason != ReasonInvalidEvidence {
			t.Fatalf("a success without required evidence was accepted: %+v", hold)
		}
		restart := fresh()
		e.staffOp(restart, "restart")
		e.settle(restart.Name)
		if err := e.result(restart.Name, ops.PhaseSucceeded, `{"before":"On"}`); err != nil {
			t.Fatal(err)
		}
		e.settle(restart.Name)
		if hold := e.custody(restart.Name).Spec.RecoveryHold; hold == nil || hold.Reason != ReasonInvalidEvidence {
			t.Fatalf("a restart without proof of the reset was accepted: %+v", hold)
		}
		proven := fresh()
		operation := e.staffOp(proven, "restart")
		e.settle(proven.Name)
		if err := e.result(proven.Name, ops.PhaseSucceeded, `{"before":"On","after":"On"}`); err != nil {
			t.Fatal(err)
		}
		e.settle(proven.Name)
		e.requirePhase(operation, ops.PhaseSucceeded, "")
	})

	t.Run("a result for an unregistered kind is rejected as stale", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		e.staffOp(device, "ok")
		e.settle(device.Name)
		active := e.custody(device.Name).Spec.ActiveOperation
		err := e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: ops.OperationReference{Kind: "DeviceInvented", Name: active.Name}, Fence: active.Fence, Phase: ops.PhaseSucceeded, Evidence: []byte(`{"after":"On"}`)})
		if !errors.Is(err, ErrStaleFence) {
			t.Fatalf("result for an unregistered kind: %v", err)
		}
		if rejected := e.custody(device.Name).Spec.RejectedResults; rejected == nil || rejected.Count != 1 {
			t.Fatalf("rejected = %+v", rejected)
		}
	})
}
