// Package requestedhardware owns the only status projection that writes
// Machine status.requestedHardware and the RequestResolved condition.
//
// Both writers use it: the fleet-manager's in-process projector for its one
// configured project, and the multi-project fleet-request-resolver. They must
// never serve the same project; see docs/enhancements/machine-lifecycle.
//
// The projection reads the Machine and its MachineProfile and performs no
// allocation, provider, quota, or private-record effect.
package requestedhardware

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

// Project resolves and pins the requested hardware of the Machine named by
// key, reading and writing through c.
//
// The first successful resolution accepts the request and pins the snapshot.
// Later calls never re-resolve it, so profile edits, deletion, or same-name
// recreation cannot change what the Machine reports it requested. Until then,
// a missing, deleting, or inexact profile leaves the snapshot absent and sets
// RequestResolved=False with an explicit reason; no value is defaulted.
//
// When uid is non-empty, only that Machine lifetime is projected; a missing,
// replaced, or deleting Machine is a no-op. A returned error is an unknown
// result (a failed read or a conflicting write) and must be retried; it is
// never converted into an unresolved condition.
func Project(ctx context.Context, c client.Client, key client.ObjectKey, uid types.UID) error {
	if c == nil || key.Namespace == "" || key.Name == "" {
		return errors.New("requested-hardware projection requires a client and a namespaced Machine key")
	}
	var machine fleetv1.Machine
	if err := c.Get(ctx, key, &machine); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if (uid != "" && machine.UID != uid) || machine.DeletionTimestamp != nil {
		return nil
	}

	snapshot := machine.Status.RequestedHardware
	var resolved metav1.Condition
	if snapshot != nil {
		resolved = condition(machine.Generation, metav1.ConditionTrue, controller.ReasonRequestResolved, pinnedMessage(snapshot))
	} else {
		var err error
		snapshot, resolved, err = resolve(ctx, c, &machine)
		if err != nil {
			return err
		}
	}

	newlyPinned := machine.Status.RequestedHardware == nil && snapshot != nil
	if !newlyPinned && sameCondition(apiMeta.FindStatusCondition(machine.Status.Conditions, resolved.Type), resolved) {
		return nil
	}
	updated := machine.DeepCopyObject().(*fleetv1.Machine)
	if newlyPinned {
		updated.Status.RequestedHardware = snapshot
	}
	apiMeta.SetStatusCondition(&updated.Status.Conditions, resolved)
	// Update carries the read resourceVersion, so a concurrent status writer
	// produces a conflict and a retry from fresh state instead of a lost write.
	return c.Status().Update(ctx, updated)
}

func resolve(ctx context.Context, c client.Client, machine *fleetv1.Machine) (*fleetv1.RequestedHardwareStatus, metav1.Condition, error) {
	name := machine.Spec.MachineProfileRef.Name
	if name == "" {
		return nil, condition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileNotFound, "spec.machineProfileRef.name is empty."), nil
	}
	var profile fleetv1.MachineProfile
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &profile); apierrors.IsNotFound(err) {
		return nil, condition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileNotFound, fmt.Sprintf("MachineProfile %q was not found.", name)), nil
	} else if err != nil {
		// An unknown read result is retried, never reported as a missing profile.
		return nil, metav1.Condition{}, err
	}
	if profile.DeletionTimestamp != nil {
		return nil, condition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileDeleting, fmt.Sprintf("MachineProfile %q is being deleted.", name)), nil
	}
	snapshot, err := controller.ResolveRequestedHardware(machine, &profile)
	if errors.Is(err, controller.ErrProfileNotExact) {
		return nil, condition(machine.Generation, metav1.ConditionFalse, controller.ReasonProfileInvalid, fmt.Sprintf("MachineProfile %q: %v.", name, err)), nil
	}
	if err != nil {
		return nil, metav1.Condition{}, err
	}
	return snapshot, condition(machine.Generation, metav1.ConditionTrue, controller.ReasonRequestResolved, pinnedMessage(snapshot)), nil
}

func condition(generation int64, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: controller.ConditionRequestResolved, Status: status, ObservedGeneration: generation, Reason: reason, Message: message}
}

func pinnedMessage(snapshot *fleetv1.RequestedHardwareStatus) string {
	return fmt.Sprintf("Requested hardware is pinned from MachineProfile %q generation %d at Machine generation %d.", snapshot.Source.ProfileRef.Name, snapshot.Source.ProfileGeneration, snapshot.Source.MachineGeneration)
}

func sameCondition(current *metav1.Condition, want metav1.Condition) bool {
	return current != nil && current.Status == want.Status && current.Reason == want.Reason && current.Message == want.Message && current.ObservedGeneration == want.ObservedGeneration
}
