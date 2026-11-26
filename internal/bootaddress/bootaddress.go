// Package bootaddress claims a provisioning address from IPAM for one
// network-booting operation, and checks a claimed address at admission.
//
// The requester of an installation or wipe claims the address before it
// creates the operation, under a name derived from the operation, so a retry
// finds the same claim. It releases the claim once the operation can no
// longer reach DHCP: after the operation ends, or, for an unknown outcome,
// after its recovery hold is settled. Fleet reads only the claim's bound
// address; the gateway and prefix stay in the DHCP owner's base
// configuration.
//
// It follows IPAM's claim contract: IPAM binds a claim, or refuses it, in the
// create response; a second create under the same name is a conflict; and a
// delete releases the address and removes the claim and its allocation in
// one transaction, ignoring delete preconditions. Fleet uses only claims
// whose stored reclaim policy is Delete, which overrides the class's: a claim
// IPAM retained would leave its address allocated after the claim is gone.
package bootaddress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// GVK is IPAM's address claim.
var GVK = schema.GroupVersionKind{Group: "ipam.miloapis.com", Version: "v1alpha1", Kind: "IPClaim"}

// AllocationGVK is IPAM's record of an address held for a claim.
var AllocationGVK = schema.GroupVersionKind{Group: "ipam.miloapis.com", Version: "v1alpha1", Kind: "IPAllocation"}

// Labels name the allocation and operation a claim belongs to.
const (
	AllocationLabel = "fleet.datumapis.com/allocation"
	OperationLabel  = "fleet.datumapis.com/operation"
	KindLabel       = "fleet.datumapis.com/operation-kind"
)

var (
	// ErrPending means IPAM has not bound the claim yet.
	ErrPending = errors.New("the boot address claim is not bound yet")
	// ErrUnavailable means the claim is missing, another incarnation, or
	// bound to another address.
	ErrUnavailable = errors.New("the boot address claim does not hold the admitted address")
	// ErrConflict means a claim with the operation's name has other inputs.
	ErrConflict = errors.New("the boot address claim has other inputs")
	// ErrExhausted means IPAM has no free provisioning address. Nothing was
	// claimed, so the operation must not be created.
	ErrExhausted = errors.New("IPAM has no free provisioning address")
	// ErrRefused means IPAM refused the claim, for example for a class or
	// scope it does not know. Nothing was claimed.
	ErrRefused = errors.New("IPAM refused the boot address claim")
	// ErrUnreachable means IPAM could not be read, so a claim's state is
	// unknown. Admission waits rather than deciding.
	ErrUnreachable = errors.New("IPAM could not be read")
	// ErrReleaseUnverified means the claim is gone but IPAM still records an
	// allocation for it.
	ErrReleaseUnverified = errors.New("the boot address is still allocated")
)

// ipamCause returns the first IPAM cause type of an API error, such as
// ClaimExists or Exhausted, and the error's status.
func ipamCause(err error) (string, *apierrors.StatusError) {
	var status *apierrors.StatusError
	if !errors.As(err, &status) {
		return "", nil
	}
	if details := status.ErrStatus.Details; details != nil {
		for _, cause := range details.Causes {
			if cause.Type != "" {
				return string(cause.Type), status
			}
		}
	}
	return "", status
}

// createError classifies IPAM's refusal of a claim create. IPAM reports
// exhaustion as 507, with an Exhausted cause and the pool's name when one
// pool ran out, and other refusals of the claim as 400, 409 or 422 with a
// cause. A ClaimExists conflict is handled before this.
func createError(err error) error {
	cause, status := ipamCause(err)
	switch {
	case status == nil:
		return err
	case status.ErrStatus.Code == http.StatusInsufficientStorage || cause == "Exhausted":
		if details := status.ErrStatus.Details; details != nil && details.Name != "" {
			return fmt.Errorf("%w: pool %s is full", ErrExhausted, details.Name)
		}
		return ErrExhausted
	case apierrors.IsBadRequest(err) || apierrors.IsInvalid(err) || apierrors.IsConflict(err):
		return fmt.Errorf("%w: %s", ErrRefused, status.ErrStatus.Message)
	}
	return err
}

