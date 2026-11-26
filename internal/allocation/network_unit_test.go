package allocation

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/network"
	"go.datum.net/fleet/internal/network/nso"
)

// partial creates the first interface's claim and conflicts on the second.
type partial struct{}

func (partial) Check(context.Context, *fleetv1.Machine, string) error { return nil }

func (partial) Ensure(_ context.Context, _ *fleetv1.MachineAllocation, _ *fleetv1.Machine, request fleetv1.NetworkInterfaceRequest, _ fleetv1.AllocatedInterface) (types.UID, nso.Observation, error) {
	if request.Name == "primary" {
		return "claim-uid-primary", nso.Observation{}, nil
	}
	return "", nso.Observation{}, network.ErrConflict
}

func (partial) Release(context.Context, fleetv1.ClaimReference) (bool, error) { return true, nil }

// TestAttachKeepsProvenUIDsWhenALaterInterfaceFails records the first
// claim's UID even though the second interface conflicts, so the first
// claim is never adopted by name again.
func TestAttachKeepsProvenUIDsWhenALaterInterfaceFails(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{inventory.AddToScheme, fleetv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server-example-001", Namespace: namespace, UID: "device-uid"}, Spec: inventory.DeviceSpec{
		Interfaces: []inventory.Interface{{Name: "primary", MACAddress: "02:00:00:00:00:01"}, {Name: "secondary", MACAddress: "02:00:00:00:00:02"}},
	}}
	cable := func(name, iface, port string) *inventory.Cable {
		return &inventory.Cable{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: inventory.CableSpec{Medium: "Copper",
			Lanes: []inventory.CableLane{{ID: "one", A: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: device.Name}, ComponentID: iface}, B: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: "switch-example-a"}, ComponentID: port}}}}}
	}
	allocation := &fleetv1.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: "alloc-example", Namespace: namespace}, Spec: fleetv1.MachineAllocationSpec{DeviceRef: fleetv1.AllocatedDeviceReference{Name: device.Name, UID: "device-uid"}},
		Status: fleetv1.MachineAllocationStatus{Phase: fleetv1.AllocationActive}}
	api := fake.NewClientBuilder().WithScheme(s).WithObjects(device, cable("c1", "primary", "ethernet1"), cable("c2", "secondary", "ethernet2"), allocation).WithStatusSubresource(allocation).Build()
	allocator := &Allocator{Provider: api, Namespace: namespace, Network: partial{}}
	machine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default", UID: "machine-uid"}, Spec: fleetv1.MachineSpec{NetworkInterfaces: []fleetv1.NetworkInterfaceRequest{
		{Name: "primary", Network: fleetv1.NamespacedReference{Name: "customer-a"}}, {Name: "secondary", Network: fleetv1.NamespacedReference{Name: "customer-a"}},
	}}}
	if err := api.Get(ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := allocator.Attach(ctx, allocation, machine); !errors.Is(err, network.ErrConflict) {
		t.Fatalf("attach = %v", err)
	}
	stored := &fleetv1.MachineAllocation{}
	if err := api.Get(ctx, client.ObjectKeyFromObject(allocation), stored); err != nil {
		t.Fatal(err)
	}
	if got := stored.Status.Network.Interfaces; len(got) != 2 || got[0].Claim.UID != "claim-uid-primary" || got[1].Claim.UID != "" {
		t.Fatalf("recorded = %+v", got)
	}
}
