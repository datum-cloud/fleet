package allocation

import (
	"context"
	"errors"
	"slices"
	"testing"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	"go.datum.net/fleet/internal/bootaddress"
)

// TestAnNSOReleaseIsVerifiedAfterTheClaimIsGone installs through NSO, then
// NSO drops the boot attachment claim while IPAM still holds the address
// NSO claimed for it. The release is checked again from the allocator's own
// record of the operation, and does not finish until IPAM's allocation is
// gone. After a switch back to IPAM, a boot attachment claim NSO still holds
// keeps reporting that it holds the boot NIC.
func TestAnNSOReleaseIsVerifiedAfterTheClaimIsGone(t *testing.T) {
	e, _ := nsoEnv(t)
	r := newRealizer(e.admin)
	ctx, stop := context.WithCancel(e.ctx)
	go r.run(ctx)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	operation := e.installed(allocation, Installation{Installation: e.installation(1, "debian-12", "")})
	stop()
	claimName := bootaddress.ClaimName(operation.Name)
	var allocations unstructured.UnstructuredList
	allocations.SetGroupVersionKind(bootaddress.AllocationGVK.GroupVersion().WithKind("IPAllocationList"))
	if err := e.admin.List(e.ctx, &allocations, client.InNamespace(ipamNamespace)); err != nil || len(allocations.Items) != 1 {
		t.Fatalf("IPAM allocations = %d, %v", len(allocations.Items), err)
	}
	// NSO's finalizer clears and the claim goes, but IPAM still holds the
	// address.
	claim := &networkingv1.NetworkInterfaceClaim{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: claimName}, claim); err != nil {
		t.Fatal(err)
	}
	claim.Finalizers = nil
	if err := e.admin.Update(e.ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.Delete(e.ctx, claim); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if none, err := e.allocator.HandOver(e.ctx, allocation); none || !errors.Is(err, bootaddress.ErrReleaseUnverified) {
			t.Fatalf("hand over %d while IPAM holds the address of a claim NSO dropped: none=%v %v", i, none, err)
		}
	}
	if err := e.admin.Delete(e.ctx, &allocations.Items[0]); err != nil {
		t.Fatal(err)
	}
	if none, err := e.allocator.HandOver(e.ctx, allocation); err != nil || !none {
		t.Fatalf("hand over once IPAM released: none=%v %v", none, err)
	}

	// Switch back to IPAM while NSO still holds a boot attachment claim for
	// a later operation: the claim still holds the boot NIC.
	nsoSource := e.allocator.BootAddresses
	if err := e.allocator.installClaims().Attach(e.ctx, allocation, allocation.Name+"-install-next"); !errors.Is(err, bootaddress.ErrPending) {
		t.Fatal(err)
	}
	e.allocator.BootAddresses = bootaddress.Claimer{Class: "provisioning"}
	e.allocator.PreviousBootAddresses = []bootaddress.Source{nsoSource}
	if err := e.allocator.BootNIC(e.ctx, allocation, []string{"primary"}); !errors.Is(err, ErrBootAttached) {
		t.Fatalf("BootNIC after switching back to IPAM = %v, want the NSO claim reported", err)
	}
}

// noIPAM is a provider API that does not serve IPAM's kinds.
type noIPAM struct{ client.Client }

func (c noIPAM) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if list.GetObjectKind().GroupVersionKind().Group == bootaddress.GVK.Group {
		return &apiMeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: bootaddress.GVK.Group, Kind: bootaddress.GVK.Kind}, SearchedVersions: []string{"v1alpha1"}}
	}
	return c.Client.List(ctx, list, opts...)
}

// TestNSOReleaseNeedsNoIPAMAPI releases an NSO boot attachment on a provider
// API that does not serve IPAM's claims: IPAM's default claimer has nothing
// to release there. NSO's own IPAM is read through its own reader.
func TestNSOReleaseNeedsNoIPAMAPI(t *testing.T) {
	e, _ := nsoEnv(t)
	r := newRealizer(e.admin)
	go r.run(e.ctx)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	operation := e.installed(allocation, Installation{Installation: e.installation(1, "debian-12", "")})
	e.allocator.Provider, e.allocator.Releaser = noIPAM{e.admin}, noIPAM{e.admin}
	var last error
	e.until("the NSO boot attachment is released", func() bool {
		none, err := e.allocator.HandOver(e.ctx, allocation)
		last = err
		if apiMeta.IsNoMatchError(err) {
			t.Fatalf("hand over without IPAM's API: %v", err)
		}
		return none && err == nil
	})
	if names, err := e.allocator.installClaims().Operations(e.ctx, allocation.Name); err != nil || slices.Contains(names, operation.Name) || last != nil {
		t.Fatalf("NSO claims = %v, %v, %v", names, err, last)
	}
}
