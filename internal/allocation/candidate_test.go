package allocation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// offersExcept offers every capacity but those it fails, and records which
// capacities it evaluated.
type offersExcept struct {
	fail      map[string]bool
	evaluated []string
}

func (o *offersExcept) Offer(ctx context.Context, capacity *fleetv1.MachineCapacity) (bool, fleetv1.Hardware, error) {
	o.evaluated = append(o.evaluated, capacity.Name)
	if o.fail[capacity.Name] {
		return false, fleetv1.Hardware{}, errors.New("the evaluation could not read its inputs")
	}
	return offersAll{}.Offer(ctx, capacity)
}

// candidates is three capacities, a to c, each backed by its own identified
// Device. held names the Devices whose custody is taken.
func candidates(t *testing.T, held ...string) (client.Client, Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{inventory.AddToScheme, ops.AddToScheme, fleetv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	taken := map[string]bool{}
	for _, name := range held {
		taken[name] = true
	}
	var objects []client.Object
	for index, name := range []string{"a", "b", "c"} {
		device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server-" + name, Namespace: namespace, UID: types.UID(uid(index + 1))},
			Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Model: "example", Serial: "serial-" + name, SystemUUID: uid(100 + index)}}}
		holder := ops.Holder{Type: ops.HolderNone}
		if taken[device.Name] {
			holder = ops.Holder{Type: ops.HolderAllocation, AllocationRef: &ops.LocalReference{Name: "allocation-example"}}
		}
		state := &ops.DeviceCustody{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: namespace, Labels: map[string]string{ops.LabelDeviceUID: string(device.UID)}},
			Spec: ops.DeviceCustodySpec{DeviceRef: ops.DeviceReference{Name: device.Name}, State: ops.CustodyVerified, Holder: holder}}
		capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "capacity-" + name, Namespace: namespace, Labels: map[string]string{"pool": "example"}},
			Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: device.Name}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "example-cleaning"}, ReclaimPolicy: "Retain"}}
		objects = append(objects, device, state, capacity)
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class-example"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{
		MachineCapacities: fleetv1.MachineSelector{Namespace: namespace, Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "example"}}}}}}
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile-example"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 2}, Memory: fleetv1.MemoryRequirement{Capacity: "2Gi"}}}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), Request{Class: class, Profile: profile}
}

// One capacity whose evaluation fails does not stop the allocator from
// selecting the next; the error surfaces only when nothing is offered.
func TestACapacityThatFailsEvaluationIsSkipped(t *testing.T) {
	provider, request := candidates(t)
	offers := &offersExcept{fail: map[string]bool{"capacity-a": true}}
	allocator := &Allocator{Provider: provider, Namespace: namespace, Capacity: offers}
	selected, _, _, err := allocator.candidate(context.Background(), request, map[string]bool{})
	if err != nil || selected == nil || selected.Name != "capacity-b" {
		t.Fatalf("selected %v, %v; want capacity-b", selected, err)
	}

	offers = &offersExcept{fail: map[string]bool{"capacity-a": true, "capacity-b": true, "capacity-c": true}}
	allocator.Capacity = offers
	if _, _, _, err := allocator.candidate(context.Background(), request, map[string]bool{}); err == nil || errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("every evaluation failed, got %v; want the evaluation error", err)
	}
}

// The evaluation, which lists the provider project, runs only for a
// capacity whose Device is identified and whose custody is free.
func TestTheAllocatorEvaluatesOnlyFreeCapacity(t *testing.T) {
	provider, request := candidates(t, "server-a", "server-b")
	offers := &offersExcept{}
	allocator := &Allocator{Provider: provider, Namespace: namespace, Capacity: offers}
	selected, _, _, err := allocator.candidate(context.Background(), request, map[string]bool{})
	if err != nil || selected == nil || selected.Name != "capacity-c" {
		t.Fatalf("selected %v, %v; want capacity-c", selected, err)
	}
	if fmt.Sprint(offers.evaluated) != "[capacity-c]" {
		t.Fatalf("evaluated %v; want only the free capacity-c", offers.evaluated)
	}
}
