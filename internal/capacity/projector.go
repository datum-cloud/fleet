// Package capacity derives MachineCapacity status from evidence: the
// Device's latest commissioning and current plan, discovery, custody, the
// cleaning policy, and duplicate identities. The Projector is the only
// writer of a capacity's Prepared, Healthy and Available conditions and its
// hardware. The allocator calls Evaluate on live inputs and never reads the
// conditions, so editing status cannot make a server allocatable.
package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RegisteredDeviceUID is the annotation in which registration records the
// Device incarnation a registered capacity was created for. Registration
// admission keeps it unchanged.
const RegisteredDeviceUID = "fleet.datumapis.com/registration-device-uid"

// ErrUnresolved is what a CleaningResolver returns for a name that does not
// resolve. Any other error means the owner could not be reached.
var ErrUnresolved = errors.New("the cleaning policy does not resolve")

// CleaningResolver maps a cleaning policy name in the provider project to its
// current digest. The cleaning policy owner provides it.
type CleaningResolver func(ctx context.Context, name string) (string, error)

// Unresolved is the resolver to use until the cleaning policy owner provides
// one: no name resolves, so no capacity is Available.
func Unresolved(context.Context, string) (string, error) { return "", ErrUnresolved }

// Fixed resolves exactly the given names to their digests. Test modes use it.
func Fixed(digests map[string]string) CleaningResolver {
	return func(_ context.Context, name string) (string, error) {
		if digest, ok := digests[name]; ok && name != "" {
			return digest, nil
		}
		return "", ErrUnresolved
	}
}

// Projector derives and writes capacity status.
type Projector struct {
	// Reader reads the provider project. It must read the stored state.
	Reader client.Reader
	// Writer writes capacity status with the projector's identity. Evaluate
	// does not need it.
	Writer    client.Client
	Namespace string
	Cleaning  CleaningResolver
	// DiscoveryMaxAge bounds how old discovery may be. It defaults to the
	// coordinator's default.
	DiscoveryMaxAge time.Duration
	Now             func() time.Time
}

func (p *Projector) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Evaluate derives the capacity's projection from live inputs.
func (p *Projector) Evaluate(ctx context.Context, capacity *metal.MachineCapacity) (custody.CapacityProjection, error) {
	in, err := p.inputs(ctx, capacity)
	if err != nil {
		return custody.CapacityProjection{}, err
	}
	return custody.ProjectCapacity(in), nil
}

func (p *Projector) inputs(ctx context.Context, capacity *metal.MachineCapacity) (custody.CapacityInputs, error) {
	now := p.now()
	in := custody.CapacityInputs{Generation: capacity.Generation, Now: now, AllocationPolicyEnabled: capacity.Spec.AllocationPolicy == "Enabled" && capacity.DeletionTimestamp.IsZero()}
	resolve := p.Cleaning
	if resolve == nil {
		resolve = Unresolved
	}
	if _, err := resolve(ctx, capacity.Spec.CleaningPolicyRef.Name); err == nil {
		in.Cleaning = custody.CleaningResolved
	} else if errors.Is(err, ErrUnresolved) {
		in.Cleaning = custody.CleaningUnresolved
	} else {
		in.Cleaning = custody.CleaningUnknown
	}
	device := &inventory.Device{}
	if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: capacity.Namespace, Name: capacity.Spec.DeviceRef.Name}, device); apierrors.IsNotFound(err) {
		return in, nil
	} else if err != nil {
		return in, err
	}
	if registered, ok := capacity.Annotations[RegisteredDeviceUID]; ok && registered != string(device.UID) {
		// A Device recreated under the name inherits nothing from capacity
		// registered for the old incarnation.
		in.DeviceReplaced = true
		return in, nil
	}
	if inventory.CapacityBackingError(device) != nil {
		// A server without a hardware ID never backs capacity (#131), so
		// nothing else about it is read.
		in.DeviceUnidentified = true
		return in, nil
	}
	in.DeviceUID = string(device.UID)
	in.DeviceSerial, in.DeviceSystemUUID = device.Spec.Identity.Serial, device.Spec.Identity.SystemUUID
	in.DeviceActive = inventory.LifecycleOf(device) == inventory.DeviceActive && device.DeletionTimestamp.IsZero()
	maxAge := p.DiscoveryMaxAge
	if maxAge <= 0 {
		maxAge = custody.DefaultDiscoveryMaxAge
	}
	in.Discovery.Current = custody.DiscoveryCurrent(device, now, maxAge)
	if discovery := device.Status.Discovery; discovery != nil && discovery.TLS != nil {
		in.Discovery.KeyDigest = discovery.TLS.PublicKeySHA256
	}
	state := &ops.DeviceCustody{}
	if err := p.Reader.Get(ctx, client.ObjectKeyFromObject(device), state); err == nil {
		if state.Labels[ops.LabelDeviceUID] == string(device.UID) && state.DeletionTimestamp.IsZero() {
			in.Custody = &state.Spec
		}
	} else if !apierrors.IsNotFound(err) {
		return in, err
	}
	plan, reason, err := custody.CurrentPlan(ctx, p.Reader, device)
	if plan == nil && reason == "" && err != nil {
		return in, err
	}
	in.CurrentPlan = custody.CurrentPlanInput{Plan: plan, Reason: reason}
	if plan != nil {
		reference, err := custody.PlanReferenceOf(plan)
		if err != nil {
			return in, err
		}
		in.CurrentPlan.Reference = ops.ApprovedPlanReference{PlanReference: reference, Approval: custody.ApprovalName(plan)}
	}
	if in.Commissioning, err = latestCommissioning(ctx, p.Reader, device); err != nil {
		return in, err
	}
	if in.Duplicate, err = Duplicate(ctx, p.Reader, device); err != nil {
		return in, err
	}
	return in, nil
}

