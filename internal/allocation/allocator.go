// Package allocation allocates a Device to a Machine, installs it, and
// releases it, on Device custody. Every mode uses it: synthetic staging, the
// standalone smoke test and the multi-plane acceptance.
//
// An allocation is a MachineAllocation in the provider project, named from
// its Machine. The allocator writes in the order the custody design fixes:
// quota claim, then MachineAllocation, then custody. The conditional custody
// update decides which Machine wins a Device; the capacity conditions only
// pick candidates.
package allocation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/tenancy"
)

var (
	// ErrInsufficientCapacity means no eligible Device could be claimed.
	ErrInsufficientCapacity = errors.New("no eligible capacity is free")
	// ErrConflict means the allocation name belongs to another Machine.
	ErrConflict = errors.New("the allocation belongs to another Machine")
	// ErrReleased means the Machine's allocation is releasing or released.
	ErrReleased = errors.New("the allocation is releasing or released")
	// ErrCustodyLost means an active allocation no longer holds its Device.
	// Fleet does not repair it: an operator resolves the custody conflict.
	ErrCustodyLost = errors.New("the allocation no longer holds its Device")
	// ErrPredecessor means an Installation does not follow the current one.
	ErrPredecessor = errors.New("the installation does not name the current installation as its predecessor")
	// ErrUnsupported means a request Fleet does not implement, such as a
	// bootstrap payload.
	ErrUnsupported = errors.New("unsupported request")
)

// Machine names one Machine incarnation in one consumer incarnation.
type Machine struct {
	// Consumer is the consumer's typed reference, as recorded in the
	// allocation's consumerRef.
	Consumer fleetv1.TypedReference
	// ConsumerUID is the consumer's incarnation. It goes into the
	// allocation's name and labels, never its spec.
	ConsumerUID types.UID
	Namespace   string
	Name        string
	UID         types.UID
}

// MachineOf is a resolved consumer's Machine.
func MachineOf(consumer tenancy.Consumer, machine *fleetv1.Machine) Machine {
	return Machine{Consumer: consumer.Ref, ConsumerUID: consumer.UID, Namespace: machine.Namespace, Name: machine.Name, UID: machine.UID}
}

func (m Machine) Valid() bool {
	return tenancy.Supported(m.Consumer) == nil && m.ConsumerUID != "" && m.Namespace != "" && m.Name != "" && m.UID != ""
}

func (m Machine) reference() fleetv1.AllocatedMachineReference {
	return fleetv1.AllocatedMachineReference{Namespace: m.Namespace, Name: m.Name}
}

// labels pin the Machine and consumer incarnations. No spec holds an object
// UID; the allocation's name hashes both, and these labels confirm them.
func (m Machine) labels() map[string]string {
	return map[string]string{fleetv1.LabelMachineUID: string(m.UID), fleetv1.LabelConsumerUID: string(m.ConsumerUID)}
}

// Name is the only name a Machine's allocation may have. It derives from the
// Machine UID within its consumer, so a retried create collides and equal
// UIDs in two consumers never share an allocation.
func Name(machine Machine) string {
	sum := sha256.Sum256([]byte(string(machine.ConsumerUID) + "/" + string(machine.UID)))
	return "alloc-" + hex.EncodeToString(sum[:])[:16]
}

// Quota reserves and releases the consumer's quota for one Machine. Both are
// idempotent and name the claim from the Machine, so a claim reserved before
// a crash, or before capacity was found, is released by the Machine alone.
type Quota interface {
	Acquire(ctx context.Context, machine Machine) (string, error)
	// Release returns nil once the Machine's claim no longer exists.
	Release(ctx context.Context, machine Machine) error
}

// CleaningPolicies resolves a MachineCapacity's cleaning policy reference
// to the digest of the policy record pinned at allocation.
type CleaningPolicies func(name string) (string, error)

// Offers evaluates, from live inputs, whether a capacity may be offered and
// the hardware its latest passed commissioning inspected. The capacity
// projector implements it. The allocator never reads capacity conditions, so
// editing status cannot make a server allocatable.
type Offers interface {
	Offer(ctx context.Context, capacity *fleetv1.MachineCapacity) (bool, fleetv1.Hardware, error)
}

// Request is one Machine's allocation request, already admitted.
type Request struct {
	Machine Machine
	// Class and Profile are the provider's source class and profile.
	Class     *fleetv1.MachineClass
	Profile   *fleetv1.MachineProfile
	Authority fleetv1.AllocationAuthority
}

