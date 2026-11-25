package capacity

import (
	"context"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestADuplicateInAnotherNamespaceCounts records the same serial number in
// two namespaces of one provider project.
func TestADuplicateInAnotherNamespaceCounts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := inventory.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default", UID: "one"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "SERIAL-1", SystemUUID: "00000000-0000-4000-8000-000000000001"}}}
	other := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "elsewhere", UID: "two"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: " serial-1", SystemUUID: "00000000-0000-4000-8000-000000000002"}}}
	alone := fake.NewClientBuilder().WithScheme(scheme).WithObjects(device).Build()
	if duplicate, err := Duplicate(context.Background(), alone, device); err != nil || duplicate {
		t.Fatalf("a lone Device has a duplicate: %v", err)
	}
	both := fake.NewClientBuilder().WithScheme(scheme).WithObjects(device, other).Build()
	if duplicate, err := Duplicate(context.Background(), both, device); err != nil || !duplicate {
		t.Fatalf("a duplicate in another namespace was missed: %v", err)
	}
}

// TestARecreatedDeviceInheritsNoRegisteredCapacity recreates the Device behind
// capacity registered and enabled for its old incarnation.
func TestARecreatedDeviceInheritsNoRegisteredCapacity(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{inventory.AddToScheme, ops.AddToScheme, metal.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	capacity := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "device-old", Namespace: "default", Generation: 2,
		Annotations: map[string]string{RegisteredDeviceUID: "old"}},
		Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "server"}, AllocationPolicy: "Enabled", CleaningPolicyRef: metal.LocalReference{Name: "tenant-reuse"}}}
	for name, test := range map[string]struct {
		uid      string
		replaced bool
	}{"the registered incarnation": {"old", false}, "a recreated Device": {"new", true}} {
		t.Run(name, func(t *testing.T) {
			device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default", UID: types.UID(test.uid)}}
			projector := &Projector{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(device).Build(), Namespace: "default",
				Cleaning: Fixed(map[string]string{"tenant-reuse": "sha256:policy"})}
			in, err := projector.inputs(context.Background(), capacity)
			if err != nil {
				t.Fatal(err)
			}
			if in.DeviceReplaced != test.replaced || (in.DeviceUID != "") == test.replaced {
				t.Fatalf("inputs for %s: replaced %v, Device UID %q", name, in.DeviceReplaced, in.DeviceUID)
			}
			if !test.replaced {
				return
			}
			projection, err := projector.Evaluate(context.Background(), capacity)
			if err != nil {
				t.Fatal(err)
			}
			if prepared := apiMeta.FindStatusCondition(projection.Conditions, "Prepared"); projection.Available() || prepared.Reason != "DeviceReplaced" {
				t.Fatalf("a recreated Device inherited the capacity: %+v", projection.Conditions)
			}
			if offered, _, err := projector.Offer(context.Background(), capacity); err != nil || offered {
				t.Fatalf("the allocator was offered capacity of a recreated Device: %v %v", offered, err)
			}
		})
	}
}
