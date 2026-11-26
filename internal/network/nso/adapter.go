// Package nso asks NSO for one interface per Machine interface and reads
// back what NSO and the network owner's realizer report. Claims are named
// from the allocation and the interface, so a crash between creating a claim
// and recording it is recovered by name. NSO has no attachment mode for a
// physical server yet; the adapter refuses every request until one is
// configured.
package nso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/network"
)

// Labels name the allocation a claim was created for, and the Machine whose
// interface it is, so a claim's status change wakes that Machine.
const (
	AllocationLabel = "fleet.datumapis.com/allocation"
	MachineUIDLabel = "fleet.datumapis.com/machine-uid"
)

// Authorizer decides whether a principal may use a Network. It must check
// the principal Fleet admitted the Machine for, never Fleet's own identity.
type Authorizer interface {
	AuthorizeNetworkUse(ctx context.Context, principal string, network fleetv1.NamespacedReference) error
}

// Adapter is Fleet's side of the NSO contract.
type Adapter struct {
	// Client reaches the NSO API that holds the consumer's Networks and
	// the claims Fleet creates for them.
	Client client.Client
	// Mode is NSO's attachment mode for a physical server. Empty refuses
	// every request.
	Mode networkingv1.NetworkInterfaceAttachmentMode
	// Authorizer checks network use. Nil refuses every request.
	Authorizer Authorizer
}

// ClaimName is the only name the claim for a Machine interface may have.
func ClaimName(allocation, iface string) string {
	sum := sha256.Sum256([]byte(allocation + "/" + iface))
	return "fleet-" + hex.EncodeToString(sum[:])[:40]
}

// Check admits a Machine's interfaces before allocation: each names a
// Network in the Machine's namespace that exists, is ready, carries the
// requested families, and that the principal may use. It reserves nothing.
func (a Adapter) Check(ctx context.Context, machine *fleetv1.Machine, principal string) error {
	if len(machine.Spec.NetworkInterfaces) == 0 {
		return nil
	}
	if a.Client == nil || a.Mode == "" {
		return network.ErrUnsupported
	}
	if a.Authorizer == nil || principal == "" {
		return fmt.Errorf("%w: no network-use check is available", network.ErrUnauthorized)
	}
	seen := map[string]bool{}
	for _, request := range machine.Spec.NetworkInterfaces {
		if request.Name == "" || seen[request.Name] {
			return fmt.Errorf("%w: interface names must be unique", network.ErrNotReady)
		}
		seen[request.Name] = true
		reference := Reference(machine, request)
		if reference.Namespace != machine.Namespace {
			return fmt.Errorf("%w: a Network must be in the Machine's namespace", network.ErrUnauthorized)
		}
		var current networkingv1.Network
		if err := a.Client.Get(ctx, client.ObjectKey{Namespace: reference.Namespace, Name: reference.Name}, &current); apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: Network %s does not exist", network.ErrNotReady, reference.Name)
		} else if err != nil {
			return err
		}
		ready := apiMeta.FindStatusCondition(current.Status.Conditions, networkingv1.NetworkReady)
		if current.DeletionTimestamp != nil || ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != current.Generation {
			return fmt.Errorf("%w: Network %s is not ready", network.ErrNotReady, reference.Name)
		}
		for _, family := range request.IPFamilies {
			carried := false
			for _, offered := range current.Spec.IPFamilies {
				carried = carried || string(offered) == family
			}
			if !carried {
				return fmt.Errorf("%w: Network %s does not carry %s", network.ErrNotReady, reference.Name, family)
			}
		}
		if err := a.Authorizer.AuthorizeNetworkUse(ctx, principal, reference); err != nil {
			return fmt.Errorf("%w: %v", network.ErrUnauthorized, err)
		}
	}
	return nil
}

// Reference is the Network an interface names, defaulting to the Machine's
// namespace.
func Reference(machine *fleetv1.Machine, request fleetv1.NetworkInterfaceRequest) fleetv1.NamespacedReference {
	reference := request.Network
	if reference.Namespace == "" {
		reference.Namespace = machine.Namespace
	}
	return reference
}

