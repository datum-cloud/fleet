package multiplane

import (
	"context"
	"net/netip"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.datum.net/fleet/internal/bootaddress"
)

func claim(name string, created time.Time, status map[string]any) *unstructured.Unstructured {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(bootaddress.GVK)
	c.SetNamespace(DeviceNamespace)
	c.SetName(name)
	c.SetCreationTimestamp(metav1.NewTime(created))
	if status != nil {
		c.Object["status"] = status
	}
	return c
}

// TestStandInIPAMBindsTheLowestFreeAddressOldestFirst binds unbound claims
// in creation order to the lowest addresses no claim holds, leaves bound
// claims alone, and refuses when the range is exhausted.
func TestStandInIPAMBindsTheLowestFreeAddressOldestFirst(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(bootaddress.GVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(bootaddress.GVK.GroupVersion().WithKind("IPClaimList"), &unstructured.UnstructuredList{})
	now := time.Now().Truncate(time.Second)
	api := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		claim("held", now.Add(-time.Hour), map[string]any{"phase": "Bound", "address": "192.0.2.100"}),
		claim("newer", now, nil),
		claim("older", now.Add(-time.Minute), nil),
	).WithStatusSubresource(claim("x", now, nil)).Build()
	ipam := StandInIPAM{Client: api, Namespace: DeviceNamespace, First: netip.MustParseAddr("192.0.2.100"), Last: netip.MustParseAddr("192.0.2.102")}
	if bound, err := ipam.Bind(ctx); err != nil || bound != 2 {
		t.Fatalf("bound %d, %v", bound, err)
	}
	address := func(name string) string {
		c := claim(name, now, nil)
		if err := api.Get(ctx, client.ObjectKeyFromObject(c), c); err != nil {
			t.Fatal(err)
		}
		value, _, _ := unstructured.NestedString(c.Object, "status", "address")
		return value
	}
	if address("held") != "192.0.2.100" || address("older") != "192.0.2.101" || address("newer") != "192.0.2.102" {
		t.Fatalf("addresses = %s %s %s", address("held"), address("older"), address("newer"))
	}
	if bound, err := ipam.Bind(ctx); err != nil || bound != 0 {
		t.Fatalf("a second pass bound %d, %v", bound, err)
	}
	if err := api.Create(ctx, claim("extra", now.Add(time.Minute), nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ipam.Bind(ctx); err == nil {
		t.Fatal("an exhausted range bound a claim")
	}
}