// latestCommissioning returns the Device's latest succeeded commissioning
// with its evidence.
func latestCommissioning(ctx context.Context, reader client.Reader, device *inventory.Device) (*custody.LatestCommissioning, error) {
	var list ops.DeviceCommissioningList
	if err := reader.List(ctx, &list, client.InNamespace(device.Namespace)); err != nil {
		return nil, err
	}
	var operations []ops.Operation
	for index := range list.Items {
		if list.Items[index].Spec.DeviceRef.Name == device.Name && list.Items[index].Labels[ops.LabelDeviceUID] == string(device.UID) {
			operations = append(operations, &list.Items[index])
		}
	}
	latest, ok := custody.LatestSucceeded(operations)[custody.DeviceIncarnation{Name: device.Name, UID: string(device.UID)}]
	if !ok {
		return nil, nil
	}
	status := latest.OperationStatus()
	if status.Evidence == nil {
		return nil, nil
	}
	var evidence ops.CommissioningEvidence
	if err := json.Unmarshal(status.Evidence.Raw, &evidence); err != nil {
		return nil, nil
	}
	return &custody.LatestCommissioning{DeviceUID: latest.GetLabels()[ops.LabelDeviceUID], FinishedAt: status.FinishedAt.Time, Evidence: evidence}, nil
}

// Duplicate reports whether another Device in the provider project, in any
// namespace, shares one of the Device's identity aliases. Retired Devices
// count until they are deleted. Registration admission checks the other
// provider projects when capacity is registered and enabled.
func Duplicate(ctx context.Context, reader client.Reader, device *inventory.Device) (bool, error) {
	var devices inventory.DeviceList
	if err := reader.List(ctx, &devices); err != nil {
		return false, err
	}
	aliases := inventory.Aliases(device)
	for index := range devices.Items {
		other := &devices.Items[index]
		if other.UID == device.UID {
			continue
		}
		for _, alias := range inventory.Aliases(other) {
			if slices.Contains(aliases, alias) {
				return true, nil
			}
		}
	}
	return false, nil
}

// Project writes the capacity's projected status if it changed. Only the
// projector's identity may write the status of registered capacity.
func (p *Projector) Project(ctx context.Context, capacity *metal.MachineCapacity) (bool, error) {
	projection, err := p.Evaluate(ctx, capacity)
	if err != nil {
		return false, err
	}
	next := capacity.DeepCopyObject().(*metal.MachineCapacity)
	for _, condition := range projection.Conditions {
		apiMeta.SetStatusCondition(&next.Status.Conditions, condition)
	}
	next.Status.Hardware = hardware(projection.Hardware)
	if apiequality.Semantic.DeepEqual(next.Status, capacity.Status) {
		return false, nil
	}
	if err := p.Writer.Status().Update(ctx, next); err != nil {
		return false, err
	}
	return true, nil
}

// ProjectAll projects every capacity in the namespace.
func (p *Projector) ProjectAll(ctx context.Context) error {
	var capacities metal.MachineCapacityList
	if err := p.Reader.List(ctx, &capacities, client.InNamespace(p.Namespace)); err != nil {
		return err
	}
	var failed error
	for index := range capacities.Items {
		if _, err := p.Project(ctx, &capacities.Items[index]); err != nil && !apierrors.IsConflict(err) {
			failed = errors.Join(failed, fmt.Errorf("%s: %w", capacities.Items[index].Name, err))
		}
	}
	return failed
}

// hardware converts inspected hardware to the MachineCapacity shape.
func hardware(in *ops.InspectedHardware) metal.Hardware {
	if in == nil {
		return metal.Hardware{}
	}
	out := metal.Hardware{CPU: metal.HardwareCPU{Architecture: in.CPU.Architecture, Model: in.CPU.Model, Sockets: in.CPU.Sockets, Cores: in.CPU.Cores}, Memory: metal.HardwareMemory{Capacity: in.Memory}}
	for _, disk := range in.Disks {
		out.Disks = append(out.Disks, metal.HardwareDisk{Name: disk.Name, Capacity: disk.Capacity})
	}
	for _, nic := range in.Interfaces {
		out.Interfaces = append(out.Interfaces, metal.HardwareInterface{Name: nic.Name, Speed: nic.Speed})
	}
	return out
}

// Hardware is the capacity hardware a projection carries.
func Hardware(projection custody.CapacityProjection) metal.Hardware {
	return hardware(projection.Hardware)
}

// Offer reports whether the capacity may be offered now, from live inputs,
// and the hardware its latest passed commissioning inspected. It never reads
// the capacity's conditions.
func (p *Projector) Offer(ctx context.Context, capacity *metal.MachineCapacity) (bool, metal.Hardware, error) {
	projection, err := p.Evaluate(ctx, capacity)
	if err != nil {
		return false, metal.Hardware{}, err
	}
	return projection.Available(), hardware(projection.Hardware), nil
}
