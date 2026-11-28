package allocation

import (
	"context"
	"errors"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// offersAll offers every capacity with the profile's hardware, so only the
// allocator's own checks can refuse one.
type offersAll struct{}

func (offersAll) Offer(context.Context, *fleetv1.MachineCapacity) (bool, fleetv1.Hardware, error) {
	return true, fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 2}, Memory: fleetv1.HardwareMemory{Capacity: "2Gi"}}, nil
}

// TestTheAllocatorNeverSelectsAServerWithoutAHardwareID gives the allocator
// an evaluation that offers everything and free custody, and checks that it
// still never selects capacity backed by a Device without a hardware ID
// (#131). The identified control case shows the setup is otherwise eligible.
func TestTheAllocatorNeverSelectsAServerWithoutAHardwareID(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{inventory.AddToScheme, ops.AddToScheme, fleetv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class-example"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{
		MachineCapacities: fleetv1.MachineSelector{Namespace: namespace, Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "example"}}}}}}
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile-example"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 2}, Memory: fleetv1.MemoryRequirement{Capacity: "2Gi"}}}
	for name, test := range map[string]struct {
		identity inventory.DeviceIdentity
		eligible bool
	}{
		"recorded without a hardware ID": {inventory.DeviceIdentity{Model: "patch-panel", Unidentified: true, UnidentifiedReason: "the equipment has no serial"}, false},
		"with no serial or system UUID":  {inventory.DeviceIdentity{Model: "patch-panel"}, false},
		"identified":                     {inventory.DeviceIdentity{Model: "example", Serial: "serial-example-001", SystemUUID: uid(101)}, true},
	} {
		t.Run(name, func(t *testing.T) {
			device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server-example-001", Namespace: namespace, UID: types.UID(uid(1))}, Spec: inventory.DeviceSpec{Identity: test.identity}}
			state := &ops.DeviceCustody{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: namespace, Labels: map[string]string{ops.LabelDeviceUID: uid(1)}},
				Spec: ops.DeviceCustodySpec{DeviceRef: ops.DeviceReference{Name: device.Name}, State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderNone}}}
			capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "capacity-" + device.Name, Namespace: namespace, Labels: map[string]string{"pool": "example"}},
				Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: device.Name}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "example-cleaning"}, ReclaimPolicy: "Retain"}}
			allocator := &Allocator{Provider: fake.NewClientBuilder().WithScheme(scheme).WithObjects(device, state, capacity).Build(), Namespace: namespace, Capacity: offersAll{}}
			selected, _, _, err := allocator.candidate(context.Background(), Request{Class: class, Profile: profile}, map[string]bool{})
			switch {
			case test.eligible && (err != nil || selected == nil || selected.Name != capacity.Name):
				t.Fatalf("the identified server was not selected: %v", err)
			case !test.eligible && !errors.Is(err, ErrInsufficientCapacity):
				t.Fatalf("a server without a hardware ID was selected: %v %v", selected, err)
			}
		})
	}
}
