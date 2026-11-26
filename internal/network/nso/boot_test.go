package nso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
)

const (
	bootNamespace = "fleet-provider"
	ipamNamespace = "ipam-provider"
	bootNetwork   = "boot"
	location      = "location-example-a"
)

// bootFixture is the provider's NSO API with one cabled Device, and an
// allocation of it whose boot NIC is primary.
func bootFixture(t *testing.T, funcs interceptor.Funcs) (BootAttachments, client.Client, *fleetv1.MachineAllocation) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{networkingv1.AddToScheme, inventory.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	scheme.AddKnownTypeWithName(bootaddress.AllocationGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(bootaddress.AllocationGVK.GroupVersion().WithKind("IPAllocationList"), &unstructured.UnstructuredList{})
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server-example-001", Namespace: bootNamespace, UID: "00000000-0000-4000-8000-000000000101"},
		Spec: inventory.DeviceSpec{Interfaces: []inventory.Interface{{Name: "primary", MACAddress: "02:00:00:00:00:01"}}}}
	cable := &inventory.Cable{ObjectMeta: metav1.ObjectMeta{Name: "cable-example-1", Namespace: bootNamespace}, Spec: inventory.CableSpec{Lanes: []inventory.CableLane{{ID: "one",
		A: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: device.Name}, ComponentID: "primary"},
		B: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: "switch-example-a"}, ComponentID: "ethernet7"}}}}}
	api := fake.NewClientBuilder().WithScheme(scheme).WithObjects(device, cable).WithStatusSubresource(&networkingv1.NetworkInterfaceClaim{}).WithInterceptorFuncs(funcs).Build()
	allocation := &fleetv1.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: "alloc-example", Namespace: bootNamespace},
		Spec: fleetv1.MachineAllocationSpec{DeviceRef: fleetv1.AllocatedDeviceReference{Name: device.Name, UID: string(device.UID)}, BootInterface: "primary"}}
	source := BootAttachments{Network: bootNetwork, IPAM: api, IPAMNamespace: ipamNamespace, Location: location}
	return source.Requester(api, bootNamespace, "DeviceInstallation").(BootAttachments), api, allocation
}

