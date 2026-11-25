package multiplane

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
)

// The failure scenarios of #174's acceptance tests. Each phase checks one
// outcome through the provider project API and never writes custody.

// AdmitInstallSecond creates consumer B's installation and returns
// ErrAdmittedUnknown once custody admitted it.
func (r *Runner) AdmitInstallSecond(ctx context.Context) error { return r.admitIndex(ctx, 1) }

// recoveryMismatchName names the staff recovery the mismatch phase creates,
// so a retried phase finds the same one.
const recoveryMismatchName = "synthetic-recovery-mismatch"

// RecoveryMismatch creates, as staff, a DeviceRecovery that names the held
// operation at the wrong fence. Admission refuses it before anything is
// delivered: custody's fence and hold stay as they were.
func (r *Runner) RecoveryMismatch(ctx context.Context) error {
	staff, err := API(filepath.Join(r.Config.State, "provider", "staff-project.json"))
	if err != nil {
		return err
	}
	before := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, before); err != nil {
		return err
	}
	hold := before.Spec.RecoveryHold
	if hold == nil {
		return fmt.Errorf("%w: the Device has no recovery hold", errTerminal)
	}
	device := &inventory.Device{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, device); err != nil {
		return err
	}
	recovery := &ops.DeviceRecovery{}
	if err := staff.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: recoveryMismatchName}, recovery); apierrors.IsNotFound(err) {
		creator, err := identity(ctx, filepath.Join(r.Config.State, "provider", "staff-project.json"))
		if err != nil {
			return err
		}
		recovery = &ops.DeviceRecovery{
			ObjectMeta: metav1.ObjectMeta{Name: recoveryMismatchName, Namespace: DeviceNamespace},
			Spec: ops.DeviceRecoverySpec{
				OperationEnvelope: ops.OperationEnvelope{Creator: creator, DeviceRef: ops.DeviceReference{Name: device.Name, UID: string(device.UID)},
					Authority: ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: "synthetic acceptance: a recovery for another hold"}}},
				Decision: ops.DecisionSettle,
				Hold:     &ops.HeldOperationReference{Kind: hold.Kind, Name: hold.Name, Fence: hold.Fence + 1000},
				Evidence: ops.RecoveryEvidence{ObservedAt: metav1.Now(), Summary: "synthetic acceptance evidence"},
			},
		}
		if err := staff.Create(ctx, recovery); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	switch phase := recovery.Status.Phase; {
	case phase == ops.PhaseFailed:
	case phase.Ended():
		return fmt.Errorf("%w: the mismatched recovery ended %s: %s", errTerminal, phase, recovery.Status.Reason)
	default:
		return fmt.Errorf("%w: the mismatched recovery is %q", errPending, phase)
	}
	after := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, after); err != nil {
		return err
	}
	if after.Spec.Fence != before.Spec.Fence || after.Spec.RecoveryHold == nil || after.Spec.RecoveryHold.UID != hold.UID || after.Spec.ActiveOperation != nil {
		return fmt.Errorf("%w: the refused recovery changed custody", errTerminal)
	}
	if recovery.Status.Fence != 0 || recovery.Status.AdmittedAt != nil {
		return fmt.Errorf("%w: the refused recovery was admitted", errTerminal)
	}
	fmt.Printf("recovery-mismatch: refused %s; fence %d and the hold unchanged\n", recovery.Status.Reason, after.Spec.Fence)
	return nil
}

// ProbeUnallocatable tries to allocate consumer A's Machine while custody
// is Unverified. The allocator must find no eligible capacity and record no
// allocation for the Machine.
func (r *Runner) ProbeUnallocatable(ctx context.Context) error {
	state := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, state); err != nil {
		return fmt.Errorf("%w: custody: %v", errPending, err)
	}
	if state.Spec.State != ops.CustodyUnverified {
		return fmt.Errorf("%w: custody is %s, not Unverified", errTerminal, state.Spec.State)
	}
	q, err := r.request(ctx, 0)
	if err != nil {
		return err
	}
	if _, err := r.Allocator.Allocate(ctx, q.alloc); !errors.Is(err, allocation.ErrInsufficientCapacity) {
		return fmt.Errorf("%w: allocation against Unverified custody: %v", errTerminal, err)
	}
	current, err := r.Allocator.Get(ctx, q.alloc.Machine)
	if err != nil {
		return err
	}
	if current != nil && current.Status.Phase != fleet.AllocationReleased {
		return fmt.Errorf("%w: an allocation exists for the probe Machine", errTerminal)
	}
	fmt.Println("probe-unallocatable: no eligible capacity while custody is Unverified; no allocation recorded")
	return nil
}