// Allocator allocates, installs and releases on custody.
type Allocator struct {
	// Provider is the Metal provider's client to the provider project. It
	// must read through to the API server. It creates MachineAllocations
	// and DeviceInstallations.
	Provider        client.Client
	ProviderCreator ops.Creator
	// Releaser is the release controller's client. It creates DeviceWipes.
	Releaser       client.Client
	ReleaseCreator ops.Creator
	Custody        *custody.Coordinator
	// Namespace is the Devices' namespace. Capacities and allocations live
	// in it too.
	Namespace string
	Quota     Quota
	Cleaning  CleaningPolicies
	// Capacity decides which capacity may be offered.
	Capacity Offers
	// BootInterface and TargetDisks are pinned on each allocation.
	BootInterface string
	TargetDisks   []string
}

// Get returns the Machine's allocation, or nil.
func (a *Allocator) Get(ctx context.Context, machine Machine) (*fleetv1.MachineAllocation, error) {
	allocation := &fleetv1.MachineAllocation{}
	if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: Name(machine)}, allocation); apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	// A same-name Machine or consumer never inherits the allocation.
	if allocation.Spec.ConsumerRef != machine.Consumer || allocation.Spec.MachineRef != machine.reference() || allocation.Labels[fleetv1.LabelMachineUID] != string(machine.UID) || allocation.Labels[fleetv1.LabelConsumerUID] != string(machine.ConsumerUID) {
		return nil, ErrConflict
	}
	return allocation, nil
}

// Allocate gives the Machine one Device and returns its active allocation.
// A retry after a lost response finds the same allocation by name and never
// claims a second Device.
func (a *Allocator) Allocate(ctx context.Context, request Request) (*fleetv1.MachineAllocation, error) {
	if err := tenancy.Supported(request.Machine.Consumer); err != nil {
		return nil, err
	}
	if !request.Machine.Valid() || request.Class == nil || request.Profile == nil || request.Class.Spec.Capacity == nil {
		return nil, errors.New("allocation: an exact Machine, class and profile are required")
	}
	// No spec holds an object UID: the allocation's name and labels carry
	// the incarnations, so the authority names its decision by name.
	for _, uid := range []types.UID{request.Machine.UID, request.Machine.ConsumerUID} {
		if strings.Contains(request.Authority.Principal, string(uid)) || strings.Contains(request.Authority.Decision, string(uid)) {
			return nil, fmt.Errorf("%w: the allocation authority records an object UID", ErrUnsupported)
		}
	}
	if request.Class.Spec.Capacity.MachineCapacities.Namespace != a.Namespace {
		return nil, errors.New("allocation: the class selects capacity outside the Device namespace")
	}
	var claim string
	if a.Quota != nil {
		var err error
		if claim, err = a.Quota.Acquire(ctx, request.Machine); err != nil {
			return nil, fmt.Errorf("reserve quota: %w", err)
		}
	}
	tried := map[string]bool{}
	for {
		allocation, err := a.Get(ctx, request.Machine)
		if err != nil {
			return nil, err
		}
		if allocation != nil {
			done, err := a.claim(ctx, allocation)
			if err != nil || done {
				return allocation, err
			}
			tried[allocation.Spec.MachineCapacityRef.Name] = true
			continue
		}
		candidate, device, hardware, err := a.candidate(ctx, request, tried)
		if err != nil {
			return nil, err
		}
		tried[candidate.Name] = true
		allocation, err = a.newAllocation(request, candidate, device, hardware, claim)
		if err != nil {
			return nil, err
		}
		if err := a.Provider.Create(ctx, allocation); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
	}
}

// claim claims custody for an existing allocation. It returns true when the
// allocation holds its Device. An allocation that never held it is deleted
// when another holder took the Device, so the caller selects again.
func (a *Allocator) claim(ctx context.Context, allocation *fleetv1.MachineAllocation) (bool, error) {
	switch allocation.Status.Phase {
	case fleetv1.AllocationReleasing, fleetv1.AllocationReleased:
		return false, ErrReleased
	}
	err := a.Custody.Claim(ctx, allocation.Spec.DeviceRef.Name, types.UID(allocation.Labels[fleetv1.LabelDeviceUID]), allocation.Name)
	switch {
	case err == nil:
		return true, a.setPhase(ctx, allocation, fleetv1.AllocationActive)
	case !errors.Is(err, custody.ErrUnavailable):
		return false, err
	case allocation.Status.Phase == fleetv1.AllocationActive:
		return false, ErrCustodyLost
	}
	// No effect ran under this allocation: it never held the Device.
	uid, version := allocation.UID, allocation.ResourceVersion
	if err := a.Provider.Delete(ctx, allocation, client.Preconditions{UID: &uid, ResourceVersion: &version}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return false, err
	}
	return false, nil
}

