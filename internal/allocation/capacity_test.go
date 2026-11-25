package allocation

import (
	"errors"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestOnlyEvidenceOffersCapacity commissions a server, fails it on a second
// commissioning, and then edits its status to claim it is Available. The
// projector withdraws the offer and the allocator refuses the server.
func TestOnlyEvidenceOffersCapacity(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	capacity := e.capacity(device.Name)
	e.until("the projector writes Available", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(capacity), capacity); err != nil {
			t.Fatal(err)
		}
		available := apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionAvailable)
		return available != nil && available.Status == metav1.ConditionTrue && capacity.Status.Hardware.CPU.Cores == 2
	})

	// The server reports other hardware, so commissioning it again fails.
	target := e.enrollment[namespace+"/"+device.Name]
	changed := *target.Hardware
	changed.CPU.Cores = 4
	target.Hardware = &changed
	e.enrollment[namespace+"/"+device.Name] = target
	operation, err := custody.NewCommissioning(device, e.plan, ops.Creator{Username: "operator@example.com", UID: uid(805)}, "recommission")
	if err != nil {
		t.Fatal(err)
	}
	e.create(operation)
	e.until("the capacity is withdrawn", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(capacity), capacity); err != nil {
			t.Fatal(err)
		}
		prepared := apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionPrepared)
		return prepared != nil && prepared.Reason == "CommissioningFailed" && capacity.Status.Hardware.CPU.Cores == 0
	})

	// Nobody can offer it by editing status.
	for _, kind := range []string{custody.ConditionPrepared, custody.ConditionHealthy, custody.ConditionAvailable} {
		apiMeta.SetStatusCondition(&capacity.Status.Conditions, metav1.Condition{Type: kind, Status: metav1.ConditionTrue, Reason: "Edited", ObservedGeneration: capacity.Generation})
	}
	capacity.Status.Hardware = fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 2}, Memory: fleetv1.HardwareMemory{Capacity: "2Gi"}}
	if err := e.admin.Status().Update(e.ctx, capacity); err != nil {
		t.Fatal(err)
	}
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("an edited status offered the server: %v", err)
	}
	// The projector restores the derived status on its next pass.
	if err := e.loop.Pass(e.ctx); err != nil {
		t.Logf("pass: %v", err)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(capacity), capacity); err != nil {
		t.Fatal(err)
	}
	if available := apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionAvailable); available == nil || available.Status != metav1.ConditionFalse {
		t.Fatalf("the projector kept the edited status: %+v", capacity.Status.Conditions)
	}
}

// TestADuplicateDeviceWithdrawsTheOffer records a second Device with the
// first one's serial number after its capacity is offered.
func TestADuplicateDeviceWithdrawsTheOffer(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	copied := *device
	copied.ObjectMeta = metav1.ObjectMeta{Name: "server-example-duplicate", Namespace: namespace}
	copied.Spec.Identity.SystemUUID = uid(999)
	copied.Status = device.Status
	copied.Spec.ClaimRef.Name = "claim-duplicate"
	e.create(&copied)
	offered, _, err := e.projector.Offer(e.ctx, e.capacity(device.Name))
	if err != nil || offered {
		t.Fatalf("a duplicate serial left the capacity offered: %v", err)
	}
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("the allocator offered a server with a duplicate: %v", err)
	}
}

// TestAnAllocationOnDisabledCapacityIsWithdrawn creates an allocation, as an
// allocator would after selecting the capacity, and then disables the
// capacity before the claim. The allocator withdraws the allocation instead
// of claiming the Device.
func TestAnAllocationOnDisabledCapacityIsWithdrawn(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	capacity := e.capacity(device.Name)
	stale, err := e.allocator.newAllocation(e.request(1), capacity, device, fleetv1.Hardware{}, "")
	if err != nil {
		t.Fatal(err)
	}
	e.create(stale)
	capacity.Spec.AllocationPolicy = "Disabled"
	if err := e.admin.Update(e.ctx, capacity); err != nil {
		t.Fatal(err)
	}
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("allocation on Disabled capacity: %v", err)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(stale), stale); err == nil {
		t.Fatal("the allocation on Disabled capacity was kept")
	}
	if holder := e.custody(device.Name).Spec.Holder; holder.Type != ops.HolderNone {
		t.Fatalf("custody was claimed for Disabled capacity: %+v", holder)
	}
}