func bootClaim(t *testing.T, api client.Client, operation string) *networkingv1.NetworkInterfaceClaim {
	t.Helper()
	claim := &networkingv1.NetworkInterfaceClaim{}
	if err := api.Get(context.Background(), client.ObjectKey{Namespace: bootNamespace, Name: bootaddress.ClaimName(operation)}, claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

// report writes what NSO reports for a claim: its addresses and conditions.
func report(t *testing.T, api client.Client, claim *networkingv1.NetworkInterfaceClaim, address string, conditions map[string]metav1.Condition) {
	t.Helper()
	if err := api.Get(context.Background(), client.ObjectKeyFromObject(claim), claim); err != nil {
		t.Fatal(err)
	}
	claim.Status.Addresses = nil
	if address != "" {
		claim.Status.Addresses = []networkingv1.NetworkInterfaceAddress{{Family: networkingv1.IPv4Protocol, Address: address, Gateway: "198.51.100.1", Primary: true}}
	}
	for kind, condition := range conditions {
		condition.Type, condition.ObservedGeneration = kind, claim.Generation
		if condition.Reason == "" {
			condition.Reason = "Test"
		}
		apiMeta.SetStatusCondition(&claim.Status.Conditions, condition)
	}
	if err := api.Status().Update(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
}

var programmedTrue = map[string]metav1.Condition{
	networkingv1.NetworkInterfaceClaimAllocated:  {Status: metav1.ConditionTrue},
	networkingv1.NetworkInterfaceClaimPrepared:   {Status: metav1.ConditionTrue},
	networkingv1.NetworkInterfaceClaimProgrammed: {Status: metav1.ConditionTrue},
}

// TestBootAttachmentIsTheOperationsOwnClaim creates one Physical claim per
// operation on the boot network, named from the operation, for the boot
// NIC's attachment point from inventory, and waits until NSO programs it.
func TestBootAttachmentIsTheOperationsOwnClaim(t *testing.T) {
	ctx := context.Background()
	source, api, allocation := bootFixture(t, interceptor.Funcs{})
	if err := source.Attach(ctx, allocation, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrPending) {
		t.Fatalf("attach before NSO programs the port: %v", err)
	}
	claim := bootClaim(t, api, "alloc-example-install-1")
	want := AttachmentPoint{MACAddress: "02:00:00:00:00:01", Switch: "switch-example-a", Port: "ethernet7"}
	if claim.Spec.AttachmentMode != PhysicalMode || claim.Spec.Network.Name != bootNetwork || AttachmentPointOf(claim) != want ||
		claim.Labels[bootaddress.AllocationLabel] != allocation.Name || claim.Labels[bootaddress.OperationLabel] != "alloc-example-install-1" || claim.Labels[bootaddress.KindLabel] != "DeviceInstallation" {
		t.Fatalf("claim = %+v", claim)
	}
	report(t, api, claim, "198.51.100.10/24", programmedTrue)
	if err := source.Attach(ctx, allocation, "alloc-example-install-1"); err != nil {
		t.Fatalf("attach once programmed: %v", err)
	}
	// Another operation, such as a retry, has its own claim.
	if err := source.Attach(ctx, allocation, "alloc-example-install-1-retry-1"); !errors.Is(err, bootaddress.ErrPending) {
		t.Fatal(err)
	}
	if names, err := source.Operations(ctx, allocation.Name); err != nil || len(names) != 2 || names[0] == names[1] {
		t.Fatalf("operations = %v, %v", names, err)
	}
	// Another requester's claim under the name is a conflict, not its own.
	wipes := source.Requester(api, bootNamespace, "DeviceWipe")
	if err := wipes.Attach(ctx, allocation, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrConflict) {
		t.Fatalf("a requester adopted another's claim: %v", err)
	}
	if _, err := wipes.Release(ctx, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrConflict) {
		t.Fatalf("a requester released another's claim: %v", err)
	}
}

// TestNoAddressIsClaimedBeforeTheBootNICIsProgrammed reads no address while
// the port is not on the boot network, even one NSO already bound.
func TestNoAddressIsClaimedBeforeTheBootNICIsProgrammed(t *testing.T) {
	ctx := context.Background()
	source, api, allocation := bootFixture(t, interceptor.Funcs{})
	if _, err := source.Claim(ctx, allocation.Name, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrPending) {
		t.Fatalf("claim before attach: %v", err)
	}
	if err := source.Attach(ctx, allocation, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrPending) {
		t.Fatal(err)
	}
	claim := bootClaim(t, api, "alloc-example-install-1")
	report(t, api, claim, "198.51.100.10/24", map[string]metav1.Condition{networkingv1.NetworkInterfaceClaimAllocated: {Status: metav1.ConditionTrue}})
	if input, err := source.Claim(ctx, allocation.Name, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrPending) {
		t.Fatalf("an address was claimed before the port was programmed: %+v, %v", input, err)
	}
	report(t, api, claim, "198.51.100.10/24", programmedTrue)
	input, err := source.Claim(ctx, allocation.Name, "alloc-example-install-1")
	if err != nil || input.Address != "198.51.100.10" || input.AddressClaim != BootReference(claim.Name) {
		t.Fatalf("input = %+v, %v", input, err)
	}
	if input.AddressClaim.APIGroup != networkingv1.GroupVersion.Group {
		t.Fatalf("reference group %q is not NSO's", input.AddressClaim.APIGroup)
	}
}

// TestNSORefusalsAreReported maps NSO's report that IPAM refused the address
// to the same errors IPAM's refusals give, so no operation is created, and
// waits out a failure NSO retries.
func TestNSORefusalsAreReported(t *testing.T) {
	ctx := context.Background()
	for reason, want := range map[string]error{
		"AddressPoolExhausted":      bootaddress.ErrExhausted,
		"RetainedAddressConflict":   bootaddress.ErrRefused,
		"AddressAllocationRejected": bootaddress.ErrRefused,
		"AllocationFailed":          bootaddress.ErrPending,
		"SomethingNSOAddsLater":     bootaddress.ErrPending,
	} {
		source, api, allocation := bootFixture(t, interceptor.Funcs{})
		_ = source.Attach(ctx, allocation, "alloc-example-install-1")
		report(t, api, bootClaim(t, api, "alloc-example-install-1"), "", map[string]metav1.Condition{
			networkingv1.NetworkInterfaceClaimAllocated: {Status: metav1.ConditionFalse, Reason: reason, Message: `IPPool "lab-provisioning" is exhausted`},
		})
		if err := source.Attach(ctx, allocation, "alloc-example-install-1"); !errors.Is(err, want) {
			t.Errorf("%s: attach = %v, want %v", reason, err, want)
		}
		_, err := source.Claim(ctx, allocation.Name, "alloc-example-install-1")
		if !errors.Is(err, want) {
			t.Errorf("%s: claim = %v, want %v", reason, err, want)
		}
		// NSO retries a failure it does not classify; the wait says why.
		if reason == "AllocationFailed" && (!strings.Contains(err.Error(), "AllocationFailed") || !strings.Contains(err.Error(), "lab-provisioning")) {
			t.Errorf("AllocationFailed: %v, want NSO's reason and message", err)
		}
	}
}

// TestBootAttachmentCheckNeedsNSOsTypes refuses to make the check for an
// admission reader whose scheme does not know NSO's claim, which would leave
// every operation Pending, and a check not made by its constructor fails
// closed.
func TestBootAttachmentCheckNeedsNSOsTypes(t *testing.T) {
	if _, err := NewBootAttachmentCheck(runtime.NewScheme(), "", bootNetwork); err == nil {
		t.Fatal("a check was made for a reader that cannot read NSO's claims")
	}
	if _, err := NewBootAttachmentCheck(nil, "", bootNetwork); err == nil {
		t.Fatal("a check was made with no scheme")
	}
	scheme := runtime.NewScheme()
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBootAttachmentCheck(scheme, "", ""); err == nil {
		t.Fatal("a check was made with no boot network")
	}
	if _, err := NewBootAttachmentCheck(scheme, "", bootNetwork); err != nil {
		t.Fatal(err)
	}
	operation := &ops.DeviceInstallation{}
	operation.Name, operation.Namespace = "alloc-example-install-1", bootNamespace
	_, api, _ := bootFixture(t, interceptor.Funcs{})
	if err := (BootAttachmentCheck{}).Verify(context.Background(), api, operation, "DeviceInstallation", ops.BootNetworkInput{}); !errors.Is(err, bootaddress.ErrUnavailable) {
		t.Fatalf("a zero check: %v", err)
	}
}

// TestNSOUnavailableAttachesNothing returns NSO's outage, never a pending
// attachment or an address, and creates nothing.
func TestNSOUnavailableAttachesNothing(t *testing.T) {
	ctx := context.Background()
	down := func(object client.Object) bool { _, ok := object.(*networkingv1.NetworkInterfaceClaim); return ok }
	source, api, allocation := bootFixture(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
			if down(object) {
				return apierrors.NewServiceUnavailable("nso is down")
			}
			return c.Get(ctx, key, object, opts...)
		},
	})
	err := source.Attach(ctx, allocation, "alloc-example-install-1")
	if err == nil || errors.Is(err, bootaddress.ErrPending) || !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("attach while NSO is down: %v", err)
	}
	if _, err := source.Claim(ctx, allocation.Name, "alloc-example-install-1"); err == nil || errors.Is(err, bootaddress.ErrPending) {
		t.Fatalf("claim while NSO is down: %v", err)
	}
	var claims networkingv1.NetworkInterfaceClaimList
	if err := api.List(ctx, &claims); err != nil || len(claims.Items) != 0 {
		t.Fatalf("claims = %d, %v", len(claims.Items), err)
	}
}