// candidate returns the first capacity, by name, that the projection offers
// for the profile and whose Device custody looks free, with the hardware its
// commissioning inspected. Custody decides; this only skips Devices that are
// visibly taken.
func (a *Allocator) candidate(ctx context.Context, request Request, tried map[string]bool) (*fleetv1.MachineCapacity, *inventory.Device, fleetv1.Hardware, error) {
	selector, err := metav1.LabelSelectorAsSelector(&request.Class.Spec.Capacity.MachineCapacities.Selector)
	if err != nil {
		return nil, nil, fleetv1.Hardware{}, fmt.Errorf("invalid MachineClass selector: %w", err)
	}
	var capacities fleetv1.MachineCapacityList
	if err := a.Provider.List(ctx, &capacities, client.InNamespace(a.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, nil, fleetv1.Hardware{}, err
	}
	sort.Slice(capacities.Items, func(i, j int) bool { return capacities.Items[i].Name < capacities.Items[j].Name })
	if a.Capacity == nil {
		return nil, nil, fleetv1.Hardware{}, errors.New("allocation: no capacity evaluator")
	}
	for index := range capacities.Items {
		capacity := &capacities.Items[index]
		if tried[capacity.Name] || capacity.Spec.AllocationPolicy != "Enabled" || !capacity.DeletionTimestamp.IsZero() || !selector.Matches(labels.Set(capacity.Labels)) {
			continue
		}
		offered, hardware, err := a.Capacity.Offer(ctx, capacity)
		if err != nil {
			return nil, nil, fleetv1.Hardware{}, err
		}
		if !offered || !matches(request.Profile.Spec, hardware) {
			continue
		}
		device := &inventory.Device{}
		if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: capacity.Spec.DeviceRef.Name}, device); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, nil, fleetv1.Hardware{}, err
		}
		state := &ops.DeviceCustody{}
		if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: device.Name}, state); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, nil, fleetv1.Hardware{}, err
		}
		if !Free(state, device) {
			continue
		}
		return capacity, device, hardware, nil
	}
	return nil, nil, fleetv1.Hardware{}, ErrInsufficientCapacity
}

// Free reports whether custody shows the Device free: Verified, for this
// Device incarnation, holder None, no hold and no active operation.
func Free(state *ops.DeviceCustody, device *inventory.Device) bool {
	spec := state.Spec
	return state.DeletionTimestamp.IsZero() && state.Labels[ops.LabelDeviceUID] == string(device.UID) && spec.State == ops.CustodyVerified && spec.Holder.Type == ops.HolderNone &&
		spec.RecoveryHold == nil && spec.ActiveOperation == nil && inventory.LifecycleOf(device) == inventory.DeviceActive
}

func (a *Allocator) newAllocation(request Request, capacity *fleetv1.MachineCapacity, device *inventory.Device, hardware fleetv1.Hardware, claim string) (*fleetv1.MachineAllocation, error) {
	if a.Cleaning == nil {
		return nil, errors.New("allocation: no cleaning policy resolver")
	}
	digest, err := a.Cleaning(capacity.Spec.CleaningPolicyRef.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve cleaning policy: %w", err)
	}
	classDigest, err := custody.SpecDigest(request.Class.Spec)
	if err != nil {
		return nil, err
	}
	profileDigest, err := custody.SpecDigest(request.Profile.Spec)
	if err != nil {
		return nil, err
	}
	labels := request.Machine.labels()
	labels[fleetv1.LabelDeviceUID] = string(device.UID)
	allocation := &fleetv1.MachineAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: Name(request.Machine), Namespace: a.Namespace, Labels: labels},
		Spec: fleetv1.MachineAllocationSpec{
			ConsumerRef:        request.Machine.Consumer,
			MachineRef:         request.Machine.reference(),
			MachineCapacityRef: fleetv1.NamespacedReference{Namespace: capacity.Namespace, Name: capacity.Name},
			DeviceRef:          fleetv1.AllocatedDeviceReference{Name: device.Name},
			Accepted: fleetv1.AcceptedContract{
				MachineClassRef:   fleetv1.ContentReference{Name: request.Class.Name, Generation: request.Class.Generation, Digest: classDigest},
				MachineProfileRef: fleetv1.ContentReference{Name: request.Profile.Name, Generation: request.Profile.Generation, Digest: profileDigest},
				CPU:               request.Profile.Spec.CPU,
				Memory:            request.Profile.Spec.Memory,
			},
			Hardware:             hardware,
			Authority:            request.Authority,
			CleaningPolicyDigest: digest,
			BootInterface:        a.BootInterface,
			TargetDisks:          slices.Clone(a.TargetDisks),
		},
	}
	if claim != "" {
		allocation.Spec.QuotaClaimRef = &fleetv1.LocalReference{Name: claim}
	}
	return allocation, nil
}

