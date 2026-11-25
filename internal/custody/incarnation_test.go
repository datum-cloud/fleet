package custody

import (
	"fmt"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// No spec holds an object UID, so custody names an operation by kind and
// name with the fence it admitted it at. These tests pin the guards that
// keep a same-name recreation from inheriting anything.

// recreate deletes an operation and creates another with the same name and
// spec, as a client that lost the first might. The new object has no
// admission record.
func (e *env) recreate(operation ops.Operation) {
	e.t.Helper()
	if err := e.admin.Delete(e.ctx, operation); err != nil {
		e.t.Fatal(err)
	}
	fresh := operation.DeepCopyObject().(ops.Operation)
	fresh.SetResourceVersion("")
	fresh.SetUID("")
	*fresh.OperationStatus() = ops.OperationStatus{}
	e.create(fresh)
}

// statusOf reads an operation's current status by name.
func (e *env) statusOf(operation ops.Operation) ops.OperationStatus {
	e.t.Helper()
	current := operation.DeepCopyObject().(ops.Operation)
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), current); err != nil {
		e.t.Fatal(err)
	}
	return *current.OperationStatus()
}

// TestARecreatedActiveOperationNeverGetsItsResult recreates the active
// operation after admission, and the coordinator passes over it before the
// site's result arrives. The recreated object never takes the admission, so
// the result for it is never applied to that object.
func TestARecreatedActiveOperationNeverGetsItsResult(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-842", uid(842), fmt.Sprintf("%064x", 842))
	if err := e.coord.Claim(e.ctx, device.Name, "alloc-a"); err != nil {
		t.Fatal(err)
	}
	first := e.providerOp(device, "alloc-a", "again")
	e.settle(device.Name)
	active := *e.custody(device.Name).Spec.ActiveOperation
	e.recreate(first)
	e.settle(device.Name)
	_ = e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseSucceeded})
	e.settle(device.Name)
	if status := e.statusOf(first); status.Fence == active.Fence || status.Phase == ops.PhaseSucceeded || admittedAtStatus(status, active.Fence) {
		t.Fatalf("the recreated operation got the admission's result: %+v", status)
	}
}

// TestARecreatedOperationNeverInheritsItsLastResult recreates an operation
// after it ended. Custody's last result names the first at its fence, and is
// never copied to the second.
func TestARecreatedOperationNeverInheritsItsLastResult(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-840", uid(840), fmt.Sprintf("%064x", 840))
	if err := e.coord.Claim(e.ctx, device.Name, "alloc-r"); err != nil {
		t.Fatal(err)
	}
	first := e.providerOp(device, "alloc-r", "again")
	e.settle(device.Name)
	if err := e.result(device.Name, ops.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	e.settle(device.Name)
	ended := e.requirePhase(first, ops.PhaseSucceeded, "")
	e.recreate(first)
	e.settle(device.Name)
	if last := e.custody(device.Name).Spec.LastResult; last == nil || last.Name != first.Name || last.Fence != ended.Fence {
		t.Fatalf("custody's last result = %+v", last)
	}
	if status := e.statusOf(first); status.Fence == ended.Fence || status.Phase == ops.PhaseSucceeded {
		t.Fatalf("the recreated operation inherited the result: %+v", status)
	}
}

// TestARecreatedHeldOperationNeverTakesTheHold recreates an operation whose
// outcome custody holds. The hold stays on the first, and the second is
// neither held nor admitted as the held operation.
func TestARecreatedHeldOperationNeverTakesTheHold(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-843", uid(843), fmt.Sprintf("%064x", 843))
	if err := e.coord.Claim(e.ctx, device.Name, "alloc-h"); err != nil {
		t.Fatal(err)
	}
	if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-h"); err != nil {
		t.Fatal(err)
	}
	wipe := e.wipe(device, "alloc-h", 1)
	e.settle(device.Name)
	if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
		t.Fatal(err)
	}
	e.settle(device.Name)
	hold := *e.custody(device.Name).Spec.RecoveryHold
	e.recreate(wipe)
	e.settle(device.Name)
	if after := e.custody(device.Name).Spec.RecoveryHold; after == nil || heldReference(*after) != heldReference(hold) {
		t.Fatalf("hold after the recreation = %+v", after)
	}
	if status := e.statusOf(wipe); status.Phase == ops.PhaseOutcomeUnknown || status.Admission != nil {
		t.Fatalf("the recreated operation took the hold: %+v", status)
	}
}

