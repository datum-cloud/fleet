package nso

import (
	"context"
	"errors"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/network"
)

func fixture(t *testing.T, objects ...client.Object) (Adapter, client.Client, *fleetv1.MachineAllocation, *fleetv1.Machine, fleetv1.AllocatedInterface) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// The fake API server assigns no UIDs; this one does, as a real one would.
	created := 0
	api := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&networkingv1.NetworkInterfaceClaim{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
			created++
			object.SetUID(types.UID(fmt.Sprintf("00000000-0000-4000-8000-%012d", created)))
			return c.Create(ctx, object, options...)
		}}).Build()
	allocation := &fleetv1.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: "alloc-example", Namespace: "fleet-provider"}}
	machine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default", UID: "00000000-0000-4000-8000-000000000601", Generation: 2}, Spec: fleetv1.MachineSpec{
		NetworkInterfaces: []fleetv1.NetworkInterfaceRequest{{Name: "primary", Network: fleetv1.NamespacedReference{Name: "customer-a"}}},
	}}
	recorded := fleetv1.AllocatedInterface{Name: "primary", Network: fleetv1.NamespacedReference{Namespace: "default", Name: "customer-a"},
		Claim: fleetv1.ClaimReference{Namespace: "default", Name: ClaimName(allocation.Name, "primary")}}
	return Adapter{Client: api, Mode: "Physical"}, api, allocation, machine, recorded
}

// TestEnsureAdoptsOnlyFleetsOwnClaim recovers the claim by name when its
// UID was not recorded, and refuses a claim with another allocation, another
// Machine, another spec, or another UID, and a recorded claim that is gone.
func TestEnsureAdoptsOnlyFleetsOwnClaim(t *testing.T) {
	ctx := context.Background()
	adapter, api, allocation, machine, recorded := fixture(t)
	request := machine.Spec.NetworkInterfaces[0]
	uid, observation, err := adapter.Ensure(ctx, allocation, machine, request, recorded)
	if err != nil || uid == "" || observation.Ready {
		t.Fatalf("first ensure: %q %+v %v", uid, observation, err)
	}
	if again, _, err := adapter.Ensure(ctx, allocation, machine, request, recorded); err != nil || again != uid {
		t.Fatalf("recovery by name: %q %v", again, err)
	}
	recorded.Claim.UID = string(uid)
	claim := &networkingv1.NetworkInterfaceClaim{}
	if err := api.Get(ctx, client.ObjectKey{Namespace: "default", Name: recorded.Claim.Name}, claim); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*networkingv1.NetworkInterfaceClaim){
		"another allocation": func(c *networkingv1.NetworkInterfaceClaim) { c.Labels[AllocationLabel] = "alloc-other" },
		"another Machine":    func(c *networkingv1.NetworkInterfaceClaim) { c.Labels[MachineUIDLabel] = "other" },
		"another spec": func(c *networkingv1.NetworkInterfaceClaim) {
			c.Spec.AttachmentMode = networkingv1.NetworkInterfaceAttachmentModeNetns
		},
	} {
		changed := claim.DeepCopy()
		mutate(changed)
		if err := api.Update(ctx, changed); err != nil {
			t.Fatal(err)
		}
		if _, _, err := adapter.Ensure(ctx, allocation, machine, request, recorded); !errors.Is(err, network.ErrConflict) {
			t.Errorf("%s: %v", name, err)
		}
		restored := &networkingv1.NetworkInterfaceClaim{}
		_ = api.Get(ctx, client.ObjectKeyFromObject(claim), restored)
		claim.ResourceVersion = restored.ResourceVersion
		if err := api.Update(ctx, claim.DeepCopy()); err != nil {
			t.Fatal(err)
		}
		_ = api.Get(ctx, client.ObjectKeyFromObject(claim), claim)
	}
	// The recorded claim is gone: Fleet does not create another.
	if err := api.Delete(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Ensure(ctx, allocation, machine, request, recorded); !errors.Is(err, network.ErrConflict) {
		t.Fatalf("a recorded claim that is gone was recreated: %v", err)
	}
	// A same-name claim someone else created is not Fleet's.
	if err := api.Create(ctx, adapter.Desired(allocation, machine, request)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Ensure(ctx, allocation, machine, request, recorded); !errors.Is(err, network.ErrConflict) {
		t.Fatalf("a claim with another UID was adopted: %v", err)
	}
	// Release treats Fleet's own claim as gone and leaves the other alone.
	if gone, err := adapter.Release(ctx, recorded.Claim); err != nil || !gone {
		t.Fatalf("release = %v, %v", gone, err)
	}
	if err := api.Get(ctx, client.ObjectKey{Namespace: "default", Name: recorded.Claim.Name}, &networkingv1.NetworkInterfaceClaim{}); err != nil {
		t.Fatalf("release deleted a claim that is not Fleet's: %v", err)
	}
	if _, _, err := (Adapter{Client: api}).Ensure(ctx, allocation, machine, request, recorded); !errors.Is(err, network.ErrUnsupported) {
		t.Fatalf("no attachment mode: %v", err)
	}
}
