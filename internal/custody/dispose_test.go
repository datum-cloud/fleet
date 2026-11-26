package custody

import (
	"context"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// TestARebuiltHoldCannotBeDisposed refuses DisposeDrive under a hold that
// rebuild formed. A rebuilt hold has no record of which drives were held, so
// nothing could say which drive is disposed of.
func TestARebuiltHoldCannotBeDisposed(t *testing.T) {
	custody := &ops.DeviceCustody{Spec: ops.DeviceCustodySpec{
		Holder:       ops.Holder{Type: ops.HolderRelease, AllocationRef: &ops.LocalReference{Name: "alloc-r"}},
		RecoveryHold: &ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: "DeviceWipe", Name: "alloc-r-wipe-1"}, Fence: 4, Reason: ReasonRebuilt},
	}}
	recovery := &ops.DeviceRecovery{Spec: ops.DeviceRecoverySpec{Decision: ops.DecisionDisposeDrive,
		Drive: &ops.DriveDisposal{Name: "disk-0", Method: ops.DisposalRemoved, Reference: "ticket-r"}}}
	if _, refusal := disposedDrive(context.Background(), recovery, custody, &Admission{}); refusal == nil || refusal.Reason != ReasonNotSettleable {
		t.Fatalf("refusal = %+v, want NotSettleable", refusal)
	}
}
