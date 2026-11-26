package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

// PowerProjector is the only writer of Machine status.power. It reads the
// custody of the Machine's allocation in the provider project: while a
// recovery hold exists, or custody is unverified, power is Unknown, because
// nothing proves what the server did. Otherwise it is the newer of the power
// state the Device's last discovery observed and the state custody's last
// succeeded power operation proved. When an operation that may have changed
// power, other than a recovery, finished after discovery's read and proved no
// power state, power is Unknown until discovery reads it again. A Machine
// without an active allocation reports no power.
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
	consumer := ProjectConsumer(p.Project, p.Client)
	machine := allocation.Machine{Consumer: consumer.Ref, ConsumerUID: consumer.UID, Namespace: request.Namespace, Name: request.Name, UID: request.UID}
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
	if current.Labels[fleetv1.LabelMachineUID] != string(machine.UID) || current.Labels[fleetv1.LabelConsumerUID] != string(machine.ConsumerUID) || current.Status.Phase != fleetv1.AllocationActive {
		return nil, nil
	}
	state := &ops.DeviceCustody{}
	if err := provider.Get(ctx, client.ObjectKey{Namespace: namespace, Name: current.Spec.DeviceRef.Name}, state); apierrors.IsNotFound(err) {
		return unknown, nil
	} else if err != nil {
		return nil, err
	}
	if state.Spec.RecoveryHold != nil || state.Spec.State != ops.CustodyVerified || state.Labels[ops.LabelDeviceUID] != current.Labels[fleetv1.LabelDeviceUID] {
		return unknown, nil
	}
	device := &inventory.Device{}
	if err := provider.Get(ctx, client.ObjectKey{Namespace: namespace, Name: current.Spec.DeviceRef.Name}, device); apierrors.IsNotFound(err) {
		return unknown, nil
	} else if err != nil {
		return nil, err
	}
	if device.UID != types.UID(current.Labels[fleetv1.LabelDeviceUID]) {
		return unknown, nil
	}
	var observed metav1.Time
	result := unknown
	if discovery := device.Status.Discovery; discovery != nil && discovery.Inventory != nil {
		for _, system := range discovery.Inventory.Systems {
			if strings.EqualFold(system.UUID, device.Spec.Identity.SystemUUID) {
				switch system.PowerState {
				case "On", "Off":
					observed, result = discovery.Inventory.ObservedAt, &fleetv1.PowerStatus{State: system.PowerState}
				}
			}
		}
	}
	// A tie favors the operation's proven state: a discovery read stamped in
	// the same second cannot be shown to be later.
	if after := provenPower(state); after != nil && (result == unknown || !after.ObservedAt.Before(&observed)) {
		return &fleetv1.PowerStatus{State: string(after.State)}, nil
	}
	// An operation that finished after discovery's read, and proved no power
	// state, may have changed power since: discovery no longer shows it. A
	// tie in the one-second timestamps cannot be ordered, so it counts too.
	if last := state.Spec.LastResult; last != nil && last.Kind != "DeviceRecovery" && !last.FinishedAt.Before(&observed) {
		return unknown, nil
	}
	return result, nil
}

// provenPower returns the power state custody's last result proved, when
// that result is a succeeded power operation with a settled state.
func provenPower(state *ops.DeviceCustody) *ops.PowerObservation {
	last := state.Spec.LastResult
	if last == nil || last.Kind != "DevicePowerOperation" || last.Phase != ops.PhaseSucceeded || last.Evidence == nil {
		return nil
	}
	var evidence ops.PowerEvidence
	if json.Unmarshal(last.Evidence.Raw, &evidence) != nil || evidence.Power == nil || evidence.Power.After == nil || !evidence.Power.After.State.Settled() {
		return nil
	}
	return evidence.Power.After
}

// Chain runs projectors in order and stops at the first that must retry.
func Chain(handlers ...Handler) Handler {
	return HandlerFunc(func(ctx context.Context, request ProjectRequest) Outcome {
		for _, handler := range handlers {
			if outcome := handler.ReconcileEvent(ctx, request); outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineUnchanged {
				return outcome
			}
		}
		return Outcome{}
	})
}
