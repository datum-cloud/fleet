package allocation

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// PowerAction maps a Machine's powerState to the power action that applies
// it, as #173 defines: On is On, and Off is ForceOff.
func PowerAction(state string) (ops.PowerAction, error) {
	switch state {
	case "On":
		return ops.PowerOn, nil
	case "Off":
		return ops.PowerForceOff, nil
	}
	return "", fmt.Errorf("%w: powerState %q is neither On nor Off", ErrUnsupported, state)
}

// Power creates the DevicePowerOperation that applies a Machine's power
// intent, or returns nil when none is due. Owner is the Machine's identity,
// whose allocation is the one given. It is create-only: a retry finds
// the same operation by name.
//
// An operation is named from the allocation and the Machine generation, so
// a refused one is not created again until the generation changes. It is
// due only when powerState differs from the Machine's observed power, which
// the power projector alone writes, or when a request for another action has
// not run yet. Power not yet observed, or Unknown, differs from nothing:
// every Machine defaults to Off, and a server is not forced off before its
// power is known.
//
// While a power hold on this allocation exists, observed power is Unknown.
// Power then creates nothing until staff apply a DeviceRecovery Retry, and
// creates the retry only while powerState still maps to the held action.
// Otherwise staff settle the hold, and the newer operation runs after it.
func (a *Allocator) Power(ctx context.Context, allocation *fleetv1.MachineAllocation, owner Machine, machine *fleetv1.Machine) (*ops.DevicePowerOperation, error) {
	if allocation.Status.Phase != fleetv1.AllocationActive {
		return nil, ErrReleased
	}
	// The allocation belongs to this Machine only if its name is the one the
	// Machine's incarnation derives, and it records the same Machine and
	// consumer. A same-name Machine never inherits it.
	if owner.UID != machine.UID || owner.Namespace != machine.Namespace || owner.Name != machine.Name || allocation.Name != Name(owner) ||
		allocation.Spec.ConsumerRef != owner.Consumer || allocation.Spec.MachineRef != owner.reference() ||
		allocation.Labels[fleetv1.LabelMachineUID] != string(owner.UID) || allocation.Labels[fleetv1.LabelConsumerUID] != string(owner.ConsumerUID) {
		return nil, ErrConflict
	}
	if machine.Spec.PowerState == "" {
		return nil, nil
	}
	action, err := PowerAction(machine.Spec.PowerState)
	if err != nil {
		return nil, err
	}
	state := &ops.DeviceCustody{}
	if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: allocation.Spec.DeviceRef.Name}, state); err != nil {
		return nil, err
	}
	operation := &ops.DevicePowerOperation{
		ObjectMeta: metav1.ObjectMeta{Name: custody.PowerName(allocation.Name, machine.Generation), Namespace: a.Namespace, Labels: operationLabels(allocation)},
		Spec: ops.DevicePowerOperationSpec{
			OperationEnvelope: a.envelope(allocation, a.ProviderCreator),
			Action:            action,
			MachineGeneration: machine.Generation,
		},
	}
	if hold := state.Spec.RecoveryHold; hold != nil && hold.Kind == "DevicePowerOperation" {
		held := &ops.DevicePowerOperation{}
		if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: hold.Name}, held); apierrors.IsNotFound(err) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		reference := held.Spec.Authority.AllocationRef
		if reference == nil || reference.Name != allocation.Name || hold.RetryAuthorizedBy == nil || held.Spec.Action != action {
			return nil, nil
		}
		// The retry repeats the held operation's inputs under the
		// foundation's retry name.
		operation.Name = custody.RetryName(*hold, hold.Retries)
		operation.Spec = held.Spec
		return operation, a.createOnce(ctx, a.Provider, operation, &ops.DevicePowerOperation{})
	}
	observed := machine.Status.Power
	if observed == nil || (observed.State != "On" && observed.State != "Off") {
		return nil, nil
	}
	if observed.State == machine.Spec.PowerState {
		// A request for another action that has not run yet would undo the
		// intent the Machine now states, so a newer one replaces it.
		pending, err := a.pendingPower(ctx, allocation)
		if err != nil || pending == nil || pending.Spec.Action == action {
			return nil, err
		}
	}
	return operation, a.createOnce(ctx, a.Provider, operation, &ops.DevicePowerOperation{})
}

// pendingPower returns this allocation's newest power request that custody
// has not ended, or nil.
func (a *Allocator) pendingPower(ctx context.Context, allocation *fleetv1.MachineAllocation) (*ops.DevicePowerOperation, error) {
	var list ops.DevicePowerOperationList
	if err := a.Provider.List(ctx, &list, client.InNamespace(a.Namespace)); err != nil {
		return nil, err
	}
	var newest *ops.DevicePowerOperation
	for index := range list.Items {
		operation := &list.Items[index]
		reference := operation.Spec.Authority.AllocationRef
		if reference == nil || reference.Name != allocation.Name || operation.Status.Phase.Ended() {
			continue
		}
		if newest == nil || newest.Spec.MachineGeneration < operation.Spec.MachineGeneration {
			newest = operation
		}
	}
	return newest, nil
}
