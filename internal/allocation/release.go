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
	"go.datum.net/fleet/internal/bootaddress"
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
		if gone, err := a.releaseBootAddresses(ctx, allocation, true); err != nil || !gone {
			return false, err
		}
		return true, a.setPhase(ctx, allocation, fleetv1.AllocationReleased)
	}
	if _, err := a.releaseBootAddresses(ctx, allocation, false); err != nil {
		return false, err
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

// releaseBootAddresses releases the boot addresses, then detaches the boot
// NICs, of the allocation's ended operations, each with its requester's
// client and through the source of its claim's kind, and reports whether
// none remain of any kind. An operation's claims count as gone only once
// both are verified. The operations come from the claims that remain and
// from the allocation's record of the claims it made, so a release is
// verified again after its claim, or its operation, is gone; a record is
// removed only once its release is verified. A held operation keeps its claim, because a silent DHCP
// instance may still serve its address, and so does an operation not yet
// created. The hold names the held operation, and, after a retry that also
// ended unknown, the original operation as its retry name; both keep their
// claims. Custody is read after the operation, because the coordinator writes
// a hold before it copies OutcomeUnknown to the operation. With all, it
// deletes every claim: the Device left the allocation, so no operation of it
// is held or can still be created.
func (a *Allocator) releaseBootAddresses(ctx context.Context, allocation *fleetv1.MachineAllocation, all bool) (bool, error) {
	none := true
	var verified []fleetv1.BootClaimRecord
	type requester struct {
		claimer  bootaddress.Source
		implicit bool
		api      client.Client
		kind     string
		new      func() ops.Operation
	}
	var pairs []requester
	for _, known := range a.releaseSources(a.Provider, "DeviceInstallation") {
		pairs = append(pairs, requester{known.source, known.implicit, a.Provider, "DeviceInstallation", func() ops.Operation { return &ops.DeviceInstallation{} }})
	}
	for _, known := range a.releaseSources(a.Releaser, "DeviceWipe") {
		pairs = append(pairs, requester{known.source, known.implicit, a.Releaser, "DeviceWipe", func() ops.Operation { return &ops.DeviceWipe{} }})
	}
	for _, pair := range pairs {
		operations, err := pair.claimer.Operations(ctx, allocation.Name)
		if pair.implicit && absentAPI(err) {
			// No IPAM API, so no IPAM claim to release.
			continue
		}
		if err != nil {
			return false, err
		}
		for _, name := range recordedBootClaims(allocation, pair.kind, pair.claimer.ClaimKind()) {
			if !slices.Contains(operations, name) {
				operations = append(operations, name)
			}
		}
		for _, name := range operations {
			if !all {
				operation := pair.new()
				if err := pair.api.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, operation); apierrors.IsNotFound(err) {
					none = false
					continue
				} else if err != nil {
					return false, err
				}
				if !operation.OperationStatus().Phase.Ended() {
					none = false
					continue
				}
				held, err := a.held(ctx, allocation, name)
				if err != nil {
					return false, err
				}
				if held {
					none = false
					continue
				}
			}
			released, err := pair.claimer.Release(ctx, name)
			if err != nil {
				return false, err
			}
			if !released {
				none = false
				continue
			}
			detached, err := pair.claimer.Detach(ctx, name)
			if err != nil {
				return false, err
			}
			none = none && detached
			if detached {
				claimKind := pair.claimer.ClaimKind()
				verified = append(verified, fleetv1.BootClaimRecord{OperationKind: pair.kind, Operation: name,
					Claim: fleetv1.TypedReference{APIGroup: claimKind.Group, Kind: claimKind.Kind, Name: bootaddress.ClaimName(name)}})
			}
		}
	}
	if kept := slices.DeleteFunc(slices.Clone(allocation.Status.BootClaims), func(record fleetv1.BootClaimRecord) bool {
		return slices.Contains(verified, record)
	}); len(kept) != len(allocation.Status.BootClaims) {
		allocation.Status.BootClaims = kept
		if err := a.Provider.Status().Update(ctx, allocation); err != nil {
			return false, err
		}
	}
	return none, nil
}

// held reports whether custody's recovery hold names the operation, as the
// held operation or as the original of a held retry.
func (a *Allocator) held(ctx context.Context, allocation *fleetv1.MachineAllocation, operation string) (bool, error) {
	state := &ops.DeviceCustody{}
	if err := a.Releaser.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: allocation.Spec.DeviceRef.Name}, state); err != nil {
		return false, err
	}
	hold := state.Spec.RecoveryHold
	return hold != nil && (hold.Name == operation || hold.RetryName == operation), nil
}

func (a *Allocator) releaseQuota(ctx context.Context, _ *fleetv1.MachineAllocation, machine Machine) error {
	if a.Quota == nil {
		return nil
	}
	return a.Quota.Release(ctx, machine)
}

// ReleaseMachine releases whatever a deleted Machine holds and reports
// whether nothing remains. A Machine deleted before its allocation existed,
// such as one still waiting for capacity, may hold a quota claim; it is
// released by the Machine's name for it.
func (a *Allocator) ReleaseMachine(ctx context.Context, machine Machine) (bool, error) {
	current, err := a.Get(ctx, machine)
	if err != nil {
		return false, err
	}
	if current == nil {
		return true, a.releaseQuota(ctx, nil, machine)
	}
	return a.Release(ctx, current, machine)
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
	if err := a.Releaser.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: reference.Name}, &ops.DeviceWipe{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	network, err := a.bootNetwork(ctx, a.wipeClaims(), allocation, "DeviceWipe", reference.Name)
	if err != nil {
		return err
	}
	wipe := &ops.DeviceWipe{
		ObjectMeta: metav1.ObjectMeta{Name: reference.Name, Namespace: a.Namespace},
		Spec: ops.DeviceWipeSpec{
			OperationEnvelope:    a.envelope(allocation, a.ReleaseCreator),
			Step:                 reference.Step,
			CleaningPolicyDigest: allocation.Spec.CleaningPolicyDigest,
			TargetDisks:          slices.Clone(allocation.Spec.TargetDisks),
			BootInterface:        allocation.Spec.BootInterface,
			BootNetwork:          network,
		},
	}
	return a.createOnce(ctx, a.Releaser, wipe, &ops.DeviceWipe{})
}
