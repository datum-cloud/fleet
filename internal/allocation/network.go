package allocation

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/network"
	"go.datum.net/fleet/internal/network/nso"
)

// Networks is the network owner's attachment contract.
type Networks interface {
	Check(ctx context.Context, machine *fleetv1.Machine, principal string) error
	Ensure(ctx context.Context, allocation *fleetv1.MachineAllocation, machine *fleetv1.Machine, request fleetv1.NetworkInterfaceRequest, recorded fleetv1.AllocatedInterface) (types.UID, nso.Observation, error)
	Release(ctx context.Context, claim fleetv1.ClaimReference) (bool, error)
}

var _ Networks = nso.Adapter{}

// CheckNetwork admits a Machine's interfaces before allocation, reserving
// nothing. A Machine without interfaces needs no network owner.
func (a *Allocator) CheckNetwork(ctx context.Context, machine *fleetv1.Machine, principal string) error {
	if len(machine.Spec.NetworkInterfaces) == 0 {
		return nil
	}
	if a.Network == nil {
		return network.ErrUnsupported
	}
	return a.Network.Check(ctx, machine, principal)
}

// Attach asks the network owner for each of the Machine's interfaces on its
// allocated Device and returns the Machine's network status, and whether
// every interface is ready. The allocation records each interface's
// attachment point and claim name before the claim is created, and the
// claim's UID after, so a crash between the writes is recovered by name.
func (a *Allocator) Attach(ctx context.Context, allocation *fleetv1.MachineAllocation, machine *fleetv1.Machine) (*fleetv1.NetworkStatus, bool, error) {
	if len(machine.Spec.NetworkInterfaces) == 0 {
		return &fleetv1.NetworkStatus{}, true, nil
	}
	if a.Network == nil {
		return nil, false, network.ErrUnsupported
	}
	if allocation.Status.Phase != fleetv1.AllocationActive {
		return nil, false, ErrReleased
	}
	recorded := map[string]fleetv1.AllocatedInterface{}
	if allocation.Status.Network != nil {
		for _, iface := range allocation.Status.Network.Interfaces {
			recorded[iface.Name] = iface
		}
	}
	var device *inventory.Device
	changed := false
	for _, request := range machine.Spec.NetworkInterfaces {
		if _, ok := recorded[request.Name]; ok {
			continue
		}
		if device == nil {
			device = &inventory.Device{}
			if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: allocation.Spec.DeviceRef.Name}, device); err != nil {
				return nil, false, err
			}
			if string(device.UID) != allocation.Spec.DeviceRef.UID {
				return nil, false, fmt.Errorf("%w: the Device is another incarnation", ErrCustodyLost)
			}
		}
		point, err := network.Locate(ctx, a.Provider, device, request.Name)
		if err != nil {
			return nil, false, err
		}
		reference := nso.Reference(machine, request)
		recorded[request.Name] = fleetv1.AllocatedInterface{Name: request.Name, Network: reference, Attachment: point,
			Claim: fleetv1.ClaimReference{Namespace: reference.Namespace, Name: nso.ClaimName(allocation.Name, request.Name)}}
		changed = true
	}
	if changed {
		if err := a.recordNetwork(ctx, allocation, machine, recorded); err != nil {
			return nil, false, err
		}
	}
	status := &fleetv1.NetworkStatus{}
	ready, changed := true, false
	for _, request := range machine.Spec.NetworkInterfaces {
		current := recorded[request.Name]
		if current.Network != nso.Reference(machine, request) {
			return nil, false, fmt.Errorf("%w: interface %s names another Network than the allocation recorded", network.ErrConflict, request.Name)
		}
		uid, observation, err := a.Network.Ensure(ctx, allocation, machine, request, current)
		if err != nil {
			// Keep the UIDs already proven, so those claims are never
			// adopted by name again.
			if changed {
				if recordErr := a.recordNetwork(ctx, allocation, machine, recorded); recordErr != nil {
					return nil, false, recordErr
				}
			}
			return nil, false, err
		}
		if current.Claim.UID == "" {
			current.Claim.UID = string(uid)
			recorded[request.Name] = current
			changed = true
		}
		status.Interfaces = append(status.Interfaces, observation.Status)
		ready = ready && observation.Ready
	}
	if changed {
		if err := a.recordNetwork(ctx, allocation, machine, recorded); err != nil {
			return nil, false, err
		}
	}
	return status, ready, nil
}

// recordNetwork writes the allocation's interfaces in the Machine's order,
// conditional on the allocation read.
func (a *Allocator) recordNetwork(ctx context.Context, allocation *fleetv1.MachineAllocation, machine *fleetv1.Machine, recorded map[string]fleetv1.AllocatedInterface) error {
	record := &fleetv1.AllocationNetwork{}
	for _, request := range machine.Spec.NetworkInterfaces {
		record.Interfaces = append(record.Interfaces, recorded[request.Name])
	}
	allocation.Status.Network = record
	return a.Provider.Status().Update(ctx, allocation)
}

// releaseNetwork deletes every recorded interface claim and reports whether
// all are gone. The network owner keeps a claim until it detached the port.
func (a *Allocator) releaseNetwork(ctx context.Context, allocation *fleetv1.MachineAllocation) (bool, error) {
	if allocation.Status.Network == nil || len(allocation.Status.Network.Interfaces) == 0 {
		return true, nil
	}
	if a.Network == nil {
		return false, network.ErrUnsupported
	}
	gone := true
	for _, iface := range allocation.Status.Network.Interfaces {
		released, err := a.Network.Release(ctx, iface.Claim)
		if err != nil {
			return false, err
		}
		gone = gone && released
	}
	return gone, nil
}

// NetworkCondition is the Machine's NetworkReady condition for an attach
// result.
func NetworkCondition(machine *fleetv1.Machine, ready bool, err error) metav1.Condition {
	condition := metav1.Condition{Type: "NetworkReady", ObservedGeneration: machine.Generation, LastTransitionTime: metav1.Now()}
	switch {
	case err == nil && ready:
		condition.Status, condition.Reason, condition.Message = metav1.ConditionTrue, "Programmed", "Every interface is allocated, prepared and programmed."
	case err == nil:
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "NetworkPending", "The network owner has not programmed every interface."
	case errors.Is(err, network.ErrUnsupported):
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "NetworkAttachmentUnsupported", "Physical network attachment is not available at this location."
	case errors.Is(err, network.ErrUnauthorized):
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "NetworkUseNotAuthorized", "The Machine's creator may not use a requested Network."
	case errors.Is(err, network.ErrNotReady):
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "NetworkNotReady", "A requested Network is missing, not ready, or does not carry a requested address family."
	case errors.Is(err, network.ErrAttachmentUnknown):
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "AttachmentPointUnknown", "Inventory does not say where an interface is cabled."
	case errors.Is(err, network.ErrConflict):
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "NetworkConfigurationChanged", "An interface no longer matches what Fleet attached."
	default:
		condition.Status, condition.Reason, condition.Message = metav1.ConditionUnknown, "NetworkUnavailable", "The network owner could not be read."
	}
	return condition
}