// TestBootAttachmentCheckRequiresTheExactProgrammedClaim admits only the
// operation's own claim, created no later than the operation, programmed on
// the boot network and bound to the input's address; waits while NSO cannot
// be read; and refuses everything else.
func TestBootAttachmentCheckRequiresTheExactProgrammedClaim(t *testing.T) {
	ctx := context.Background()
	down := false
	source, api, allocation := bootFixture(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
			if _, ok := object.(*networkingv1.NetworkInterfaceClaim); ok && down {
				return apierrors.NewServiceUnavailable("nso is down")
			}
			return c.Get(ctx, key, object, opts...)
		},
	})
	check, err := NewBootAttachmentCheck(api.Scheme(), "", bootNetwork)
	if err != nil {
		t.Fatal(err)
	}
	_ = source.Attach(ctx, allocation, "alloc-example-install-1")
	claim := bootClaim(t, api, "alloc-example-install-1")
	created := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	claim.CreationTimestamp = metav1.NewTime(created)
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	report(t, api, claim, "198.51.100.10/24", programmedTrue)
	operation := &ops.DeviceInstallation{}
	operation.Name, operation.Namespace = "alloc-example-install-1", bootNamespace
	operation.CreationTimestamp = metav1.NewTime(created.Add(time.Second))
	good := ops.BootNetworkInput{AddressClaim: BootReference(claim.Name), Address: "198.51.100.10"}
	if err := check.Verify(ctx, api, operation, "DeviceInstallation", good); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, operation ops.Operation, kind string, input ops.BootNetworkInput) {
		t.Helper()
		if err := check.Verify(ctx, api, operation, kind, input); !errors.Is(err, bootaddress.ErrUnavailable) || errors.Is(err, bootaddress.ErrUnreachable) {
			t.Errorf("%s: %v", what, err)
		}
	}
	other := &ops.DeviceInstallation{ObjectMeta: *operation.ObjectMeta.DeepCopy()}
	other.Name = "alloc-example-install-2"
	refused("another operation", other, "DeviceInstallation", good)
	refused("another kind", operation, "DeviceWipe", good)
	refused("another address", operation, "DeviceInstallation", ops.BootNetworkInput{AddressClaim: good.AddressClaim, Address: "198.51.100.11"})
	refused("IPAM's kind", operation, "DeviceInstallation", ops.BootNetworkInput{AddressClaim: bootaddress.Reference(claim.Name), Address: good.Address})
	if other, _ := NewBootAttachmentCheck(api.Scheme(), "", "customer-a"); !errors.Is(other.Verify(ctx, api, operation, "DeviceInstallation", good), bootaddress.ErrUnavailable) {
		t.Errorf("a claim on another network verified: %v", err)
	}
	// Recreated after the operation, even with the same address.
	claim.CreationTimestamp = metav1.NewTime(created.Add(2 * time.Second))
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	refused("recreated", operation, "DeviceInstallation", good)
	claim.CreationTimestamp = metav1.NewTime(created)
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	// Not programmed: the port is not on the boot network.
	report(t, api, claim, "198.51.100.10/24", map[string]metav1.Condition{networkingv1.NetworkInterfaceClaimProgrammed: {Status: metav1.ConditionFalse}})
	refused("not programmed", operation, "DeviceInstallation", good)
	report(t, api, claim, "198.51.100.10/24", programmedTrue)
	if err := check.Verify(ctx, api, operation, "DeviceInstallation", good); err != nil {
		t.Fatal(err)
	}
	down = true
	if err := check.Verify(ctx, api, operation, "DeviceInstallation", good); !errors.Is(err, bootaddress.ErrUnreachable) || errors.Is(err, bootaddress.ErrUnavailable) {
		t.Fatalf("an outage: %v", err)
	}
	down = false
	// Being deleted, and then missing.
	claim.Finalizers = []string{"networking.datumapis.com/physical-detach"}
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(ctx, claim); err != nil {
		t.Fatal(err)
	}
	refused("being deleted", operation, "DeviceInstallation", good)
	claim = bootClaim(t, api, "alloc-example-install-1")
	claim.Finalizers = nil
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	refused("missing", operation, "DeviceInstallation", good)
}

