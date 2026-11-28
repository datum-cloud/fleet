package capacity

import (
	"context"
	"fmt"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestAPassListsEachKindOnce evaluates many capacities in one pass and
// checks that the pass lists each kind once, rather than once per
// capacity, and that every capacity still sees its own Device's inputs.
func TestAPassListsEachKindOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{inventory.AddToScheme, ops.AddToScheme, metal.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	var objects []client.Object
	var capacities []*metal.MachineCapacity
	for index := range 5 {
		name := fmt.Sprintf("server-%d", index)
		objects = append(objects, &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name)},
			Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Model: "example", Serial: "SERIAL-" + name, SystemUUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index)}}})
		capacities = append(capacities, &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: name}, AllocationPolicy: "Enabled"}})
	}
	// Two Devices share a serial; only they are duplicates.
	objects = append(objects, &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "copy", Namespace: "elsewhere", UID: "copy"},
		Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Model: "example", Serial: "SERIAL-server-0", SystemUUID: "00000000-0000-4000-8000-999999999999"}}})
	lists := map[string]int{}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			lists[fmt.Sprintf("%T", list)]++
			return c.List(ctx, list, opts...)
		},
	}).Build()
	pass := (&Projector{Reader: reader, Namespace: "default"}).Snapshot()
	for index, capacity := range capacities {
		in, err := pass.inputs(context.Background(), capacity)
		if err != nil {
			t.Fatal(err)
		}
		if in.DeviceUID != capacity.Spec.DeviceRef.Name || in.Duplicate != (index == 0) {
			t.Fatalf("%s: inputs for Device %q, duplicate %v", capacity.Name, in.DeviceUID, in.Duplicate)
		}
	}
	for kind, count := range lists {
		if count != 1 {
			t.Fatalf("the pass listed %s %d times; want once: %v", kind, count, lists)
		}
	}
	if len(lists) < 4 {
		t.Fatalf("the pass listed %v; want Devices, plans, approvals and commissionings", lists)
	}
}
