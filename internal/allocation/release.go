package allocation

import (
	"context"
	"errors"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// maxWipeSteps bounds the wipes one release creates after wipes that ended
// Failed, which sent nothing. A wipe whose outcome is unknown is never
// repeated without a DeviceRecovery Retry.
const maxWipeSteps = 3

// ErrWipeFailed means the release created its last wipe step and it failed.
// The Cleaning gate stays open, and the Device stays in Release.
var ErrWipeFailed = errors.New("every wipe step of the release failed")

// Release advances the release of an allocation by one step and reports
// whether it is Released. Callers repeat it while the custody coordinator
// runs. The holder moves to Release, the release controller closes each
// gate with evidence, the coordinator returns the Device to None, and only
// then is the allocation Released.
func (a *Allocator) Release(ctx context.Context, allocation *fleetv1.MachineAllocation, machine Machine) (bool, error) {
	if allocation.Status.Phase == fleetv1.AllocationReleased {
		return true, nil
	}
	state := &ops.DeviceCustody{}
	if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: allocation.Spec.DeviceRef.Name}, state); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	holder := state.Spec.Holder
	ours := holder.AllocationRef != nil && holder.AllocationRef.Name == allocation.Name && (holder.Type == ops.HolderAllocation || holder.Type == ops.HolderRelease)
	if !ours {
		switch {
		case allocation.Status.Phase != fleetv1.AllocationActive && allocation.Status.Phase != fleetv1.AllocationReleasing:
			// The allocation never held the Device: nothing ran under it.
		case state.Spec.State == ops.CustodyVerified && allocation.Status.Phase == fleetv1.AllocationReleasing:
			// Custody returned the Device after every gate closed.
		default:
			// Unverified custody waits for an operator to resolve it.
			return false, nil
		}
		if err := a.releaseQuota(ctx, allocation, machine); err != nil {
			return false, err
		}
		return true, a.setPhase(ctx, allocation, fleetv1.AllocationReleased)
	}
	if err := a.setPhase(ctx, allocation, fleetv1.AllocationReleasing); err != nil {
		return false, err
	}
	if holder.Type == ops.HolderAllocation {
		return false, a.Custody.BeginRelease(ctx, state.Name, allocation.Name)
	}
	// No mode provisions access, networks or addresses for a Machine, so
	// those gates have nothing to release.
	for _, gate := range []ops.ReleaseGate{ops.GateAccess, ops.GateNetwork, ops.GateAddress} {
		if slices.Contains(holder.OpenGates, gate) {
			return false, a.Custody.CloseGate(ctx, state.Name, allocation.Name, gate)
		}
	}
	if slices.Contains(holder.OpenGates, ops.GateOperations) {
		running, err := a.installing(ctx, state, allocation)
		if err != nil || running {
			return false, err
		}
		return false, a.Custody.CloseGate(ctx, state.Name, allocation.Name, ops.GateOperations)
	}
	if slices.Contains(holder.OpenGates, ops.GateCleaning) {
		cleaned, err := a.clean(ctx, state, allocation)
		if err != nil || !cleaned {
			return false, err
		}
		return false, a.Custody.CloseGate(ctx, state.Name, allocation.Name, ops.GateCleaning)
	}
	if slices.Contains(holder.OpenGates, ops.GateQuota) {
		if err := a.releaseQuota(ctx, allocation, machine); err != nil {
			return false, err
		}
		return false, a.Custody.CloseGate(ctx, state.Name, allocation.Name, ops.GateQuota)
	}
	return false, nil
}

func (a *Allocator) releaseQuota(ctx context.Context, allocation *fleetv1.MachineAllocation, machine Machine) error {
	if allocation.Spec.QuotaClaimRef == nil || a.Quota == nil {
		return nil
	}
	return a.Quota.Release(ctx, machine, allocation.Spec.QuotaClaimRef.Name)
}

// installing reports whether an installation of this allocation is active
// or has not ended. A pending one ends once admission refuses it, because
// installations are admitted only while the allocation holds the Device.
func (a *Allocator) installing(ctx context.Context, state *ops.DeviceCustody, allocation *fleetv1.MachineAllocation) (bool, error) {
	if active := state.Spec.ActiveOperation; active != nil && active.Kind == "DeviceInstallation" {
		return true, nil
	}
	var installations ops.DeviceInstallationList
	if err := a.Provider.List(ctx, &installations, client.InNamespace(a.Namespace)); err != nil {
		return false, err
	}
	for _, installation := range installations.Items {
		reference := installation.Spec.Authority.AllocationRef
		if reference != nil && reference.Name == allocation.Name && !installation.Status.Phase.Ended() {
			return true, nil
		}
	}
	return false, nil
}

// clean drives the release's wipes and reports whether one succeeded. A
// wipe that ended Failed sent nothing, so a new step follows, up to
// maxWipeSteps. A wipe whose outcome is unknown is held: the next one is
// created only after staff authorize a Retry, under the name custody gives.
func (a *Allocator) clean(ctx context.Context, state *ops.DeviceCustody, allocation *fleetv1.MachineAllocation) (bool, error) {
	current := allocation.Status.Wipe
	if current == nil {
		return false, a.wipe(ctx, allocation, fleetv1.WipeReference{Name: custody.WipeName(allocation.Name, 1), Step: 1})
	}
	wipe := &ops.DeviceWipe{}
	if err := a.Releaser.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: current.Name}, wipe); apierrors.IsNotFound(err) {
		// The status was written but the create was lost.
		return false, a.wipe(ctx, allocation, *current)
	} else if err != nil {
		return false, err
	}
	switch wipe.Status.Phase {
	case ops.PhaseSucceeded:
		return true, nil
	case ops.PhaseFailed, ops.PhaseCancelled:
		if current.Step >= maxWipeSteps {
			return false, fmt.Errorf("%w: %s: %s", ErrWipeFailed, wipe.Status.Reason, wipe.Status.Message)
		}
		step := current.Step + 1
		return false, a.wipe(ctx, allocation, fleetv1.WipeReference{Name: custody.WipeName(allocation.Name, step), Step: step})
	case ops.PhaseOutcomeUnknown:
		hold := state.Spec.RecoveryHold
		if hold != nil && hold.Kind == "DeviceWipe" && hold.Name == current.Name && hold.RetryName != "" {
			return false, a.wipe(ctx, allocation, fleetv1.WipeReference{Name: hold.RetryName, Step: current.Step + 1})
		}
	}
	return false, nil
}

// wipe records the wipe in status, then creates it, so a crash between the
// two creates the same wipe again.
func (a *Allocator) wipe(ctx context.Context, allocation *fleetv1.MachineAllocation, reference fleetv1.WipeReference) error {
	if current := allocation.Status.Wipe; current == nil || *current != reference {
		allocation.Status.Wipe = &reference
		if err := a.Provider.Status().Update(ctx, allocation); err != nil {
			return err
		}
	}
	wipe := &ops.DeviceWipe{
		ObjectMeta: metav1.ObjectMeta{Name: reference.Name, Namespace: a.Namespace},
		Spec: ops.DeviceWipeSpec{
			OperationEnvelope:    a.envelope(allocation, a.ReleaseCreator),
			Step:                 reference.Step,
			CleaningPolicyDigest: allocation.Spec.CleaningPolicyDigest,
			TargetDisks:          slices.Clone(allocation.Spec.TargetDisks),
		},
	}
	return a.createOnce(ctx, a.Releaser, wipe, &ops.DeviceWipe{})
}
