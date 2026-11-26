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
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	"go.datum.net/fleet/internal/custody"
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

// Machine names one Machine incarnation in one consumer project
// incarnation.
type Machine struct {
	Project    string
	ProjectUID types.UID
	Namespace  string
	Name       string
	UID        types.UID
}

func (m Machine) Valid() bool {
	return m.Project != "" && m.ProjectUID != "" && m.Namespace != "" && m.Name != "" && m.UID != ""
}

func (m Machine) reference() fleetv1.AllocatedMachineReference {
	return fleetv1.AllocatedMachineReference{Project: m.Project, ProjectUID: string(m.ProjectUID), Namespace: m.Namespace, Name: m.Name, UID: string(m.UID)}
}

// Name is the only name a Machine's allocation may have. It derives from the
// Machine UID within its consumer project, so a retried create collides and
// equal UIDs in two projects never share an allocation.
func Name(machine Machine) string {
	sum := sha256.Sum256([]byte(string(machine.ProjectUID) + "/" + string(machine.UID)))
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
	// BootInterface and TargetDisks are pinned on each allocation.
	BootInterface string
	TargetDisks   []string
	// BootAddresses attaches each installation's and wipe's boot NIC and
	// claims its provisioning address when the site's DHCP is synchronized.
	// IPAM's bootaddress.Claimer is the default source. Each requester uses
	// it with its own client. Nil means the site's DHCP is configured
	// independently of Fleet. New claims go through it; claims an operation
	// already holds are released through the source of their claim kind, so
	// after a switch to another source IPAM's claims are still released by
	// IPAM's verified release.
	BootAddresses bootaddress.Source
	// PreviousBootAddresses are sources the requesters used before
	// BootAddresses. Their claims are still released, listed and checked
	// through them, by claim kind, until no operation names their kind.
	// IPAM's claimer always takes part as the default.
	PreviousBootAddresses []bootaddress.Source
}

// Get returns the Machine's allocation, or nil.
func (a *Allocator) Get(ctx context.Context, machine Machine) (*fleetv1.MachineAllocation, error) {
	allocation := &fleetv1.MachineAllocation{}
	if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: Name(machine)}, allocation); apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if allocation.Spec.MachineRef != machine.reference() {
		return nil, ErrConflict
	}
	return allocation, nil
}

// Allocate gives the Machine one Device and returns its active allocation.
// A retry after a lost response finds the same allocation by name and never
// claims a second Device.
func (a *Allocator) Allocate(ctx context.Context, request Request) (*fleetv1.MachineAllocation, error) {
	if !request.Machine.Valid() || request.Class == nil || request.Profile == nil || request.Class.Spec.Capacity == nil {
		return nil, errors.New("allocation: an exact Machine, class and profile are required")
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
		candidate, device, err := a.candidate(ctx, request, tried)
		if err != nil {
			return nil, err
		}
		tried[candidate.Name] = true
		allocation, err = a.newAllocation(request, candidate, device, claim)
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
	err := a.Custody.Claim(ctx, allocation.Spec.DeviceRef.Name, allocation.Name)
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

// candidate returns the first eligible capacity, by name, whose Device
// custody looks free. Custody decides; this only skips Devices that are
// visibly taken.
func (a *Allocator) candidate(ctx context.Context, request Request, tried map[string]bool) (*fleetv1.MachineCapacity, *inventory.Device, error) {
	selector, err := metav1.LabelSelectorAsSelector(&request.Class.Spec.Capacity.MachineCapacities.Selector)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid MachineClass selector: %w", err)
	}
	var capacities fleetv1.MachineCapacityList
	if err := a.Provider.List(ctx, &capacities, client.InNamespace(a.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, nil, err
	}
	sort.Slice(capacities.Items, func(i, j int) bool { return capacities.Items[i].Name < capacities.Items[j].Name })
	for index := range capacities.Items {
		capacity := &capacities.Items[index]
		if tried[capacity.Name] || !Eligible(capacity, request.Profile.Spec) || !selector.Matches(labels.Set(capacity.Labels)) {
			continue
		}
		device := &inventory.Device{}
		if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: capacity.Spec.DeviceRef.Name}, device); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, nil, err
		}
		state := &ops.DeviceCustody{}
		if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: device.Name}, state); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, nil, err
		}
		if !Free(state, device) {
			continue
		}
		return capacity, device, nil
	}
	return nil, nil, ErrInsufficientCapacity
}

