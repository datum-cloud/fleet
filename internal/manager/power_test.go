package manager

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// TestPowerFollowsCustodyAndDiscovery reports the discovered power state of
// the allocation's Device, Unknown while a recovery hold exists or custody
// is unverified, and nothing once the allocation is released.
func TestPowerFollowsCustodyAndDiscovery(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{fleetv1.AddToScheme, inventory.AddToScheme, ops.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	machine := allocation.Machine{Project: "consumer-example", ProjectUID: "00000000-0000-4000-8000-000000000501", Namespace: "default", Name: "allocation", UID: "00000000-0000-4000-8000-000000000601"}
	consumer := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: machine.Name, Namespace: machine.Namespace, UID: machine.UID}}).WithStatusSubresource(&fleetv1.Machine{}).Build()
	const namespace, deviceUID = "fleet-provider", "00000000-0000-4000-8000-000000000101"
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server-example-001", Namespace: namespace, UID: deviceUID}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{SystemUUID: "00000000-0000-4000-8000-000000000201"}},
		Status: inventory.DeviceStatus{Discovery: &inventory.DeviceDiscovery{Inventory: &inventory.ObservedInventory{Systems: []inventory.ObservedSystem{{ID: "s", UUID: "00000000-0000-4000-8000-000000000201", PowerState: "On"}}}}}}
	held := &ops.DeviceCustody{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: namespace, Labels: map[string]string{ops.LabelDeviceUID: deviceUID}}, Spec: ops.DeviceCustodySpec{DeviceRef: ops.DeviceReference{Name: device.Name}, State: ops.CustodyVerified,
		Holder: ops.Holder{Type: ops.HolderAllocation, AllocationRef: &ops.LocalReference{Name: allocation.Name(machine)}}}}
	active := &fleetv1.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: allocation.Name(machine), Namespace: namespace, Labels: map[string]string{
		fleetv1.LabelMachineUID: string(machine.UID), fleetv1.LabelConsumerProjectUID: string(machine.ProjectUID), fleetv1.LabelDeviceUID: deviceUID}}, Spec: fleetv1.MachineAllocationSpec{
		MachineRef: fleetv1.AllocatedMachineReference{Project: machine.Project, Namespace: machine.Namespace, Name: machine.Name},
		DeviceRef:  fleetv1.AllocatedDeviceReference{Name: device.Name}}, Status: fleetv1.MachineAllocationStatus{Phase: fleetv1.AllocationActive}}
	provider := fake.NewClientBuilder().WithScheme(scheme).WithObjects(device, held, active).Build()
	power := func() string {
		t.Helper()
		if err := ProjectPower(ctx, consumer, provider, namespace, machine); err != nil {
			t.Fatal(err)
		}
		current := &fleetv1.Machine{}
		if err := consumer.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: machine.Name}, current); err != nil {
			t.Fatal(err)
		}
		if current.Status.Power == nil {
			return ""
		}
		return current.Status.Power.State
	}
	if got := power(); got != "On" {
		t.Fatalf("power = %q, want the discovered On", got)
	}
	update := func(change func(*ops.DeviceCustody)) {
		t.Helper()
		if err := provider.Get(ctx, client.ObjectKeyFromObject(held), held); err != nil {
			t.Fatal(err)
		}
		change(held)
		if err := provider.Update(ctx, held); err != nil {
			t.Fatal(err)
		}
	}
	update(func(c *ops.DeviceCustody) {
		c.Spec.RecoveryHold = &ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: "DeviceWipe", Name: "wipe"}, Fence: 3, Reason: "DeadlineExceeded"}
	})
	if got := power(); got != "Unknown" {
		t.Fatalf("power under a recovery hold = %q", got)
	}
	update(func(c *ops.DeviceCustody) { c.Spec.RecoveryHold, c.Spec.State = nil, ops.CustodyUnverified })
	if got := power(); got != "Unknown" {
		t.Fatalf("power with unverified custody = %q", got)
	}
	update(func(c *ops.DeviceCustody) { c.Spec.State = ops.CustodyVerified })
	if got := power(); got != "On" {
		t.Fatalf("power after the hold settled = %q", got)
	}
	active.Status.Phase = fleetv1.AllocationReleased
	if err := provider.Update(ctx, active); err != nil {
		t.Fatal(err)
	}
	if got := power(); got != "" {
		t.Fatalf("power after release = %q", got)
	}
}