// Desired is the claim Fleet creates for one interface.
func (a Adapter) Desired(allocation *fleetv1.MachineAllocation, machine *fleetv1.Machine, request fleetv1.NetworkInterfaceRequest) *networkingv1.NetworkInterfaceClaim {
	reference := Reference(machine, request)
	reclaim, _ := normalizeReclaim(request.ReclaimPolicy)
	claim := &networkingv1.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: ClaimName(allocation.Name, request.Name), Namespace: reference.Namespace, Labels: map[string]string{AllocationLabel: allocation.Name, MachineUIDLabel: string(machine.UID)}},
		Spec: networkingv1.NetworkInterfaceClaimSpec{
			Network:        networkingv1.LocalNetworkRef{Name: reference.Name},
			InterfaceName:  request.Name,
			AttachmentMode: a.Mode,
			ReclaimPolicy:  networkingv1.NetworkInterfaceReclaimPolicy(reclaim),
		},
	}
	for _, family := range request.IPFamilies {
		claim.Spec.IPFamilies = append(claim.Spec.IPFamilies, networkingv1.IPFamily(family))
	}
	for _, address := range request.Addresses {
		claim.Spec.Addresses = append(claim.Spec.Addresses, networkingv1.NetworkInterfaceAddressRequest{Class: address.ClassRef.Name})
	}
	return claim
}

// Observation is what NSO reports for one interface.
type Observation struct {
	Status fleetv1.NetworkInterfaceStatus
	// Ready is Allocated, Prepared and Programmed at the claim's generation.
	Ready bool
}

// Ensure creates the interface's claim once, or finds it, and reports it.
// The claim must carry the allocation's label and exactly the desired spec,
// and, once the allocation records its UID, that UID. It returns the UID.
func (a Adapter) Ensure(ctx context.Context, allocation *fleetv1.MachineAllocation, machine *fleetv1.Machine, request fleetv1.NetworkInterfaceRequest, recorded fleetv1.AllocatedInterface) (types.UID, Observation, error) {
	if a.Client == nil || a.Mode == "" {
		return "", Observation{}, network.ErrUnsupported
	}
	desired := a.Desired(allocation, machine, request)
	if desired.Name != recorded.Claim.Name || desired.Namespace != recorded.Claim.Namespace {
		return "", Observation{}, fmt.Errorf("%w: the recorded claim is not the interface's", network.ErrConflict)
	}
	actual := &networkingv1.NetworkInterfaceClaim{}
	err := a.Client.Get(ctx, client.ObjectKeyFromObject(desired), actual)
	if apierrors.IsNotFound(err) && recorded.Claim.UID == "" {
		if err := a.Client.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", Observation{}, err
		}
		err = a.Client.Get(ctx, client.ObjectKeyFromObject(desired), actual)
	}
	if apierrors.IsNotFound(err) {
		// Fleet created and recorded the claim, and it is gone.
		return "", Observation{}, fmt.Errorf("%w: claim %s is gone", network.ErrConflict, desired.Name)
	}
	if err != nil {
		return "", Observation{}, err
	}
	if actual.Labels[AllocationLabel] != allocation.Name || actual.Labels[MachineUIDLabel] != string(machine.UID) || spec(actual.Spec) != spec(desired.Spec) || (recorded.Claim.UID != "" && string(actual.UID) != recorded.Claim.UID) || actual.DeletionTimestamp != nil {
		return "", Observation{}, fmt.Errorf("%w: claim %s", network.ErrConflict, desired.Name)
	}
	observation, err := a.observe(ctx, machine, request, recorded, actual)
	return actual.UID, observation, err
}