// claimExists reports whether a create lost to a claim of the same name.
// IPAM answers 409 Conflict with a ClaimExists cause.
func claimExists(err error) bool {
	cause, _ := ipamCause(err)
	return apierrors.IsAlreadyExists(err) || (apierrors.IsConflict(err) && cause == "ClaimExists")
}

// ClaimName is the only name an operation's boot address claim may have.
func ClaimName(operation string) string { return operation + "-boot" }

// ScopeRef is one IPAM scope reference, such as the site's location.
type ScopeRef struct {
	APIGroup string
	Kind     string
	Name     string
}

// Claimer claims and releases boot addresses in the provider project, for
// one requester and one operation kind. Each requester releases only the
// claims it made.
type Claimer struct {
	// Kind is the operation kind the requester creates.
	Kind string
	// Client must read through to the API server.
	Client client.Client
	// Namespace is the operations' namespace.
	Namespace string
	// Class is the network owner's provisioning IPAM class.
	Class string
	// Scope is passed to IPAM as the claim's scope.
	Scope map[string]ScopeRef
}

func (c Claimer) desired(allocation, operation string) *unstructured.Unstructured {
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(GVK)
	claim.SetNamespace(c.Namespace)
	claim.SetName(ClaimName(operation))
	claim.SetLabels(map[string]string{AllocationLabel: allocation, OperationLabel: operation, KindLabel: c.Kind})
	spec := map[string]any{"className": c.Class, "ipFamily": "IPv4", "prefixLength": int64(32), "reclaimPolicy": "Delete"}
	if len(c.Scope) > 0 {
		scope := map[string]any{}
		for role, ref := range c.Scope {
			scope[role] = map[string]any{"apiGroup": ref.APIGroup, "kind": ref.Kind, "name": ref.Name}
		}
		spec["scope"] = scope
	}
	claim.Object["spec"] = spec
	return claim
}

// Claim returns the operation's bound boot address, creating its claim once.
// It returns ErrPending until IPAM binds the claim, and ErrExhausted or
// ErrRefused when IPAM refuses it.
func (c Claimer) Claim(ctx context.Context, allocation, operation string) (*ops.BootNetworkInput, error) {
	if c.Client == nil || c.Namespace == "" || c.Class == "" || c.Kind == "" || allocation == "" || operation == "" {
		return nil, errors.New("bootaddress: a client, namespace, class, kind, allocation and operation are required")
	}
	desired := c.desired(allocation, operation)
	claim, err := c.get(ctx, desired.GetName())
	if apierrors.IsNotFound(err) {
		if err := c.Client.Create(ctx, desired); err != nil && !claimExists(err) {
			return nil, createError(err)
		}
		claim, err = c.get(ctx, desired.GetName())
	}
	if err != nil {
		return nil, err
	}
	if labels := claim.GetLabels(); labels[OperationLabel] != operation || labels[AllocationLabel] != allocation || labels[KindLabel] != c.Kind || !deletes(claim) {
		return nil, fmt.Errorf("%w: %s", ErrConflict, claim.GetName())
	}
	address, err := bound(claim)
	if err != nil {
		return nil, err
	}
	return &ops.BootNetworkInput{AddressClaim: ops.LocalReference{Name: claim.GetName()}, Address: address}, nil
}

// Release deletes the operation's claim. It returns true only once the
// release is verified: the claim is gone and IPAM records no allocation for
// it. The claim's reclaim policy is Delete, so IPAM removes its allocation in
// the transaction that removes the claim, and a claim found gone, even after
// a delete whose response was lost, was released. A delete that fails leaves
// the claim, and its address, held for a later call. IPAM ignores delete
// preconditions, so the claim's name and labels are the guard: a claim under
// the operation's name with its labels is that operation's claim.
func (c Claimer) Release(ctx context.Context, operation string) (bool, error) {
	claim, err := c.get(ctx, ClaimName(operation))
	if apierrors.IsNotFound(err) {
		return c.unallocated(ctx, ClaimName(operation))
	}
	if err != nil {
		return false, err
	}
	if claim.GetLabels()[OperationLabel] != operation || claim.GetLabels()[KindLabel] != c.Kind {
		return false, fmt.Errorf("%w: %s", ErrConflict, claim.GetName())
	}
	if err := c.Client.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if _, err := c.get(ctx, claim.GetName()); !apierrors.IsNotFound(err) {
		return false, err
	}
	return c.unallocated(ctx, claim.GetName())
}