// TestBootReleaseIsVerifiedInNSOAndIPAM counts a release only once NSO's
// claim is gone and IPAM holds no allocation for the address NSO claimed. A
// failed delete, or a claim NSO still keeps, stays held.
func TestBootReleaseIsVerifiedInNSOAndIPAM(t *testing.T) {
	ctx := context.Background()
	failing := false
	source, api, allocation := bootFixture(t, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
			if failing {
				return apierrors.NewInternalError(errors.New("nso could not delete"))
			}
			return c.Delete(ctx, object, opts...)
		},
	})
	_ = source.Attach(ctx, allocation, "alloc-example-install-1")
	claim := bootClaim(t, api, "alloc-example-install-1")
	claim.Finalizers = []string{"networking.datumapis.com/physical-detach"}
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	ipamAllocation := &unstructured.Unstructured{}
	ipamAllocation.SetGroupVersionKind(bootaddress.AllocationGVK)
	ipamAllocation.SetNamespace(ipamNamespace)
	ipamAllocation.SetName("alloc-0123456789abcdef")
	ipamAllocation.Object["spec"] = map[string]any{"claimRef": map[string]any{"name": BootAddressIPClaim(claim.Name)}}
	if err := api.Create(ctx, ipamAllocation); err != nil {
		t.Fatal(err)
	}

	failing = true
	if gone, err := source.Release(ctx, "alloc-example-install-1"); err == nil || gone {
		t.Fatalf("a failed delete released: %v, %v", gone, err)
	}
	failing = false
	// NSO keeps the claim until the port is off the boot network.
	if gone, err := source.Release(ctx, "alloc-example-install-1"); err != nil || gone {
		t.Fatalf("a claim NSO keeps released: %v, %v", gone, err)
	}
	if detached, err := source.Detach(ctx, "alloc-example-install-1"); err != nil || detached {
		t.Fatalf("a claim NSO keeps detached: %v, %v", detached, err)
	}
	claim = bootClaim(t, api, "alloc-example-install-1")
	claim.Finalizers = nil
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	// The claim is gone, but IPAM still holds the address NSO claimed.
	if gone, err := source.Release(ctx, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrReleaseUnverified) || gone {
		t.Fatalf("a release with a remaining IPAM allocation: %v, %v", gone, err)
	}
	if err := api.Delete(ctx, ipamAllocation); err != nil {
		t.Fatal(err)
	}
	// NSO may have claimed the address under the name for the location.
	located := ipamAllocation.DeepCopy()
	located.SetResourceVersion("")
	located.SetName("alloc-fedcba9876543210")
	located.Object["spec"] = map[string]any{"claimRef": map[string]any{"name": BootAddressIPClaims(claim.Name, location)[1]}}
	if err := api.Create(ctx, located); err != nil {
		t.Fatal(err)
	}
	if gone, err := source.Release(ctx, "alloc-example-install-1"); !errors.Is(err, bootaddress.ErrReleaseUnverified) || gone {
		t.Fatalf("a release with an allocation under the location's name: %v, %v", gone, err)
	}
	if err := api.Delete(ctx, located); err != nil {
		t.Fatal(err)
	}
	if gone, err := source.Release(ctx, "alloc-example-install-1"); err != nil || !gone {
		t.Fatalf("release = %v, %v", gone, err)
	}
	if detached, err := source.Detach(ctx, "alloc-example-install-1"); err != nil || !detached {
		t.Fatalf("detach = %v, %v", detached, err)
	}
	if _, err := (BootAttachments{Client: api, Namespace: bootNamespace, Kind: "DeviceInstallation", Network: bootNetwork}).Release(ctx, "alloc-example-install-1"); err == nil {
		t.Fatal("a release without an IPAM reader was verified")
	}
}

func TestBootAddressIPClaimFollowsNSOsNaming(t *testing.T) {
	if got := BootAddressIPClaim("alloc-example-install-1-boot"); got != "alloc-example-install-1-boot-f-ipv4" {
		t.Fatalf("name = %q", got)
	}
	if long := BootAddressIPClaim(strings.Repeat("b", 260)); len(long) > 253 {
		t.Fatalf("hashed name is %d characters", len(long))
	}
	// NSO's location name: the first eight hex digits of SHA-256("location-example-a").
	names := BootAddressIPClaims("alloc-example-install-1-boot", location)
	if len(names) != 2 || names[0] != "alloc-example-install-1-boot-f-ipv4" || names[1] != "alloc-example-install-1-boot-f-ipv4-l"+locationHash(location) {
		t.Fatalf("names = %v", names)
	}
}

func locationHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:8]
}
