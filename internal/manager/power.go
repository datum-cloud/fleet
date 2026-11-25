package manager

import (
	"context"
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/allocation"
	"go.datum.net/fleet/internal/controller"
)

// PowerProjector is the only writer of Machine status.power. It reads the
// custody of the Machine's allocation in the provider project: while a
// recovery hold exists, or custody is unverified, power is Unknown, because
// nothing proves what the server did. Otherwise it is the power state the
// Device's last discovery observed. A Machine without an active allocation
// reports no power.
type PowerProjector struct {
	Client  client.Client
	Project controller.ProjectIdentity
	// Provider reads the provider project. It must read through.
	Provider client.Reader
	// Namespace is the Devices' namespace in the provider project.
	Namespace string
}

func (p PowerProjector) ReconcileEvent(ctx context.Context, request ProjectRequest) Outcome {
	if p.Client == nil || p.Provider == nil || !p.Project.Valid() || !request.Valid() || !request.SameProject(p.Project.AuthorityID, p.Project.UID) {
		return Outcome{RetryError: ErrEventWiringUnavailable}
	}
	machine := allocation.Machine{Project: p.Project.RouteName, ProjectUID: p.Project.UID, Namespace: request.Namespace, Name: request.Name, UID: request.UID}
	if err := ProjectPower(ctx, p.Client, p.Provider, p.Namespace, machine); err != nil {
		return Outcome{RetryError: err}
	}
	return Outcome{}
}

// ProjectPower writes one Machine's status.power. The harness runners call
// it for their fixtures, so this stays the only power writer.
func ProjectPower(ctx context.Context, consumer client.Client, provider client.Reader, namespace string, machine allocation.Machine) error {
	if consumer == nil || provider == nil || namespace == "" || !machine.Valid() {
		return errors.New("power projection requires both projects and an exact Machine")
	}
	var current fleetv1.Machine
	if err := consumer.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: machine.Name}, &current); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if current.UID != machine.UID {
		return nil
	}
	want, err := power(ctx, provider, namespace, machine)
	if err != nil {
		return err
	}
	if samePower(current.Status.Power, want) {
		return nil
	}
	updated := current.DeepCopyObject().(*fleetv1.Machine)
	updated.Status.Power = want
	return consumer.Status().Update(ctx, updated)
}

func samePower(a, b *fleetv1.PowerStatus) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

var unknown = &fleetv1.PowerStatus{State: "Unknown"}

func power(ctx context.Context, provider client.Reader, namespace string, machine allocation.Machine) (*fleetv1.PowerStatus, error) {
	current := &fleetv1.MachineAllocation{}
	if err := provider.Get(ctx, client.ObjectKey{Namespace: namespace, Name: allocation.Name(machine)}, current); apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if current.Spec.MachineRef.UID != string(machine.UID) || current.Spec.MachineRef.ProjectUID != string(machine.ProjectUID) || current.Status.Phase != fleetv1.AllocationActive {
		return nil, nil
	}
	state := &ops.DeviceCustody{}
	if err := provider.Get(ctx, client.ObjectKey{Namespace: namespace, Name: current.Spec.DeviceRef.Name}, state); apierrors.IsNotFound(err) {
		return unknown, nil
	} else if err != nil {
		return nil, err
	}
	if state.Spec.RecoveryHold != nil || state.Spec.State != ops.CustodyVerified || state.Spec.DeviceRef.UID != current.Spec.DeviceRef.UID {
		return unknown, nil
	}
	device := &inventory.Device{}
	if err := provider.Get(ctx, client.ObjectKey{Namespace: namespace, Name: current.Spec.DeviceRef.Name}, device); apierrors.IsNotFound(err) {
		return unknown, nil
	} else if err != nil {
		return nil, err
	}
	if device.UID != types.UID(current.Spec.DeviceRef.UID) || device.Status.Discovery == nil || device.Status.Discovery.Inventory == nil {
		return unknown, nil
	}
	for _, system := range device.Status.Discovery.Inventory.Systems {
		if strings.EqualFold(system.UUID, device.Spec.Identity.SystemUUID) {
			switch system.PowerState {
			case "On", "Off":
				return &fleetv1.PowerStatus{State: system.PowerState}, nil
			}
		}
	}
	return unknown, nil
}
