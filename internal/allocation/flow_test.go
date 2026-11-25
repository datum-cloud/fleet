package allocation

import (
	"context"
	"errors"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site/simwork"
)

// TestAllocateInstallReleaseReuse runs one Device through a whole lifecycle
// on custody: allocation, a losing contender, a retried allocation,
// installation, reinstallation, release with a wipe, and reuse by the
// contender under a higher fence.
func TestAllocateInstallReleaseReuse(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)

	first, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != Name(machine(1)) || first.Status.Phase != fleetv1.AllocationActive || first.Labels[fleetv1.LabelDeviceUID] != string(device.UID) {
		t.Fatalf("allocation = %s %s %+v", first.Name, first.Status.Phase, first.Spec.DeviceRef)
	}
	state := e.custody(device.Name)
	if state.Spec.Holder.Type != ops.HolderAllocation || state.Spec.Holder.AllocationRef.Name != first.Name {
		t.Fatalf("holder = %+v", state.Spec.Holder)
	}

	// The contender loses: custody already names the first allocation, and
	// its own allocation never remains.
	if _, err := e.allocator.Allocate(e.ctx, e.request(2)); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("contender err = %v", err)
	}
	if found, err := e.allocator.Get(e.ctx, machine(2)); err != nil || found != nil {
		t.Fatalf("contender allocation = %v, %v", found, err)
	}
	// A retry after a lost response finds the same allocation.
	again, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil || again.UID != first.UID {
		t.Fatalf("retry = %v, %v", again, err)
	}

	install := func(allocation *fleetv1.MachineAllocation, installation *fleetv1.Installation, current string) *ops.DeviceInstallation {
		t.Helper()
		operation, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation, Current: current})
		if err != nil {
			t.Fatal(err)
		}
		e.until("the installation succeeds", func() bool {
			if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
				t.Fatal(err)
			}
			if operation.Status.Phase.Ended() && operation.Status.Phase != ops.PhaseSucceeded {
				t.Fatalf("installation ended %s: %s", operation.Status.Phase, operation.Status.Message)
			}
			return operation.Status.Phase == ops.PhaseSucceeded
		})
		return operation
	}
	initial := install(first, e.installation(1, "debian-12", ""), "")
	if initial.Name != custody.InstallationName(first.Name, uid(701)) {
		t.Fatalf("installation name = %s", initial.Name)
	}
	server, err := simwork.ReadServer(e.store, namespace+"/"+device.Name)
	if err != nil || server.Booted != initial.Spec.Image.Digest {
		t.Fatalf("server = %+v, %v", server, err)
	}
	// A reinstall must name the current installation.
	if _, err := e.allocator.Install(e.ctx, first, Installation{Installation: e.installation(2, "debian-12-reinstall", "elsewhere"), Current: "debian-12"}); !errors.Is(err, ErrPredecessor) {
		t.Fatalf("stale predecessor err = %v", err)
	}
	reinstall := install(first, e.installation(2, "debian-12-reinstall", "debian-12"), "debian-12")
	if reinstall.Status.Fence <= initial.Status.Fence {
		t.Fatalf("reinstall fence %d, initial %d", reinstall.Status.Fence, initial.Status.Fence)
	}

	e.release(first, machine(1))
	wipe := &ops.DeviceWipe{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: custody.WipeName(first.Name, 1)}, wipe); err != nil || wipe.Status.Phase != ops.PhaseSucceeded {
		t.Fatalf("wipe = %+v, %v", wipe.Status, err)
	}
	if server, err := simwork.ReadServer(e.store, namespace+"/"+device.Name); err != nil || server != (simwork.Server{}) {
		t.Fatalf("server after wipe = %+v, %v", server, err)
	}
	state = e.custody(device.Name)
	if state.Spec.Holder.Type != ops.HolderNone {
		t.Fatalf("holder after release = %+v", state.Spec.Holder)
	}
	// A released allocation never allocates again.
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrReleased) {
		t.Fatalf("released allocate err = %v", err)
	}

	second, err := e.allocator.Allocate(e.ctx, e.request(2))
	if err != nil || second.Spec.DeviceRef.Name != device.Name {
		t.Fatalf("reuse = %v, %v", second, err)
	}
	// A holder change does not raise the fence; the next admission does.
	if next := install(second, e.installation(3, "debian-12", ""), ""); next.Status.Fence <= wipe.Status.Fence {
		t.Fatalf("reused Device's installation fence %d is not above the wipe's %d", next.Status.Fence, wipe.Status.Fence)
	}
}

// TestConcurrentAllocatorsOneWinner races allocators for different Machines
// at one Device. Exactly one allocation remains and custody names it.
func TestConcurrentAllocatorsOneWinner(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	const contenders = 6
	var wg sync.WaitGroup
	results := make([]error, contenders)
	for index := range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[index] = e.allocator.Allocate(e.ctx, e.request(index+1))
		}()
	}
	wg.Wait()
	winners := 0
	for index, err := range results {
		switch {
		case err == nil:
			winners++
		case !errors.Is(err, ErrInsufficientCapacity):
			t.Fatalf("contender %d: %v", index, err)
		}
	}
	var allocations fleetv1.MachineAllocationList
	if err := e.admin.List(e.ctx, &allocations, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	if winners != 1 || len(allocations.Items) != 1 {
		t.Fatalf("winners = %d, allocations = %d", winners, len(allocations.Items))
	}
	if holder := e.custody(device.Name).Spec.Holder; holder.AllocationRef == nil || holder.AllocationRef.Name != allocations.Items[0].Name {
		t.Fatalf("holder = %+v, allocation %s", holder, allocations.Items[0].Name)
	}
}

// TestAnAllocationThatLostItsDeviceIsReplaced covers the write order: an
// allocation created for a Device another holder then took is deleted and
// the allocator selects again.
func TestAnAllocationThatLostItsDeviceIsReplaced(t *testing.T) {
	e := startEnv(t)
	taken := e.server(1)
	free := e.server(2)
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); err != nil {
		t.Fatal(err)
	}
	if holder := e.custody(taken.Name).Spec.Holder; holder.Type != ops.HolderAllocation {
		t.Fatalf("first allocation took %+v", holder)
	}
	// A crash left the second Machine's allocation naming the taken Device,
	// before it claimed custody.
	stale, err := e.allocator.newAllocation(context.Background(), e.request(2), e.capacity(taken.Name), taken, "")
	if err != nil {
		t.Fatal(err)
	}
	e.create(stale)
	second, err := e.allocator.Allocate(e.ctx, e.request(2))
	if err != nil {
		t.Fatal(err)
	}
	if second.UID == stale.UID || second.Spec.DeviceRef.Name != free.Name {
		t.Fatalf("second allocation = %s on %s", second.UID, second.Spec.DeviceRef.Name)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(stale), &fleetv1.MachineAllocation{}); err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

func (e *env) capacity(device string) *fleetv1.MachineCapacity {
	e.t.Helper()
	capacity := &fleetv1.MachineCapacity{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: "capacity-" + device}, capacity); err != nil {
		e.t.Fatal(err)
	}
	return capacity
}
