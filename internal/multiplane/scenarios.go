package multiplane

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// The failure scenarios of #174's acceptance tests. Each phase checks one
// outcome through the provider project API and never writes custody.

// AdmitInstallSecond creates consumer B's installation and returns
// ErrAdmittedUnknown once custody admitted it.
func (r *Runner) AdmitInstallSecond(ctx context.Context) error { return r.admitIndex(ctx, 1) }

// recoveryMismatchLabel marks the staff recovery the mismatch phase
// creates, so a retried phase finds the same one. Recoveries take generated
// names; admission refuses any other.
const recoveryMismatchLabel = "fleet.datumapis.com/acceptance-scenario"

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
	var existing ops.DeviceRecoveryList
	if err := staff.List(ctx, &existing, client.InNamespace(DeviceNamespace), client.MatchingLabels{recoveryMismatchLabel: "recovery-mismatch"}); err != nil {
		return err
	}
	var recovery *ops.DeviceRecovery
	switch len(existing.Items) {
	case 0:
		creator, err := identity(ctx, filepath.Join(r.Config.State, "provider", "staff-project.json"))
		if err != nil {
			return err
		}
		recovery = &ops.DeviceRecovery{
			ObjectMeta: metav1.ObjectMeta{GenerateName: Device + "-recovery-", Namespace: DeviceNamespace, Labels: map[string]string{recoveryMismatchLabel: "recovery-mismatch"}},
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
	case 1:
		recovery = &existing.Items[0]
	default:
		return fmt.Errorf("%w: more than one mismatched recovery", errTerminal)
	}
	switch phase := recovery.Status.Phase; {
	case phase == ops.PhaseFailed && recovery.Status.Reason == custody.ReasonHoldMismatch:
	case phase == ops.PhaseFailed:
		return fmt.Errorf("%w: the mismatched recovery was refused for %s, not the hold", errTerminal, recovery.Status.Reason)
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

// TenantIsolation runs while consumer A holds the Device and consumer B
// holds nothing. B's project has no Device API at all; the allocator refuses
// to install for B; and an installation the Metal provider names for B's
// allocation, which never held the Device, is refused before anything is
// delivered, with custody's fence unchanged.
func (r *Runner) TenantIsolation(ctx context.Context) error {
	if err := r.Consumers[1].API.List(ctx, &ops.DeviceCustodyList{}); !meta.IsNoMatchError(err) && !apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: a consumer project serves Device custody: %v", errTerminal, err)
	}
	if err := r.Consumers[1].API.List(ctx, &ops.DeviceInstallationList{}); !meta.IsNoMatchError(err) && !apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: a consumer project serves Device operations: %v", errTerminal, err)
	}
	if _, _, _, _, err := r.installation(ctx, 1); !errors.Is(err, errNoActiveAllocation) {
		return fmt.Errorf("%w: installing for a consumer that holds nothing was not refused for its missing allocation: %v", errTerminal, err)
	}
	q, err := r.request(ctx, 1)
	if err != nil {
		return err
	}
	before := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, before); err != nil {
		return err
	}
	if before.Spec.Holder.Type != ops.HolderAllocation {
		return fmt.Errorf("%w: consumer A does not hold the Device", errTerminal)
	}
	var install fleet.Installation
	if err := q.project.API.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "debian-pxe"}, &install); err != nil {
		return err
	}
	device := &inventory.Device{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, device); err != nil {
		return err
	}
	unheld := allocation.Name(q.alloc.Machine)
	image, err := allocation.ParseImage(install.Spec.ImageRef.Name)
	if err != nil {
		return err
	}
	// B's allocation takes this name once B reuses the Device, so the refused
	// installation carries a synthetic request UID to keep its name apart
	// from B's later installation.
	const request = "00000000-0000-4000-8000-000000000174"
	operation := &ops.DeviceInstallation{}
	key := client.ObjectKey{Namespace: DeviceNamespace, Name: custody.InstallationName(unheld, request)}
	if err := r.Provider.API.Get(ctx, key, operation); apierrors.IsNotFound(err) {
		operation = &ops.DeviceInstallation{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: ops.DeviceInstallationSpec{
				OperationEnvelope: ops.OperationEnvelope{Creator: r.Allocator.ProviderCreator, DeviceRef: ops.DeviceReference{Name: device.Name, UID: string(device.UID)},
					Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: unheld}}},
				RequestUID: request, Image: image, BootInterface: "primary", TargetDisks: []string{"disk-0"}, Delivery: ops.DeliveryInput{Method: ops.DeliveryNetworkBoot},
			},
		}
		if err := r.Provider.API.Create(ctx, operation); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	switch phase := operation.Status.Phase; {
	case phase == ops.PhaseFailed && operation.Status.Reason == custody.ReasonHolderMismatch:
	case phase.Ended() || phase.Running():
		return fmt.Errorf("%w: the unheld installation is %s: %s", errTerminal, phase, operation.Status.Reason)
	default:
		return fmt.Errorf("%w: the unheld installation is %q", errPending, phase)
	}
	after := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, after); err != nil {
		return err
	}
	if after.Spec.Fence != before.Spec.Fence || after.Spec.ActiveOperation != nil || operation.Status.Fence != 0 {
		return fmt.Errorf("%w: the refused installation changed custody", errTerminal)
	}
	fmt.Println("tenant-isolation: consumer B's project has no Device API; the allocator refused to install for it; an installation for its unheld allocation was refused for HolderMismatch with the fence unchanged")
	return nil
}
