package allocation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/cleaning"
	"go.datum.net/fleet/internal/custody"
)

// maxWipeSteps bounds the wipes one release creates after wipes that ended
// Failed, which sent nothing. A wipe whose outcome is unknown is never
// repeated without a DeviceRecovery Retry. A wipe refused because discovery
// changed between planning and admission (PlanMismatch) does not count
// toward it, but every release stops at maxPlanSteps steps in all.
const (
	maxWipeSteps = 3
	maxPlanSteps = 16
)

// ConditionCleaning is the MachineAllocation condition the release
// controller sets False while no wipe can be planned, with the reason.
const ConditionCleaning = "Cleaning"

// ErrWipeFailed means the release created its last wipe step and it failed.
// The Cleaning gate stays open, and the Device stays in Release.
var ErrWipeFailed = errors.New("every wipe step of the release failed")

// ErrCleaningBlocked means a drive cannot be cleaned now: discovery does not
// report a pinned drive, or reports one no method covers. For the
// allocator's CleaningGrace no wipe is created, so a brief gap in discovery
// spends no wipe step; after it the wipe is created, and admission holds it
// for staff to settle with DisposeDrive.
var ErrCleaningBlocked = errors.New("a drive cannot be cleaned")

// DefaultCleaningGrace is how long release waits for a drive it cannot clean
// to reappear before it holds the server for staff.
const DefaultCleaningGrace = 10 * time.Minute

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
		return a.startWipe(ctx, allocation, fleetv1.WipeReference{Name: custody.WipeName(allocation.Name, 1), Step: 1})
	}
	wipe := &ops.DeviceWipe{}
	if err := a.Releaser.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: current.Name}, wipe); apierrors.IsNotFound(err) {
		// The status was written but the create was lost.
		return a.startWipe(ctx, allocation, *current)
	} else if err != nil {
		return false, err
	}
	switch wipe.Status.Phase {
	case ops.PhaseSucceeded:
		return true, nil
	case ops.PhaseFailed, ops.PhaseCancelled:
		failed, err := a.failedSteps(ctx, allocation.Name, current.Step)
		if err != nil {
			return false, err
		}
		if failed >= maxWipeSteps || current.Step >= maxPlanSteps {
			return false, fmt.Errorf("%w: %s: %s", ErrWipeFailed, wipe.Status.Reason, wipe.Status.Message)
		}
		step := current.Step + 1
		return a.startWipe(ctx, allocation, fleetv1.WipeReference{Name: custody.WipeName(allocation.Name, step), Step: step})
	case ops.PhaseOutcomeUnknown:
		hold := state.Spec.RecoveryHold
		drives := wipe.Status.Reason == custody.ReasonDriveMissing || wipe.Status.Reason == custody.ReasonDriveUncovered
		// A drive hold is settled by DisposeDrive, never by a Retry, even
		// when it carries the lineage of a retried wipe.
		if !drives && hold != nil && hold.Kind == "DeviceWipe" && hold.Name == current.Name && hold.RetryName != "" {
			return a.startWipe(ctx, allocation, fleetv1.WipeReference{Name: hold.RetryName, Step: current.Step + 1})
		}
		// A wipe held for a drive it could not clean, whose hold staff
		// settled with DisposeDrive: plan the next step without that drive.
		held := hold != nil && hold.Kind == "DeviceWipe" && hold.Name == current.Name
		if !held && drives {
			if current.Step >= maxPlanSteps {
				return false, fmt.Errorf("%w: %s", ErrWipeFailed, wipe.Status.Reason)
			}
			// Restart the grace: another drive that cannot be cleaned now
			// gets its own wait before it holds the server again.
			if apiMeta.RemoveStatusCondition(&allocation.Status.Conditions, ConditionCleaning) {
				if err := a.Provider.Status().Update(ctx, allocation); err != nil {
					return false, err
				}
			}
			step := current.Step + 1
			return a.startWipe(ctx, allocation, fleetv1.WipeReference{Name: custody.WipeName(allocation.Name, step), Step: step})
		}
	}
	return false, nil
}

// startWipe creates the next wipe and reports whether cleaning is done
// without one: every pinned drive was disposed of, so no drive is left whose
// data a wipe could reach.
func (a *Allocator) startWipe(ctx context.Context, allocation *fleetv1.MachineAllocation, reference fleetv1.WipeReference) (bool, error) {
	err := a.wipe(ctx, allocation, reference)
	if errors.Is(err, errNothingToClean) {
		return true, nil
	}
	return false, err
}

// errNothingToClean means every drive the plan would cover was disposed of.
var errNothingToClean = errors.New("every pinned drive was disposed of")