// Eligible reports whether a capacity may be offered for the profile: its
// allocation policy is enabled, its Prepared, Healthy and Available
// conditions are current, and its hardware matches exactly.
func Eligible(capacity *fleetv1.MachineCapacity, profile fleetv1.MachineProfileSpec) bool {
	if capacity.Spec.AllocationPolicy != "Enabled" || !capacity.DeletionTimestamp.IsZero() {
		return false
	}
	for _, conditionType := range []string{"Prepared", "Healthy", "Available"} {
		condition := apiMeta.FindStatusCondition(capacity.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != capacity.Generation {
			return false
		}
	}
	return matches(profile, capacity.Status.Hardware)
}

// Free reports whether custody shows the Device free: Verified, for this
// Device incarnation, holder None, no hold and no active operation.
func Free(state *ops.DeviceCustody, device *inventory.Device) bool {
	spec := state.Spec
	return state.DeletionTimestamp.IsZero() && spec.DeviceRef.UID == string(device.UID) && spec.State == ops.CustodyVerified && spec.Holder.Type == ops.HolderNone &&
		spec.RecoveryHold == nil && spec.ActiveOperation == nil && inventory.LifecycleOf(device) == inventory.DeviceActive
}

func (a *Allocator) newAllocation(request Request, capacity *fleetv1.MachineCapacity, device *inventory.Device, claim string) (*fleetv1.MachineAllocation, error) {
	if a.Cleaning == nil {
		return nil, errors.New("allocation: no cleaning policy resolver")
	}
	digest, err := a.Cleaning(capacity.Spec.CleaningPolicyRef.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve cleaning policy: %w", err)
	}
	allocation := &fleetv1.MachineAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: Name(request.Machine), Namespace: a.Namespace},
		Spec: fleetv1.MachineAllocationSpec{
			MachineRef:         request.Machine.reference(),
			MachineCapacityRef: fleetv1.NamespacedReference{Namespace: capacity.Namespace, Name: capacity.Name},
			DeviceRef:          fleetv1.AllocatedDeviceReference{Name: device.Name, UID: string(device.UID)},
			Accepted: fleetv1.AcceptedContract{
				MachineClassRef:   fleetv1.UIDReference{Name: request.Class.Name, UID: string(request.Class.UID)},
				MachineProfileRef: fleetv1.UIDReference{Name: request.Profile.Name, UID: string(request.Profile.UID)},
				CPU:               request.Profile.Spec.CPU,
				Memory:            request.Profile.Spec.Memory,
			},
			Hardware:             capacity.Status.Hardware,
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
	name := custody.InstallationName(allocation.Name, string(installation.UID))
	// An existing installation keeps the inputs it was created with; its
	// boot address may already be released.
	existing := &ops.DeviceInstallation{}
	if err := a.Provider.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, existing); err == nil {
		return existing, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	network, err := a.bootNetwork(ctx, a.installClaims(), allocation, name)
	if err != nil {
		return nil, err
	}
	operation := &ops.DeviceInstallation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.Namespace},
		Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: a.envelope(allocation, a.ProviderCreator),
			RequestUID:        string(installation.UID),
			Image:             image,
			BootInterface:     allocation.Spec.BootInterface,
			TargetDisks:       slices.Clone(allocation.Spec.TargetDisks),
			BootNetwork:       network,
		},
	}
	return operation, a.createOnce(ctx, a.Provider, operation, &ops.DeviceInstallation{})
}

// installClaims and wipeClaims are each requester's boot address source,
// with the requester's own client. They are nil when the site's DHCP is not
// synchronized.
func (a *Allocator) installClaims() bootaddress.Source {
	return a.claims(a.Provider, "DeviceInstallation")
}

func (a *Allocator) wipeClaims() bootaddress.Source {
	return a.claims(a.Releaser, "DeviceWipe")
}

