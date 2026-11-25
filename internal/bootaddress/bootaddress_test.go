package bootaddress

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

const namespace = "fleet-provider"

func claimer(t *testing.T, objects ...client.Object) (Claimer, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(GVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(GVK.GroupVersion().WithKind("IPClaimList"), &unstructured.UnstructuredList{})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return Claimer{Client: c, Namespace: namespace, Class: "provisioning", Kind: "DeviceInstallation"}, c
}

func bind(t *testing.T, c client.Client, name, phase, address string) *unstructured.Unstructured {
	t.Helper()
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(GVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, claim); err != nil {
		t.Fatal(err)
	}
	claim.Object["status"] = map[string]any{"phase": phase, "address": address}
	if err := c.Update(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

// TestClaimIsCreatedOnceAndBoundBeforeUse creates one claim per operation
// and returns its address only once IPAM bound it to one IPv4 host.
func TestClaimIsCreatedOnceAndBoundBeforeUse(t *testing.T) {
	ctx := context.Background()
	c, api := claimer(t)
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("unbound claim: %v", err)
	}
	claim := bind(t, api, "alloc-a-install-1-boot", "Bound", "198.51.100.10")
	input, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1")
	if err != nil || input.Address != "198.51.100.10" || input.AddressClaim.Name != "alloc-a-install-1-boot" || input.AddressClaim.UID != string(claim.GetUID()) {
		t.Fatalf("input = %+v, %v", input, err)
	}
	if spec, _, _ := unstructured.NestedMap(claim.Object, "spec"); spec["className"] != "provisioning" || spec["ipFamily"] != "IPv4" || spec["reclaimPolicy"] != "Delete" {
		t.Fatalf("spec = %v", spec)
	}
	bind(t, api, "alloc-a-install-1-boot", "Bound", "2001:db8::10")
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); err == nil {
		t.Fatal("an IPv6 boot address was accepted for DHCPv4")
	}
	// Another requester's claim under the same name is a conflict.
	wipes := c
	wipes.Kind = "DeviceWipe"
	if _, err := wipes.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign claim: %v", err)
	}
	if _, err := wipes.Release(ctx, "alloc-a-install-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a requester released another's claim: %v", err)
	}
}

// TestVerifyRequiresTheExactBoundIncarnation refuses a missing claim,
// another incarnation, another address, and an unbound or deleting claim.
func TestVerifyRequiresTheExactBoundIncarnation(t *testing.T) {
	ctx := context.Background()
	c, api := claimer(t)
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	claim := bind(t, api, "alloc-a-install-1-boot", "Bound", "198.51.100.10")
	good := ops.BootNetworkInput{AddressClaim: ops.ClaimReference{Name: claim.GetName(), UID: string(claim.GetUID())}, Address: "198.51.100.10"}
	operation := &ops.DeviceInstallation{}
	operation.Name, operation.Namespace = "alloc-a-install-1", namespace
	if err := Verify(ctx, api, operation, "DeviceInstallation", good); err != nil {
		t.Fatal(err)
	}
	// The claim must be the operation's own: by name and by its labels.
	other := &ops.DeviceInstallation{}
	other.Name, other.Namespace = "alloc-a-install-2", namespace
	if err := Verify(ctx, api, other, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("another operation's claim verified: %v", err)
	}
	if err := Verify(ctx, api, operation, "DeviceWipe", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("another kind's claim verified: %v", err)
	}
	for name, input := range map[string]ops.BootNetworkInput{
		"other name":     {AddressClaim: ops.ClaimReference{Name: "other-boot", UID: good.AddressClaim.UID}, Address: good.Address},
		"other UID":      {AddressClaim: ops.ClaimReference{Name: good.AddressClaim.Name, UID: string(types.UID("other"))}, Address: good.Address},
		"other address":  {AddressClaim: good.AddressClaim, Address: "198.51.100.11"},
		"not an address": {AddressClaim: good.AddressClaim, Address: "198.51.100.10/24"},
	} {
		if err := Verify(ctx, api, operation, "DeviceInstallation", input); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	bind(t, api, claim.GetName(), "Releasing", "198.51.100.10")
	if err := Verify(ctx, api, operation, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unbound claim verified: %v", err)
	}
	if err := Verify(ctx, nil, operation, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatal("no reader verified")
	}
	if gone, err := c.Release(ctx, "alloc-a-install-1"); err != nil || gone {
		t.Fatalf("release = %v, %v", gone, err)
	}
	if gone, err := c.Release(ctx, "alloc-a-install-1"); err != nil || !gone {
		t.Fatalf("second release = %v, %v", gone, err)
	}
}
