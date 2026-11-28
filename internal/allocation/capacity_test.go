package allocation

import (
	"context"
	"errors"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	fleetcapacity "go.datum.net/fleet/internal/capacity"
	"go.datum.net/fleet/internal/custody"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// TestARecreatedDeviceIsNotClaimed creates an allocation, as an allocator
// would after selecting the capacity, for an earlier incarnation of the
// Device. The allocator withdraws it instead of claiming the Device.
func TestARecreatedDeviceIsNotClaimed(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	capacity := e.capacity(device.Name)
	stale, err := e.allocator.newAllocation(e.request(1), capacity, device, fleetv1.Hardware{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// The allocation names another incarnation than the Device now stored.
	stale.Labels[fleetv1.LabelDeviceUID] = uid(990)
	e.create(stale)
	if held, err := e.allocator.claim(e.ctx, stale); err != nil || held {
		t.Fatalf("claim for an earlier incarnation: held %v, %v", held, err)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(stale), stale); err == nil {
		t.Fatal("the allocation for an earlier incarnation was kept")
	}
	if holder := e.custody(device.Name).Spec.Holder; holder.Type != ops.HolderNone {
		t.Fatalf("custody was claimed for an earlier incarnation: %+v", holder)
	}
}

// offerAll offers every capacity, as a projection read before the Device was
// recreated would.
type offerAll struct{ hardware fleetv1.Hardware }

func (o offerAll) Offer(context.Context, *fleetv1.MachineCapacity) (bool, fleetv1.Hardware, error) {
	return true, o.hardware, nil
}

// TestCapacityRegisteredForAnotherIncarnationIsSkipped records another Device
// incarnation as the capacity's registration. Even when the offer was made,
// the allocator does not select the capacity.
func TestCapacityRegisteredForAnotherIncarnationIsSkipped(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	capacity := e.capacity(device.Name)
	e.until("the projector offers the capacity", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(capacity), capacity); err != nil {
			t.Fatal(err)
		}
		available := apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionAvailable)
		return available != nil && available.Status == metav1.ConditionTrue
	})
	e.allocator.Capacity = offerAll{hardware: capacity.Status.Hardware}
	capacity.Annotations = map[string]string{fleetcapacity.RegisteredDeviceUID: uid(990)}
	if err := e.admin.Update(e.ctx, capacity); err != nil {
		t.Fatal(err)
	}
	// Selection skips it, and so does the recheck before the claim.
	if _, _, _, err := e.allocator.candidate(e.ctx, e.request(1), map[string]bool{}); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("capacity registered for another incarnation was a candidate: %v", err)
	}
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("capacity registered for another incarnation was selected: %v", err)
	}
	if holder := e.custody(device.Name).Spec.Holder; holder.Type != ops.HolderNone {
		t.Fatalf("custody was claimed: %+v", holder)
	}
}

// afterCheckRecreatingProvider recreates the allocation's Device right after
// the allocator's last check read it, before the custody claim. With
// relabel, it also verifies custody for the new Device, as an operator's
// resolve would. The check sees the incarnation the allocation names, so
// only the claim can refuse.
type afterCheckRecreatingProvider struct {
	client.Client
	e                         *env
	relabel, armed, recreated bool
}

func (p *afterCheckRecreatingProvider) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if err := p.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	if _, ok := object.(*fleetv1.MachineAllocation); ok {
		p.armed = true
	}
	return nil
}

func (p *afterCheckRecreatingProvider) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if err := p.Client.Get(ctx, key, object, options...); err != nil {
		return err
	}
	device, ok := object.(*inventory.Device)
	if !ok || !p.armed || p.recreated {
		return nil
	}
	p.recreated = true
	e := p.e
	stored := device.DeepCopyObject().(*inventory.Device)
	stored.Finalizers = nil
	if err := e.admin.Update(ctx, stored); err != nil {
		return err
	}
	if err := e.admin.Delete(ctx, stored); err != nil {
		return err
	}
	again := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: stored.Name, Namespace: namespace, Labels: stored.Labels}, Spec: stored.Spec}
	for {
		if err := e.admin.Create(ctx, again); err == nil {
			break
		} else if !apierrors.IsAlreadyExists(err) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !p.relabel {
		return nil
	}
	state := e.custody(stored.Name)
	state.Labels[ops.LabelDeviceUID] = string(again.UID)
	return e.admin.Update(ctx, state)
}

// TestADeviceRecreatedAfterTheLastCheckIsNotClaimed recreates the selected
// Device after the allocator's last check and before its custody claim. The
// claim takes the incarnation from the allocation's device-uid label and
// refuses, so the allocation is withdrawn and the allocator selects again.
// While custody still names the old incarnation, only the claim's check of
// the live Device refuses; once custody names the new one, its check of
// custody refuses too.
func TestADeviceRecreatedAfterTheLastCheckIsNotClaimed(t *testing.T) {
	for name, relabel := range map[string]bool{"custody still for the old Device": false, "custody verified for the new Device": true} {
		t.Run(name, func(t *testing.T) {
			e := startEnv(t)
			original := e.server(1)
			e.server(2)
			provider := &afterCheckRecreatingProvider{Client: e.admin, e: e, relabel: relabel}
			e.allocator.Provider = provider
			allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
			if err != nil {
				t.Fatal(err)
			}
			if !provider.recreated {
				t.Fatal("the Device was not recreated after the last check")
			}
			live := &inventory.Device{}
			if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: allocation.Spec.DeviceRef.Name}, live); err != nil {
				t.Fatal(err)
			}
			if got := allocation.Labels[fleetv1.LabelDeviceUID]; got != string(live.UID) {
				t.Fatalf("custody of %s was claimed for an allocation made against Device %s, the live Device is %s", live.Name, got, live.UID)
			}
			if holder := e.custody(original.Name).Spec.Holder; holder.Type != ops.HolderNone {
				t.Fatalf("the recreated Device was claimed: %+v", holder)
			}
		})
	}
}