func (a *Allocator) claims(api client.Client, kind string) bootaddress.Source {
	if a.BootAddresses == nil {
		return nil
	}
	return a.BootAddresses.Requester(api, a.Namespace, kind)
}

// knownSource is one source a requester's claims may belong to. Implicit is
// IPAM's default claimer when no one configured it: a provider API without
// IPAM's kinds has no IPAM claims for it to release.
type knownSource struct {
	source   bootaddress.Source
	implicit bool
}

// releaseSources returns, by claim kind, every source whose claims a
// requester may still hold: the configured source, the previous ones, and
// IPAM's claimer, which is the default and so holds the claims made before a
// switch. Each is the requester's own. It is empty when the site's DHCP is
// not synchronized.
func (a *Allocator) releaseSources(api client.Client, kind string) []knownSource {
	if a.BootAddresses == nil {
		return nil
	}
	byKind := map[schema.GroupKind]knownSource{bootaddress.GVK.GroupKind(): {bootaddress.Claimer{}, true}}
	for _, previous := range a.PreviousBootAddresses {
		byKind[previous.ClaimKind()] = knownSource{previous, false}
	}
	byKind[a.BootAddresses.ClaimKind()] = knownSource{a.BootAddresses, false}
	kinds := make([]schema.GroupKind, 0, len(byKind))
	for claimKind := range byKind {
		kinds = append(kinds, claimKind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].String() < kinds[j].String() })
	sources := make([]knownSource, 0, len(kinds))
	for _, claimKind := range kinds {
		known := byKind[claimKind]
		sources = append(sources, knownSource{known.source.Requester(api, a.Namespace, kind), known.implicit})
	}
	return sources
}

// absentAPI reports whether an error means the API does not serve the kind.
func absentAPI(err error) bool {
	return apiMeta.IsNoMatchError(err) || discovery.IsGroupDiscoveryFailedError(err)
}

// recordedOperations returns the allocation's operations of one kind whose
// boot network input names the claim kind. Listing them from the
// allocator's own record, not only from the claims that remain, lets a
// release be verified again after the claim is gone.
func (a *Allocator) recordedOperations(ctx context.Context, api client.Client, kind string, allocation string, claimKind schema.GroupKind) ([]string, error) {
	var found []string
	keep := func(name string, authority ops.Authority, network *ops.BootNetworkInput) {
		if authority.AllocationRef != nil && authority.AllocationRef.Name == allocation && network != nil &&
			(schema.GroupKind{Group: network.AddressClaim.APIGroup, Kind: network.AddressClaim.Kind}) == claimKind {
			found = append(found, name)
		}
	}
	switch kind {
	case "DeviceInstallation":
		var list ops.DeviceInstallationList
		if err := api.List(ctx, &list, client.InNamespace(a.Namespace)); err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			keep(item.Name, item.Spec.Authority, item.Spec.BootNetwork)
		}
	case "DeviceWipe":
		var list ops.DeviceWipeList
		if err := api.List(ctx, &list, client.InNamespace(a.Namespace)); err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			keep(item.Name, item.Spec.Authority, item.Spec.BootNetwork)
		}
	}
	return found, nil
}

// bootNetwork attaches the operation's boot NIC, then claims its boot
// address, before the operation exists. No address is claimed until the
// boot NIC is on the boot network. It returns nil when the site's DHCP is
// not synchronized.
func (a *Allocator) bootNetwork(ctx context.Context, source bootaddress.Source, allocation *fleetv1.MachineAllocation, operation string) (*ops.BootNetworkInput, error) {
	if source == nil {
		return nil, nil
	}
	if err := source.Attach(ctx, allocation, operation); err != nil {
		return nil, err
	}
	return source.Claim(ctx, allocation.Name, operation)
}

func (a *Allocator) envelope(allocation *fleetv1.MachineAllocation, creator ops.Creator) ops.OperationEnvelope {
	return ops.OperationEnvelope{
		Creator:   creator,
		DeviceRef: ops.DeviceReference{Name: allocation.Spec.DeviceRef.Name, UID: allocation.Spec.DeviceRef.UID},
		Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: allocation.Name}},
	}
}

// createOnce creates an operation, or reads the existing one and requires it
// to carry the same spec.
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