// failedSteps counts the release's wipes up to step that ended Failed or
// Cancelled for a reason other than PlanMismatch.
func (a *Allocator) failedSteps(ctx context.Context, allocation string, step int32) (int, error) {
	failed := 0
	for n := int32(1); n <= step; n++ {
		wipe := &ops.DeviceWipe{}
		if err := a.Releaser.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: custody.WipeName(allocation, n)}, wipe); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return 0, err
		}
		if (wipe.Status.Phase == ops.PhaseFailed || wipe.Status.Phase == ops.PhaseCancelled) && wipe.Status.Reason != custody.ReasonPlanMismatch {
			failed++
		}
	}
	return failed, nil
}

// wipe records the wipe in status, then creates it, so a crash between the
// two creates the same wipe again. Its plan comes from the shared planner:
// the allocation's pinned policy and manifest, and the drives discovery
// reports now. Until discovery reports the server's drives, no wipe is
// created.
func (a *Allocator) wipe(ctx context.Context, allocation *fleetv1.MachineAllocation, reference fleetv1.WipeReference) error {
	device := &inventory.Device{}
	if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: allocation.Spec.DeviceRef.Name}, device); err != nil {
		return err
	}
	// The allocation's device-uid label records the incarnation it holds;
	// a Device recreated under the same name is not that server.
	if string(device.UID) != allocation.Labels[fleetv1.LabelDeviceUID] {
		return fmt.Errorf("%w: the Device is another incarnation", ErrCustodyLost)
	}
	state := &ops.DeviceCustody{}
	if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: allocation.Spec.DeviceRef.Name}, state); err != nil {
		return err
	}
	pin := allocation.Spec.CleaningPolicy
	plan, err := cleaning.Plan(pin.Spec, allocation.Spec.Drives, cleaning.StorageOf(device), custody.DisposedDriveNames(state))
	if err != nil {
		return fmt.Errorf("plan the wipe: %w", err)
	}
	if len(plan) == 0 {
		return errNothingToClean
	}
	if blocked := blockedReason(plan); blocked != nil {
		// The grace runs on the allocator's clock from when the plan first
		// blocked.
		blocked.LastTransitionTime = metav1.NewTime(a.now())
		if changed := apiMeta.SetStatusCondition(&allocation.Status.Conditions, *blocked); changed {
			if err := a.Provider.Status().Update(ctx, allocation); err != nil {
				return err
			}
		}
		since := apiMeta.FindStatusCondition(allocation.Status.Conditions, ConditionCleaning).LastTransitionTime.Time
		if a.now().Sub(since) < a.cleaningGrace() {
			return fmt.Errorf("%w: %s", ErrCleaningBlocked, blocked.Message)
		}
		// The drive has stayed uncleanable past the grace: create the wipe,
		// and admission holds the server for staff.
	} else if apiMeta.RemoveStatusCondition(&allocation.Status.Conditions, ConditionCleaning) {
		if err := a.Provider.Status().Update(ctx, allocation); err != nil {
			return err
		}
	}
	if current := allocation.Status.Wipe; current == nil || *current != reference {
		allocation.Status.Wipe = &reference
		if err := a.Provider.Status().Update(ctx, allocation); err != nil {
			return err
		}
	}
	wipe := &ops.DeviceWipe{
		ObjectMeta: metav1.ObjectMeta{Name: reference.Name, Namespace: a.Namespace, Labels: operationLabels(allocation)},
		Spec: ops.DeviceWipeSpec{
			OperationEnvelope: a.envelope(allocation, a.ReleaseCreator),
			Step:              reference.Step,
			Policy:            pin.CleaningPolicyReference,
			Assurance:         pin.Spec.Assurance,
			Drives:            plan,
		},
	}
	err = a.createOnce(ctx, a.Releaser, wipe, &ops.DeviceWipe{})
	if errors.Is(err, ErrConflict) {
		// A wipe with this name exists with another plan: a crash lost the
		// response, and discovery changed before the retry. Admission plans
		// that wipe again, so it stands.
		return nil
	}
	return err
}

// blockedReason is the Cleaning condition for a plan with a drive that
// cannot be cleaned, or nil.
func blockedReason(plan []ops.PlannedDrive) *metav1.Condition {
	for _, d := range plan {
		reason := ""
		switch d.State {
		case ops.DriveMissing:
			reason = custody.ReasonDriveMissing
		case ops.DriveUncovered:
			reason = custody.ReasonDriveUncovered
		default:
			continue
		}
		return &metav1.Condition{Type: ConditionCleaning, Status: metav1.ConditionFalse, Reason: reason,
			Message: fmt.Sprintf("drive %s (%s) cannot be cleaned", d.Name, d.SerialNumber)}
	}
	return nil
}

func (a *Allocator) cleaningGrace() time.Duration {
	if a.CleaningGrace != nil {
		return *a.CleaningGrace
	}
	return DefaultCleaningGrace
}

func (a *Allocator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
