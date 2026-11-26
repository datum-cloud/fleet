package nso

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/custody"
)

// BootClaimGroup and BootClaimKind name NSO's claim in an operation's boot
// network input.
const (
	BootClaimGroup = "networking.datumapis.com"
	BootClaimKind  = "NetworkInterfaceClaim"
)

// NSO's reasons on Allocated when IPAM refused an address.
const (
	reasonExhausted        = "AddressPoolExhausted"
	reasonRetainedConflict = "RetainedAddressConflict"
	reasonRejected         = "AddressAllocationRejected"
	// reasonFailed is NSO's catch-all for an IPAM error it does not
	// classify. NSO keeps the claim and retries it, so Fleet waits.
	reasonFailed = "AllocationFailed"
)

// BootReference is the typed reference an operation carries to its boot
// attachment claim.
func BootReference(name string) fleetv1.TypedReference {
	return fleetv1.TypedReference{APIGroup: BootClaimGroup, Kind: BootClaimKind, Name: name}
}

// BootAttachments is the NSO boot address source. For each installation and
// wipe it creates one Physical boot attachment claim on the provider's boot
// network, for the boot NIC's attachment point, named from the operation.
// NSO moves the port onto the boot network and binds its provisioning
// address; the requester claims the address only once NSO reports the port
// Programmed. Nothing configures it by default; IPAM's claimer stays the
// default source. It relies on the contract in assumed.go.
type BootAttachments struct {
	// Client reaches the provider's NSO API and Device inventory. Each
	// requester uses its own.
	Client client.Client
	// Namespace holds the boot attachment claims and the boot network.
	// Empty means the operations' namespace.
	Namespace string
	// Kind is the operation kind the requester creates.
	Kind string
	// Network is the provider-owned boot network.
	Network string
	// IPAM reads the namespace where NSO makes its IPAM claims, IPAMNamespace,
	// to verify that a released claim's address is no longer allocated.
	IPAM          client.Reader
	IPAMNamespace string
	// Location is the site's location, which NSO's IPAM claim names may
	// carry.
	Location string
}

var _ bootaddress.Source = BootAttachments{}

// Requester returns the source with the requester's client and kind.
func (b BootAttachments) Requester(api client.Client, namespace, kind string) bootaddress.Source {
	b.Client, b.Kind = api, kind
	if b.Namespace == "" {
		b.Namespace = namespace
	}
	return b
}

// ClaimKind is NSO's NetworkInterfaceClaim.
func (BootAttachments) ClaimKind() schema.GroupKind {
	return schema.GroupKind{Group: BootClaimGroup, Kind: BootClaimKind}
}

// AttachesBootNIC marks the source as the one that moves boot NICs.
func (BootAttachments) AttachesBootNIC() {}

func (b BootAttachments) valid() error {
	if b.Client == nil || b.Namespace == "" || b.Kind == "" || b.Network == "" {
		return errors.New("nso: a client, namespace, kind and boot network are required")
	}
	return nil
}

func (b BootAttachments) get(ctx context.Context, name string) (*networkingv1.NetworkInterfaceClaim, error) {
	claim := &networkingv1.NetworkInterfaceClaim{}
	return claim, b.Client.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: name}, claim)
}

// Attach creates the operation's boot attachment claim once and returns nil
// once NSO reports the port on the boot network, ErrPending until then, and
// ErrExhausted or ErrRefused when NSO reports IPAM refused its address.
func (b BootAttachments) Attach(ctx context.Context, allocation *fleetv1.MachineAllocation, operation string) error {
	if err := b.valid(); err != nil {
		return err
	}
	name := bootaddress.ClaimName(operation)
	claim, err := b.get(ctx, name)
	if apierrors.IsNotFound(err) {
		device := &inventory.Device{}
		if err := b.Client.Get(ctx, client.ObjectKey{Namespace: allocation.Namespace, Name: allocation.Spec.DeviceRef.Name}, device); err != nil {
			return err
		}
		if string(device.UID) != allocation.Spec.DeviceRef.UID {
			return fmt.Errorf("%w: the Device is another incarnation", bootaddress.ErrConflict)
		}
		point, locateErr := LocateBootNIC(ctx, b.Client, device, allocation.Spec.BootInterface)
		if locateErr != nil {
			return locateErr
		}
		if err := b.Client.Create(ctx, b.desired(allocation, operation, point)); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		claim, err = b.get(ctx, name)
	}
	if err != nil {
		return err
	}
	if err := b.owned(claim, allocation.Name, operation); err != nil {
		return err
	}
	if err := refused(claim); err != nil {
		return err
	}
	if !programmed(claim) {
		return fmt.Errorf("%w: the boot NIC is not on the boot network yet", bootaddress.ErrPending)
	}
	return nil
}