// unallocated reports whether no IPAM allocation names the claim.
func (c Claimer) unallocated(ctx context.Context, claim string) (bool, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(AllocationGVK.GroupVersion().WithKind(AllocationGVK.Kind + "List"))
	if err := c.Client.List(ctx, list, client.InNamespace(c.Namespace)); err != nil {
		return false, err
	}
	for _, allocation := range list.Items {
		if stringAt(&allocation, "spec", "claimRef", "name") == claim {
			return false, fmt.Errorf("%w: %s holds it for %s", ErrReleaseUnverified, allocation.GetName(), claim)
		}
	}
	return true, nil
}

// Operations returns the operations of the claimer's kind in an allocation
// that still have a boot address claim.
func (c Claimer) Operations(ctx context.Context, allocation string) ([]string, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(GVK.GroupVersion().WithKind(GVK.Kind + "List"))
	if err := c.Client.List(ctx, list, client.InNamespace(c.Namespace), client.MatchingLabels{AllocationLabel: allocation, KindLabel: c.Kind}); err != nil {
		return nil, err
	}
	var operations []string
	for _, claim := range list.Items {
		if operation := claim.GetLabels()[OperationLabel]; operation != "" && claim.GetName() == ClaimName(operation) {
			operations = append(operations, operation)
		}
	}
	return operations, nil
}

func (c Claimer) get(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(GVK)
	return claim, c.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, claim)
}

// Verify requires the input to name the operation's own claim, by its only
// name and its labels, and that claim to be bound to exactly the input's
// address. The requester creates the claim before the operation, so a claim
// created after its operation is a recreation, never the claim the operation
// was built from, and is refused. Admission calls it with the provider
// project's reader. It returns ErrUnreachable when the claim cannot be read
// for any reason but its absence, and ErrUnavailable otherwise.
func Verify(ctx context.Context, reader client.Reader, operation ops.Operation, kind string, input ops.BootNetworkInput) error {
	if reader == nil {
		return ErrUnavailable
	}
	namespace := operation.GetNamespace()
	if input.AddressClaim.Name != ClaimName(operation.GetName()) {
		return fmt.Errorf("%w: the claim is not the operation's own", ErrUnavailable)
	}
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(GVK)
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: input.AddressClaim.Name}, claim); apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	} else if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	created, opened := claim.GetCreationTimestamp(), operation.GetCreationTimestamp()
	if claim.GetDeletionTimestamp() != nil || created.IsZero() || opened.IsZero() || created.After(opened.Time) {
		return fmt.Errorf("%w: the claim was recreated after its operation or is being deleted", ErrUnavailable)
	}
	if labels := claim.GetLabels(); labels[OperationLabel] != operation.GetName() || labels[KindLabel] != kind {
		return fmt.Errorf("%w: the claim belongs to another operation", ErrUnavailable)
	}
	if !deletes(claim) {
		return fmt.Errorf("%w: the claim would retain its address after release", ErrUnavailable)
	}
	address, err := bound(claim)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	want, err := netip.ParseAddr(input.Address)
	if err != nil || want.String() != address {
		return fmt.Errorf("%w: the claim holds another address", ErrUnavailable)
	}
	return nil
}

// deletes reports whether IPAM stored the claim with reclaim policy Delete.
func deletes(claim *unstructured.Unstructured) bool {
	return stringAt(claim, "spec", "reclaimPolicy") == "Delete"
}

// bound returns a bound claim's IPv4 host address in canonical form.
func bound(claim *unstructured.Unstructured) (string, error) {
	if stringAt(claim, "status", "phase") != "Bound" {
		return "", ErrPending
	}
	text := stringAt(claim, "status", "address")
	if text == "" {
		if prefix, err := netip.ParsePrefix(stringAt(claim, "status", "allocatedCIDR")); err == nil && prefix.IsSingleIP() {
			text = prefix.Addr().String()
		}
	}
	address, err := netip.ParseAddr(text)
	if err != nil || !address.Is4() || address.Zone() != "" {
		return "", errors.New("the claim is not bound to one IPv4 host address")
	}
	return address.String(), nil
}

func stringAt(object *unstructured.Unstructured, fields ...string) string {
	value, _, _ := unstructured.NestedString(object.Object, fields...)
	return value
}