// setPhase writes the allocation's phase with the resourceVersion read.
func (a *Allocator) setPhase(ctx context.Context, allocation *fleetv1.MachineAllocation, phase string) error {
	if allocation.Status.Phase == phase {
		return nil
	}
	allocation.Status.Phase = phase
	return a.Provider.Status().Update(ctx, allocation)
}

// Installation is one Installation an allocation's Machine requested.
type Installation struct {
	Installation *fleetv1.Installation
	// Current is the Machine's current installation, or empty.
	Current string
}

// ParseImage splits an image reference into its name and digest. Fleet
// installs only images pinned by digest.
func ParseImage(reference string) (ops.ImageInput, error) {
	name, digest, ok := strings.Cut(reference, "@")
	hexDigest, sha := strings.CutPrefix(digest, "sha256:")
	if !ok || name == "" || !sha || len(hexDigest) != 64 || strings.Trim(hexDigest, "0123456789abcdef") != "" {
		return ops.ImageInput{}, fmt.Errorf("%w: the image must be name@sha256:<64 hex>", ErrUnsupported)
	}
	return ops.ImageInput{Name: name, Digest: digest}, nil
}

// Install creates the DeviceInstallation for an Installation. It is
// create-only: a retry finds the same operation by name.
func (a *Allocator) Install(ctx context.Context, allocation *fleetv1.MachineAllocation, request Installation) (*ops.DeviceInstallation, error) {
	installation := request.Installation
	if allocation.Status.Phase != fleetv1.AllocationActive {
		return nil, ErrReleased
	}
	if installation.Spec.BootstrapConfigRef != nil {
		return nil, fmt.Errorf("%w: bootstrap payloads need the sealed store, which is not implemented", ErrUnsupported)
	}
	predecessor := installation.Spec.ExpectedPredecessor
	if (request.Current == "" && predecessor.Type != "None") || (request.Current != "" && (predecessor.Type != "Installation" || predecessor.Name != request.Current)) {
		return nil, ErrPredecessor
	}
	image, err := ParseImage(installation.Spec.ImageRef.Name)
	if err != nil {
		return nil, err
	}
	labels := operationLabels(allocation)
	labels[ops.LabelInstallationUID] = string(installation.UID)
	operation := &ops.DeviceInstallation{
		ObjectMeta: metav1.ObjectMeta{Name: custody.InstallationName(allocation.Name, string(installation.UID)), Namespace: a.Namespace, Labels: labels},
		Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: a.envelope(allocation, a.ProviderCreator),
			Image:             image,
			BootInterface:     allocation.Spec.BootInterface,
			TargetDisks:       slices.Clone(allocation.Spec.TargetDisks),
		},
	}
	return operation, a.createOnce(ctx, a.Provider, operation, &ops.DeviceInstallation{})
}

func (a *Allocator) envelope(allocation *fleetv1.MachineAllocation, creator ops.Creator) ops.OperationEnvelope {
	return ops.OperationEnvelope{
		Creator:   creator,
		DeviceRef: ops.DeviceReference{Name: allocation.Spec.DeviceRef.Name},
		Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: allocation.Name}},
	}
}

// operationLabels carries the allocation's Device incarnation onto an
// operation, where admission compares it with the Device.
func operationLabels(allocation *fleetv1.MachineAllocation) map[string]string {
	return map[string]string{ops.LabelDeviceUID: allocation.Labels[fleetv1.LabelDeviceUID]}
}

// createOnce creates an operation, or reads the existing one and requires it
// to carry the same spec and the same incarnation labels, so an operation for
// a same-name Device or Installation is never taken for this one.
func (a *Allocator) createOnce(ctx context.Context, api client.Client, operation ops.Operation, existing ops.Operation) error {
	if err := api.Create(ctx, operation); err == nil || !apierrors.IsAlreadyExists(err) {
		return err
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(operation), existing); err != nil {
		return err
	}
	want, err := specDigest(operation)
	if err != nil {
		return err
	}
	got, err := specDigest(existing)
	if err != nil {
		return err
	}
	if want != got {
		return fmt.Errorf("%w: %s exists with other inputs", ErrConflict, operation.GetName())
	}
	for key, value := range operation.GetLabels() {
		if existing.GetLabels()[key] != value {
			return fmt.Errorf("%w: %s exists for another %s", ErrConflict, operation.GetName(), key)
		}
	}
	data, err := json.Marshal(existing)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, operation)
}

func specDigest(operation ops.Operation) (string, error) {
	var object struct {
		Spec json.RawMessage `json:"spec"`
	}
	data, err := json.Marshal(operation)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return "", err
	}
	sum := sha256.Sum256(object.Spec)
	return hex.EncodeToString(sum[:]), nil
}