func (b BootAttachments) desired(allocation *fleetv1.MachineAllocation, operation string, point AttachmentPoint) *networkingv1.NetworkInterfaceClaim {
	claim := &networkingv1.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: bootaddress.ClaimName(operation), Namespace: b.Namespace, Labels: map[string]string{
			bootaddress.AllocationLabel: allocation.Name, bootaddress.OperationLabel: operation, bootaddress.KindLabel: b.Kind,
		}},
		Spec: networkingv1.NetworkInterfaceClaimSpec{
			Network:        networkingv1.LocalNetworkRef{Name: b.Network},
			InterfaceName:  allocation.Spec.BootInterface,
			AttachmentMode: PhysicalMode,
			IPFamilies:     []networkingv1.IPFamily{networkingv1.IPv4Protocol},
			ReclaimPolicy:  networkingv1.NetworkInterfaceReclaimPolicyDelete,
		},
	}
	SetAttachmentPoint(claim, point)
	return claim
}

// ErrAttachmentUnknown means inventory cannot say where the boot NIC is
// cabled.
var ErrAttachmentUnknown = errors.New("the boot NIC's attachment point is unknown")

// LocateBootNIC returns where a Device interface meets the network: its MAC
// from Device inventory, and the switch and port at the other end of the one
// cable record that lands on it. Cable endpoints name the interface as the
// Device's component.
func LocateBootNIC(ctx context.Context, reader client.Reader, device *inventory.Device, iface string) (AttachmentPoint, error) {
	var mac string
	for _, candidate := range device.Spec.Interfaces {
		if candidate.Name == iface {
			mac = candidate.MACAddress
		}
	}
	normalized, ok := bootnet.NormalizeMAC(strings.ReplaceAll(mac, "-", ":"))
	if !ok {
		return AttachmentPoint{}, fmt.Errorf("%w: the Device records no MAC for %q", ErrAttachmentUnknown, iface)
	}
	var cables inventory.CableList
	if err := reader.List(ctx, &cables, client.InNamespace(device.Namespace)); err != nil {
		return AttachmentPoint{}, err
	}
	var found []AttachmentPoint
	for _, cable := range cables.Items {
		for _, lane := range cable.Spec.Lanes {
			for _, ends := range [][2]inventory.CableEndpoint{{lane.A, lane.B}, {lane.B, lane.A}} {
				here, there := ends[0], ends[1]
				if here.DeviceRef != nil && here.DeviceRef.Name == device.Name && here.ComponentID == iface && there.DeviceRef != nil && there.ComponentID != "" {
					found = append(found, AttachmentPoint{MACAddress: normalized, Switch: there.DeviceRef.Name, Port: there.ComponentID})
				}
			}
		}
	}
	if len(found) != 1 {
		return AttachmentPoint{}, fmt.Errorf("%w: %d cable records land on %q", ErrAttachmentUnknown, len(found), iface)
	}
	return found[0], nil
}

// owned requires the claim to be this requester's boot attachment for the
// operation, on the boot network, and not being deleted.
func (b BootAttachments) owned(claim *networkingv1.NetworkInterfaceClaim, allocation, operation string) error {
	labels := claim.Labels
	if labels[bootaddress.AllocationLabel] != allocation || labels[bootaddress.OperationLabel] != operation || labels[bootaddress.KindLabel] != b.Kind ||
		claim.Spec.Network.Name != b.Network || claim.Spec.AttachmentMode != PhysicalMode || claim.Spec.ReclaimPolicy != networkingv1.NetworkInterfaceReclaimPolicyDelete ||
		AttachmentPointOf(claim).Port == "" || claim.DeletionTimestamp != nil {
		return fmt.Errorf("%w: %s", bootaddress.ErrConflict, claim.Name)
	}
	return nil
}