func (a Adapter) observe(ctx context.Context, machine *fleetv1.Machine, request fleetv1.NetworkInterfaceRequest, recorded fleetv1.AllocatedInterface, claim *networkingv1.NetworkInterfaceClaim) (Observation, error) {
	reference := Reference(machine, request)
	status := fleetv1.NetworkInterfaceStatus{
		Name:       request.Name,
		Network:    fleetv1.NetworkReference{APIGroup: "networking.datumapis.com", Kind: "Network", Namespace: reference.Namespace, Name: reference.Name},
		MACAddress: recorded.Attachment.MACAddress,
	}
	ready := true
	for _, kind := range []string{networkingv1.NetworkInterfaceClaimAllocated, networkingv1.NetworkInterfaceClaimPrepared, networkingv1.NetworkInterfaceClaimProgrammed} {
		condition := apiMeta.FindStatusCondition(claim.Status.Conditions, kind)
		if condition == nil || condition.ObservedGeneration != claim.Generation {
			ready = false
			status.Conditions = append(status.Conditions, metav1.Condition{Type: kind, Status: metav1.ConditionUnknown, Reason: "NotReported", ObservedGeneration: machine.Generation, LastTransitionTime: metav1.Now()})
			continue
		}
		ready = ready && condition.Status == metav1.ConditionTrue
		status.Conditions = append(status.Conditions, metav1.Condition{Type: kind, Status: condition.Status, Reason: condition.Reason, ObservedGeneration: machine.Generation, LastTransitionTime: condition.LastTransitionTime})
	}
	for _, address := range claim.Status.Addresses {
		status.Addresses = append(status.Addresses, fleetv1.AddressAssignment{Address: address.Address, Family: string(address.Family), Gateway: address.Gateway, Primary: address.Primary})
	}
	for _, address := range claim.Status.ExternalAddresses {
		status.ExternalAddresses = append(status.ExternalAddresses, fleetv1.ExternalAddressAssignment{Address: address.Address, Family: string(address.Family), ClassRef: fleetv1.LocalReference{Name: address.Class}})
	}
	if claim.Status.NetworkInterfaceRef != nil {
		var iface networkingv1.NetworkInterface
		if err := a.Client.Get(ctx, client.ObjectKey{Namespace: claim.Namespace, Name: claim.Status.NetworkInterfaceRef.Name}, &iface); err != nil {
			return Observation{}, err
		}
		if iface.Spec.ClaimRef == nil || iface.Spec.ClaimRef.Name != claim.Name || iface.Spec.Network.Name != reference.Name {
			return Observation{}, fmt.Errorf("%w: interface %s is not bound to claim %s", network.ErrConflict, iface.Name, claim.Name)
		}
		status.MTU = iface.Spec.MTU
	}
	return Observation{Status: status, Ready: ready}, nil
}

// Release deletes a recorded claim and reports whether it is gone. NSO keeps
// the claim until the realizer detached the port, so absence is the
// evidence that closes the Network and Address gates. A claim under the
// name with another UID is not Fleet's; Fleet's own is gone.
func (a Adapter) Release(ctx context.Context, recorded fleetv1.ClaimReference) (bool, error) {
	if a.Client == nil {
		return false, network.ErrUnsupported
	}
	claim := &networkingv1.NetworkInterfaceClaim{}
	err := a.Client.Get(ctx, client.ObjectKey{Namespace: recorded.Namespace, Name: recorded.Name}, claim)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if recorded.UID != "" && string(claim.UID) != recorded.UID {
		return true, nil
	}
	if claim.DeletionTimestamp == nil {
		uid := claim.UID
		if err := a.Client.Delete(ctx, claim, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return false, nil
}

func normalizeReclaim(value string) (string, bool) {
	switch value {
	case "", "Delete":
		return "Delete", true
	case "Retain":
		return "Retain", true
	}
	return "", false
}

// spec compares claim specs with NSO's defaults applied.
func spec(value networkingv1.NetworkInterfaceClaimSpec) string {
	if len(value.IPFamilies) == 0 {
		value.IPFamilies = []networkingv1.IPFamily{networkingv1.IPv6Protocol}
	}
	if value.ReclaimPolicy == "" {
		value.ReclaimPolicy = networkingv1.NetworkInterfaceReclaimPolicyDelete
	}
	if value.InterfaceName == "" {
		value.InterfaceName = "eth0"
	}
	b, _ := json.Marshal(value)
	return string(b)
}
