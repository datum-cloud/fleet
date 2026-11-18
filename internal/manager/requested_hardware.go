package manager

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

// RequestedHardwareProjector is the sole writer of Machine
// status.requestedHardware and the RequestResolved condition. It needs only
// the routed project API: it reads the Machine and its MachineProfile and
// performs no allocation, provider, quota, or private-record effect, so it runs
// in the default inert manager mode as well as synthetic staging.
//
// The first successful resolution accepts the request and pins the snapshot.
// Later events never re-resolve it, so profile edits, deletion, or same-name
// recreation cannot change what the Machine reports it requested. Until then,
// a missing, deleting, or inexact profile leaves the snapshot absent and sets
// RequestResolved=False with an explicit reason; no value is defaulted.
type RequestedHardwareProjector struct {
	Client  client.Client
	Project controller.ProjectIdentity
}

func (p RequestedHardwareProjector) ReconcileEvent(ctx context.Context, request ProjectRequest) Outcome {
	if p.Client == nil || !p.Project.Valid() || !request.Valid() || !request.SameProject(p.Project.AuthorityID, p.Project.UID) {
		return Outcome{RetryError: ErrEventWiringUnavailable}
	}
	var machine fleetv1.Machine
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, &machine); apierrors.IsNotFound(err) {
		return Outcome{}
	} else if err != nil {
		return Outcome{RetryError: err}
	}
	if machine.UID != request.UID || machine.DeletionTimestamp != nil {
		return Outcome{}
	}

	snapshot := machine.Status.RequestedHardware
	var resolved metav1.Condition
	if snapshot != nil {
		resolved = requestCondition(machine.Generation, metav1.ConditionTrue, controller.ReasonRequestResolved, pinnedMessage(snapshot))
	} else {
		var err error
		snapshot, resolved, err = p.resolve(ctx, &machine)
		if err != nil {
			return Outcome{RetryError: err}
		}
	}

	newlyPinned := machine.Status.RequestedHardware == nil && snapshot != nil
	if !newlyPinned && sameCondition(apiMeta.FindStatusCondition(machine.Status.Conditions, resolved.Type), resolved) {
		return Outcome{}
	}
	updated := machine.DeepCopyObject().(*fleetv1.Machine)
	if newlyPinned {
		updated.Status.RequestedHardware = snapshot
	}
	apiMeta.SetStatusCondition(&updated.Status.Conditions, resolved)
	// Update carries the read resourceVersion, so a concurrent status writer
	// produces a conflict and a retry from fresh state instead of a lost write.
	if err := p.Client.Status().Update(ctx, updated); err != nil {
		return Outcome{RetryError: err}
	}
	return Outcome{}
}

func (p RequestedHardwareProjector) resolve(ctx context.Context, machine *fleetv1.Machine) (*fleetv1.RequestedHardwareStatus, metav1.Condition, error) {
	name := machine.Spec.MachineProfileRef.Name
	if name == "" {
		return nil, requestCondition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileNotFound, "spec.machineProfileRef.name is empty."), nil
	}
	var profile fleetv1.MachineProfile
	if err := p.Client.Get(ctx, client.ObjectKey{Name: name}, &profile); apierrors.IsNotFound(err) {
		return nil, requestCondition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileNotFound, fmt.Sprintf("MachineProfile %q was not found.", name)), nil
	} else if err != nil {
		// An unknown read result is retried, never reported as a missing profile.
		return nil, metav1.Condition{}, err
	}
	if profile.DeletionTimestamp != nil {
		return nil, requestCondition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileDeleting, fmt.Sprintf("MachineProfile %q is being deleted.", name)), nil
	}
	snapshot, err := controller.ResolveRequestedHardware(machine, &profile)
	if errors.Is(err, controller.ErrProfileNotExact) {
		return nil, requestCondition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileInvalid, fmt.Sprintf("MachineProfile %q: %v.", name, err)), nil
	}
	if err != nil {
		return nil, metav1.Condition{}, err
	}
	return snapshot, requestCondition(machine.Generation, metav1.ConditionTrue, controller.ReasonRequestResolved, pinnedMessage(snapshot)), nil
}

func requestCondition(generation int64, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: controller.ConditionRequestResolved, Status: status, ObservedGeneration: generation, Reason: reason, Message: message}
}

func pinnedMessage(snapshot *fleetv1.RequestedHardwareStatus) string {
	return fmt.Sprintf("Requested hardware is pinned from MachineProfile %q generation %d at Machine generation %d.", snapshot.Source.ProfileRef.Name, snapshot.Source.ProfileGeneration, snapshot.Source.MachineGeneration)
}

func sameCondition(current *metav1.Condition, want metav1.Condition) bool {
	return current != nil && current.Status == want.Status && current.Reason == want.Reason && current.Message == want.Message && current.ObservedGeneration == want.ObservedGeneration
}