// TestARecreatedRetryIsNeverCountedAsTheRetry recreates the operation a
// Retry admitted to settle a hold. The recreated object is not the Retry's
// operation: it is not admitted in its place, and a result for the Retry's
// admission never settles the hold through it.
func TestARecreatedRetryIsNeverCountedAsTheRetry(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-844", uid(844), fmt.Sprintf("%064x", 844))
	if err := e.coord.Claim(e.ctx, device.Name, "alloc-t"); err != nil {
		t.Fatal(err)
	}
	if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-t"); err != nil {
		t.Fatal(err)
	}
	e.wipe(device, "alloc-t", 1)
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
	retried := e.namedWipe(device, "alloc-t", RetryName(hold, 1), 1)
	e.settle(device.Name)
	e.requirePhase(retried, ops.PhaseAdmitted, "")
	active := *e.custody(device.Name).Spec.ActiveOperation
	e.recreate(retried)
	e.settle(device.Name)
	_ = e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseSucceeded})
	e.settle(device.Name)
	if status := e.statusOf(retried); admittedAtStatus(status, active.Fence) || status.Phase == ops.PhaseSucceeded {
		t.Fatalf("the recreated operation was counted as the Retry's: %+v", status)
	}
	if after := e.custody(device.Name).Spec.RecoveryHold; after == nil || heldReference(*after) != heldReference(hold) || after.Retries != 1 {
		t.Fatalf("hold after the recreated retry = %+v", after)
	}
}

func admittedAtStatus(status ops.OperationStatus, fence int64) bool {
	return status.Admission != nil && status.Admission.Fence == fence
}

// TestARecreatedDeviceLeavesCustodyUnverified recreates a Device under the
// same name. Custody's device-uid label names the first Device, so custody
// stays Unverified until a Resolve.
func TestARecreatedDeviceLeavesCustodyUnverified(t *testing.T) {
	e := startEnv(t)
	device := e.verified("server-example-841", uid(841), fmt.Sprintf("%064x", 841))
	before := e.custody(device.Name)
	if before.Labels[ops.LabelDeviceUID] != string(device.UID) {
		t.Fatalf("custody label = %q, want the Device UID", before.Labels[ops.LabelDeviceUID])
	}
	if err := e.admin.Delete(e.ctx, device); err != nil {
		t.Fatal(err)
	}
	recreated := e.device(device.Name, uid(841), fmt.Sprintf("%064x", 841))
	e.settle(device.Name)
	after := e.custody(device.Name)
	if after.Spec.State != ops.CustodyUnverified || after.Labels[ops.LabelDeviceUID] == string(recreated.UID) {
		t.Fatalf("custody for a recreated Device = %s, label %q", after.Spec.State, after.Labels[ops.LabelDeviceUID])
	}
}

// TestARecreatedRetryNeverSupersedesARebuiltHold rebuilds custody for a
// held wipe whose retry was deleted and recreated before it was admitted
// again. A later retry carries the lineage only if it was admitted, so the
// recreated one never takes the hold away from the wipe it was named for.
func TestARecreatedRetryNeverSupersedesARebuiltHold(t *testing.T) {
	e := startEnv(t)
	device := e.device("server-example-845", uid(845), fmt.Sprintf("%064x", 845))
	allocation := "alloc-n-" + device.Name
	e.allocation(device, allocation, "Releasing")
	original := e.wipe(device, allocation, 1)
	original.Status = ops.OperationStatus{Admission: admitted(1), Phase: ops.PhaseOutcomeUnknown, Fence: 1, FinishedAt: ptrTime(e.clock.Now())}
	if err := e.admin.Status().Update(e.ctx, original); err != nil {
		t.Fatal(err)
	}
	// The retry, deleted and recreated: it carries no admission record.
	e.namedWipe(device, allocation, original.Name+"-retry-1", 1)
	e.sites.report(device.Name, 1)
	e.settle(device.Name)
	custody := e.custody(device.Name)
	if hold := custody.Spec.RecoveryHold; hold == nil || hold.Name != original.Name || hold.Fence != 1 {
		t.Fatalf("rebuilt hold = %+v", custody.Spec.RecoveryHold)
	}
}