// Claim returns the operation's provisioning address, which NSO bound to its
// boot attachment claim. It reads no address while the port is not on the
// boot network.
func (b BootAttachments) Claim(ctx context.Context, allocation, operation string) (*ops.BootNetworkInput, error) {
	if err := b.valid(); err != nil {
		return nil, err
	}
	claim, err := b.get(ctx, bootaddress.ClaimName(operation))
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: the boot NIC is not attached", bootaddress.ErrPending)
	}
	if err != nil {
		return nil, err
	}
	if err := b.owned(claim, allocation, operation); err != nil {
		return nil, err
	}
	if err := refused(claim); err != nil {
		return nil, err
	}
	if !programmed(claim) {
		return nil, fmt.Errorf("%w: the boot NIC is not on the boot network yet", bootaddress.ErrPending)
	}
	address, err := bootAddress(claim)
	if err != nil {
		return nil, err
	}
	return &ops.BootNetworkInput{AddressClaim: BootReference(claim.Name), Address: address}, nil
}

// Release deletes the operation's claim and returns true only once a read
// finds it gone and IPAM holds no allocation for the address NSO claimed for
// it. NSO keeps the claim until the port is off the boot network, so a
// delete that failed or is still in progress keeps it held.
func (b BootAttachments) Release(ctx context.Context, operation string) (bool, error) {
	gone, err := b.remove(ctx, operation)
	if err != nil || !gone {
		return false, err
	}
	if b.IPAM == nil || b.IPAMNamespace == "" || b.Location == "" {
		return false, errors.New("nso: no IPAM reader, namespace and location to verify the boot address release")
	}
	for _, name := range BootAddressIPClaims(bootaddress.ClaimName(operation), b.Location) {
		if free, err := bootaddress.Unallocated(ctx, b.IPAM, b.IPAMNamespace, name); err != nil || !free {
			return false, err
		}
	}
	return true, nil
}

// Detach returns true once the operation's claim is gone, which NSO allows
// only after the port left the boot network.
func (b BootAttachments) Detach(ctx context.Context, operation string) (bool, error) {
	return b.remove(ctx, operation)
}

