package allocation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// quota records reservations by Machine.
type quota struct {
	mu   sync.Mutex
	held map[string]bool
}

func (q *quota) Acquire(_ context.Context, machine Machine) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.held == nil {
		q.held = map[string]bool{}
	}
	q.held[string(machine.UID)] = true
	return "fleet-machine-" + string(machine.UID)[len(machine.UID)-3:], nil
}

func (q *quota) Release(_ context.Context, machine Machine) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.held, string(machine.UID))
	return nil
}

func (q *quota) holds(machine Machine) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.held[string(machine.UID)]
}

// TestAMachineDeletedWhileWaitingReleasesItsQuota covers the window between
// the quota claim and the allocation: a Machine that reserved quota but
// found no capacity still releases its claim when it is deleted.
func TestAMachineDeletedWhileWaitingReleasesItsQuota(t *testing.T) {
	e := startEnv(t)
	q := &quota{}
	e.allocator.Quota = q
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("allocate without capacity = %v", err)
	}
	if !q.holds(machine(1)) {
		t.Fatal("the quota claim was not reserved before the allocation")
	}
	released, err := e.allocator.ReleaseMachine(e.ctx, machine(1))
	if err != nil || !released || q.holds(machine(1)) {
		t.Fatalf("release = %v, %v; quota still held %v", released, err, q.holds(machine(1)))
	}
}

// TestAnAllocationThatNeverHeldItsDeviceIsReleased releases an allocation a
// crash left before it claimed custody, without touching custody.
func TestAnAllocationThatNeverHeldItsDeviceIsReleased(t *testing.T) {
	e := startEnv(t)
	q := &quota{}
	e.allocator.Quota = q
	device := e.server(1)
	if _, err := q.Acquire(e.ctx, machine(1)); err != nil {
		t.Fatal(err)
	}
	orphan, err := e.allocator.newAllocation(e.request(1), e.capacity(device.Name), device, "fleet-machine-601")
	if err != nil {
		t.Fatal(err)
	}
	e.create(orphan)
	before := e.custody(device.Name)
	released, err := e.allocator.ReleaseMachine(e.ctx, machine(1))
	if err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(orphan), orphan); err != nil || orphan.Status.Phase != fleetv1.AllocationReleased {
		t.Fatalf("allocation = %+v, %v", orphan.Status, err)
	}
	if after := e.custody(device.Name); after.ResourceVersion != before.ResourceVersion || q.holds(machine(1)) {
		t.Fatalf("custody changed (%s -> %s) or quota held %v", before.ResourceVersion, after.ResourceVersion, q.holds(machine(1)))
	}
}

// TestAFailingWipeGetsNewStepsThenStops gives a wipe that ends Failed, which
// sent nothing, a new step up to the bound, then stops the release with the
// Cleaning gate open and the Device still in Release.
func TestAFailingWipeGetsNewStepsThenStops(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	// The site enrolls disk-0; a wipe of disk-1 is refused before anything
	// is sent.
	e.allocator.TargetDisks = []string{"disk-1"}
	held, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		if err := e.loop.Pass(e.ctx); err != nil {
			t.Logf("pass: %v", err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(held), held); err != nil {
			t.Fatal(err)
		}
		_, err := e.allocator.Release(e.ctx, held, machine(1))
		if errors.Is(err, ErrWipeFailed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the release did not stop at the last wipe step: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for step := int32(1); step <= maxWipeSteps; step++ {
		wipe := &ops.DeviceWipe{}
		if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: custody.WipeName(held.Name, step)}, wipe); err != nil || wipe.Status.Phase != ops.PhaseFailed || wipe.Status.Reason != custody.ReasonTargetDisks {
			t.Fatalf("wipe step %d = %+v, %v", step, wipe.Status, err)
		}
	}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: custody.WipeName(held.Name, maxWipeSteps+1)}, &ops.DeviceWipe{}); err == nil {
		t.Fatal("a wipe step past the bound was created")
	}
	state := e.custody(device.Name)
	if state.Spec.Holder.Type != ops.HolderRelease || !containsGate(state.Spec.Holder.OpenGates, ops.GateCleaning) || held.Status.Phase != fleetv1.AllocationReleasing {
		t.Fatalf("holder = %+v, allocation %s", state.Spec.Holder, held.Status.Phase)
	}
}

func containsGate(gates []ops.ReleaseGate, gate ops.ReleaseGate) bool {
	for _, g := range gates {
		if g == gate {
			return true
		}
	}
	return false
}
