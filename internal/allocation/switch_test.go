package allocation

import (
	"errors"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
)

// TestASwitchedSourceStillReleasesItsOldClaims installs through IPAM, then
// switches the allocator to NSO. The installation's IPAM claim is still
// released, by IPAM's verified release, and the release does not report
// done while IPAM holds the address. Each source lists and releases only its
// own claim kind.
func TestASwitchedSourceStillReleasesItsOldClaims(t *testing.T) {
	e, _ := nsoEnv(t)
	recorder := &ipam{Client: e.admin}
	e.allocator.Provider = recorder
	nsoSource := e.allocator.BootAddresses
	e.allocator.BootAddresses = bootaddress.Claimer{Class: "provisioning"}
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	operation := e.installed(allocation, Installation{Installation: e.installation(1, "debian-12", "")})
	if claim := operation.Spec.BootNetwork.AddressClaim; claim != bootaddress.Reference(bootaddress.ClaimName(operation.Name)) {
		t.Fatalf("claim = %+v, want IPAM's", claim)
	}
	// IPAM records the allocation for the claim. Its delete would remove it
	// in the same transaction; the stand-in API does not, so the test does.
	ipamAllocation := &unstructured.Unstructured{}
	ipamAllocation.SetGroupVersionKind(bootaddress.AllocationGVK)
	ipamAllocation.SetNamespace(namespace)
	ipamAllocation.SetName("alloc-0123456789abcdef")
	ipamAllocation.Object["spec"] = map[string]any{"claimRef": map[string]any{"name": bootaddress.ClaimName(operation.Name)}}
	e.create(ipamAllocation)

	// The operator switches the allocator to NSO, and a later operation
	// already holds an NSO claim.
	e.allocator.BootAddresses = nsoSource
	if err := e.allocator.installClaims().Attach(e.ctx, allocation, allocation.Name+"-install-next"); !errors.Is(err, bootaddress.ErrPending) {
		t.Fatal(err)
	}
	ipamClaims := bootaddress.Claimer{}.Requester(e.admin, namespace, "DeviceInstallation")
	nsoClaims := e.allocator.installClaims()
	if names, err := ipamClaims.Operations(e.ctx, allocation.Name); err != nil || !slices.Equal(names, []string{operation.Name}) {
		t.Fatalf("IPAM lists %v, %v; want only the IPAM claim", names, err)
	}
	if names, err := nsoClaims.Operations(e.ctx, allocation.Name); err != nil || !slices.Equal(names, []string{allocation.Name + "-install-next"}) {
		t.Fatalf("NSO lists %v, %v; want only the NSO claim", names, err)
	}

	// The IPAM claim is deleted, but IPAM still holds its address: not done.
	none, err := e.allocator.HandOver(e.ctx, allocation)
	if none || !errors.Is(err, bootaddress.ErrReleaseUnverified) {
		t.Fatalf("hand over while IPAM holds the old address: none=%v %v", none, err)
	}
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(bootaddress.GVK)
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: bootaddress.ClaimName(operation.Name)}, claim); !apierrors.IsNotFound(err) {
		t.Fatalf("the old IPAM claim was not deleted: %v", err)
	}
	if err := e.admin.Delete(e.ctx, ipamAllocation); err != nil {
		t.Fatal(err)
	}
	// Verified: the IPAM claim is gone and no allocation names it. The NSO
	// claim's operation was never created, so it stays, and IPAM never
	// touched it.
	if none, err := e.allocator.HandOver(e.ctx, allocation); err != nil || none {
		t.Fatalf("hand over after IPAM released: none=%v %v, want the NSO claim kept", none, err)
	}
	if names, err := ipamClaims.Operations(e.ctx, allocation.Name); err != nil || len(names) != 0 {
		t.Fatalf("IPAM claims left: %v, %v", names, err)
	}
	if names, err := nsoClaims.Operations(e.ctx, allocation.Name); err != nil || len(names) != 1 {
		t.Fatalf("NSO claims = %v, %v; want the uncreated operation's kept", names, err)
	}
	var installations ops.DeviceInstallationList
	if err := e.admin.List(e.ctx, &installations, client.InNamespace(namespace)); err != nil || len(installations.Items) != 1 {
		t.Fatalf("installations = %d, %v", len(installations.Items), err)
	}
}