func (b BootAttachments) remove(ctx context.Context, operation string) (bool, error) {
	if err := b.valid(); err != nil {
		return false, err
	}
	claim, err := b.get(ctx, bootaddress.ClaimName(operation))
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if claim.Labels[bootaddress.OperationLabel] != operation || claim.Labels[bootaddress.KindLabel] != b.Kind {
		return false, fmt.Errorf("%w: %s", bootaddress.ErrConflict, claim.Name)
	}
	if claim.DeletionTimestamp == nil {
		uid := claim.UID
		if err := b.Client.Delete(ctx, claim, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	if _, err := b.get(ctx, claim.Name); !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

// Operations returns the operations of the requester's kind in an
// allocation that still have a boot attachment claim.
func (b BootAttachments) Operations(ctx context.Context, allocation string) ([]string, error) {
	if err := b.valid(); err != nil {
		return nil, err
	}
	var list networkingv1.NetworkInterfaceClaimList
	if err := b.Client.List(ctx, &list, client.InNamespace(b.Namespace), client.MatchingLabels{bootaddress.AllocationLabel: allocation, bootaddress.KindLabel: b.Kind}); err != nil {
		return nil, err
	}
	var operations []string
	for _, claim := range list.Items {
		if operation := claim.Labels[bootaddress.OperationLabel]; operation != "" && claim.Name == bootaddress.ClaimName(operation) {
			operations = append(operations, operation)
		}
	}
	return operations, nil
}

// BootAttachmentCheck is admission's check for NSO's boot attachment claim.
// Only NewBootAttachmentCheck makes a usable one.
type BootAttachmentCheck struct {
	namespace string
	network   string
}

var _ custody.BootAddressCheck = BootAttachmentCheck{}.Verify

// NewBootAttachmentCheck returns the check for claims in namespace, empty for
// the operation's own, on the boot network. It fails when the admission
// reader's scheme does not know NSO's claim, because every read would then
// fail and leave operations Pending forever.
func NewBootAttachmentCheck(scheme *runtime.Scheme, namespace, network string) (BootAttachmentCheck, error) {
	if network == "" {
		return BootAttachmentCheck{}, errors.New("nso: the boot network is required")
	}
	if scheme == nil || !scheme.Recognizes(networkingv1.GroupVersion.WithKind(BootClaimKind)) {
		return BootAttachmentCheck{}, fmt.Errorf("nso: the admission reader's scheme does not know %s/%s", BootClaimGroup, BootClaimKind)
	}
	return BootAttachmentCheck{namespace: namespace, network: network}, nil
}

// Verify requires the input to name the operation's own boot attachment
// claim, by its only name and its labels, created no later than the
// operation, not being deleted, Programmed on the boot network, and bound to
// exactly the input's address. It returns bootaddress.ErrUnreachable when
// NSO cannot be read for any reason but the claim's absence, and
// bootaddress.ErrUnavailable otherwise.
func (c BootAttachmentCheck) Verify(ctx context.Context, reader client.Reader, operation ops.Operation, kind string, input ops.BootNetworkInput) error {
	if reader == nil || c.network == "" {
		return fmt.Errorf("%w: the check was not made by NewBootAttachmentCheck", bootaddress.ErrUnavailable)
	}
	name := bootaddress.ClaimName(operation.GetName())
	if input.AddressClaim != BootReference(name) {
		return fmt.Errorf("%w: the claim is not the operation's own", bootaddress.ErrUnavailable)
	}
	namespace := c.namespace
	if namespace == "" {
		namespace = operation.GetNamespace()
	}
	claim := &networkingv1.NetworkInterfaceClaim{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, claim); apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %v", bootaddress.ErrUnavailable, err)
	} else if err != nil {
		return fmt.Errorf("%w: %v", bootaddress.ErrUnreachable, err)
	}
	created, opened := claim.CreationTimestamp, operation.GetCreationTimestamp()
	if claim.DeletionTimestamp != nil || created.IsZero() || opened.IsZero() || created.After(opened.Time) {
		return fmt.Errorf("%w: the claim was recreated after its operation or is being deleted", bootaddress.ErrUnavailable)
	}
	if claim.Labels[bootaddress.OperationLabel] != operation.GetName() || claim.Labels[bootaddress.KindLabel] != kind {
		return fmt.Errorf("%w: the claim belongs to another operation", bootaddress.ErrUnavailable)
	}
	if claim.Spec.Network.Name != c.network || claim.Spec.AttachmentMode != PhysicalMode || !programmed(claim) {
		return fmt.Errorf("%w: the boot NIC is not on the boot network", bootaddress.ErrUnavailable)
	}
	address, err := bootAddress(claim)
	if err != nil {
		return fmt.Errorf("%w: %v", bootaddress.ErrUnavailable, err)
	}
	if want, err := netip.ParseAddr(input.Address); err != nil || want.String() != address {
		return fmt.Errorf("%w: the claim holds another address", bootaddress.ErrUnavailable)
	}
	return nil
}

// programmed reports whether NSO reports the claim Programmed at its
// generation.
func programmed(claim *networkingv1.NetworkInterfaceClaim) bool {
	condition := apiMeta.FindStatusCondition(claim.Status.Conditions, networkingv1.NetworkInterfaceClaimProgrammed)
	return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == claim.Generation
}

// refused maps NSO's report that IPAM refused the claim's address. A
// failure NSO retries is ErrPending with NSO's reason and message, so the
// wait says why.
func refused(claim *networkingv1.NetworkInterfaceClaim) error {
	condition := apiMeta.FindStatusCondition(claim.Status.Conditions, networkingv1.NetworkInterfaceClaimAllocated)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.ObservedGeneration != claim.Generation {
		return nil
	}
	switch condition.Reason {
	case reasonExhausted:
		return fmt.Errorf("%w: %s", bootaddress.ErrExhausted, condition.Message)
	case reasonRetainedConflict, reasonRejected:
		return fmt.Errorf("%w: %s", bootaddress.ErrRefused, condition.Message)
	case reasonFailed:
		return fmt.Errorf("%w: NSO retries the address: %s: %s", bootaddress.ErrPending, condition.Reason, condition.Message)
	}
	return nil
}

// bootAddress returns the claim's one IPv4 interface address, without its
// prefix length.
func bootAddress(claim *networkingv1.NetworkInterfaceClaim) (string, error) {
	var found []string
	for _, address := range claim.Status.Addresses {
		if address.Family != networkingv1.IPv4Protocol {
			continue
		}
		prefix, err := netip.ParsePrefix(address.Address)
		if err != nil || !prefix.Addr().Is4() {
			return "", errors.New("the claim's IPv4 address is not one host address")
		}
		found = append(found, prefix.Addr().String())
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%w: NSO has not bound an address", bootaddress.ErrPending)
	case 1:
		return found[0], nil
	}
	return "", errors.New("the claim holds more than one IPv4 address")
}
