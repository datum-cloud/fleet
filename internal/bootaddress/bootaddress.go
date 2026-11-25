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
package bootaddress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// GVK is IPAM's address claim.
var GVK = schema.GroupVersionKind{Group: "ipam.miloapis.com", Version: "v1alpha1", Kind: "IPClaim"}

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
)

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
// It returns ErrPending until IPAM binds the claim.
func (c Claimer) Claim(ctx context.Context, allocation, operation string) (*ops.BootNetworkInput, error) {
	if c.Client == nil || c.Namespace == "" || c.Class == "" || c.Kind == "" || allocation == "" || operation == "" {
		return nil, errors.New("bootaddress: a client, namespace, class, kind, allocation and operation are required")
	}
	desired := c.desired(allocation, operation)
	claim, err := c.get(ctx, desired.GetName())
	if apierrors.IsNotFound(err) {
		if err := c.Client.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		claim, err = c.get(ctx, desired.GetName())
	}
	if err != nil {
		return nil, err
	}
	if labels := claim.GetLabels(); labels[OperationLabel] != operation || labels[AllocationLabel] != allocation || labels[KindLabel] != c.Kind {
		return nil, fmt.Errorf("%w: %s", ErrConflict, claim.GetName())
	}
	address, err := bound(claim)
	if err != nil {
		return nil, err
	}
	return &ops.BootNetworkInput{AddressClaim: ops.LocalReference{Name: claim.GetName()}, Address: address}, nil
}

// Release deletes the operation's claim. It returns true once the claim is
// gone.
func (c Claimer) Release(ctx context.Context, operation string) (bool, error) {
	claim, err := c.get(ctx, ClaimName(operation))
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if claim.GetLabels()[OperationLabel] != operation || claim.GetLabels()[KindLabel] != c.Kind {
		return false, fmt.Errorf("%w: %s", ErrConflict, claim.GetName())
	}
	if claim.GetDeletionTimestamp() == nil {
		uid := claim.GetUID()
		if err := c.Client.Delete(ctx, claim, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return false, nil
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
// project's reader.
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
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: input.AddressClaim.Name}, claim); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	created, opened := claim.GetCreationTimestamp(), operation.GetCreationTimestamp()
	if claim.GetDeletionTimestamp() != nil || created.IsZero() || opened.IsZero() || created.After(opened.Time) {
		return fmt.Errorf("%w: the claim was recreated after its operation or is being deleted", ErrUnavailable)
	}
	if labels := claim.GetLabels(); labels[OperationLabel] != operation.GetName() || labels[KindLabel] != kind {
		return fmt.Errorf("%w: the claim belongs to another operation", ErrUnavailable)
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
