package allocation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/tenancy"
)

// No spec holds an object UID, so these tests pin each same-name
// recreation guard: an object recreated under the same name never inherits
// what was recorded for the incarnation before it.

// TestAnAllocationForAnotherMachineIncarnationIsAConflict covers a Machine
// and a consumer project recreated under the same names: the allocation's
// labels name the incarnation it was made for.
func TestAnAllocationForAnotherMachineIncarnationIsAConflict(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	for label, name := range map[string]string{fleetv1.LabelMachineUID: "Machine", fleetv1.LabelConsumerUID: "consumer"} {
		stored := &fleetv1.MachineAllocation{}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), stored); err != nil {
			t.Fatal(err)
		}
		original := stored.Labels[label]
		stored.Labels[label] = uid(999)
		if err := e.admin.Update(e.ctx, stored); err != nil {
			t.Fatal(err)
		}
		if _, err := e.allocator.Get(e.ctx, machine(1)); !errors.Is(err, ErrConflict) {
			t.Fatalf("an allocation for another %s incarnation: %v", name, err)
		}
		stored.Labels[label] = original
		if err := e.admin.Update(e.ctx, stored); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := e.allocator.Get(e.ctx, machine(1)); err != nil || found == nil {
		t.Fatalf("the allocation's own Machine: %v, %v", found, err)
	}
}

// TestAnOperationForAnotherDeviceIncarnationIsAConflict covers a Device
// recreated under the same name: an existing operation with the same name
// and spec, recorded for the earlier Device, is never taken for a new one.
func TestAnOperationForAnotherDeviceIncarnationIsAConflict(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	installation := e.installation(1, "first", "")
	operation, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation})
	if err != nil {
		t.Fatal(err)
	}
	stored := &ops.DeviceInstallation{}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), stored); err != nil {
		t.Fatal(err)
	}
	stored.Labels[ops.LabelDeviceUID] = uid(998)
	if err := e.admin.Update(e.ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an operation recorded for another Device incarnation: %v", err)
	}
	if stored.Spec.DeviceRef.Name != device.Name {
		t.Fatalf("operation names %s", stored.Spec.DeviceRef.Name)
	}
}

// TestCustodyForAnotherDeviceIncarnationIsNotFree covers a Device recreated
// under the same name: custody recorded for the earlier Device is not free
// capacity for the new one.
func TestCustodyForAnotherDeviceIncarnationIsNotFree(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	state := e.custody(device.Name)
	if !Free(state, device) {
		t.Fatalf("custody of the Device's own incarnation is not free: %+v", state.Spec)
	}
	recreated := *device
	recreated.UID = types.UID(uid(997))
	if Free(state, &recreated) {
		t.Fatal("custody of an earlier Device incarnation is free for a recreated Device")
	}
}

// TestAnAllocationForAnotherConsumerIsAConflict covers a consumer of another
// kind or name whose Machine shares the UIDs: the allocation's consumerRef
// names the consumer it was made for.
func TestAnAllocationForAnotherConsumerIsAConflict(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); err != nil {
		t.Fatal(err)
	}
	for _, consumer := range []fleetv1.TypedReference{tenancy.Project("consumer-other"), tenancy.Namespace("consumer-1")} {
		other := machine(1)
		other.Consumer = consumer
		if _, err := e.allocator.Get(e.ctx, other); !errors.Is(err, ErrConflict) {
			t.Fatalf("an allocation for %+v: %v", consumer, err)
		}
	}
}

// TestOnlyAProjectOrANamespaceConsumes refuses any other consumer kind, in
// the allocator before quota or capacity and at the API.
func TestOnlyAProjectOrANamespaceConsumes(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	for _, consumer := range []fleetv1.TypedReference{
		{APIGroup: "apps", Kind: "Deployment", Name: "consumer-1"},
		{APIGroup: "resourcemanager.miloapis.com", Kind: "Organization", Name: "consumer-1"},
		{Kind: "Project", Name: "consumer-1"},
		{APIGroup: "resourcemanager.miloapis.com", Kind: "Namespace", Name: "consumer-1"},
	} {
		request := e.request(1)
		request.Machine.Consumer = consumer
		if _, err := e.allocator.Allocate(e.ctx, request); !errors.Is(err, tenancy.ErrUnsupportedConsumer) {
			t.Fatalf("allocate for %+v: %v", consumer, err)
		}
	}
	var allocations fleetv1.MachineAllocationList
	if err := e.admin.List(e.ctx, &allocations); err != nil || len(allocations.Items) != 0 {
		t.Fatalf("allocations = %d, %v", len(allocations.Items), err)
	}
	request := e.request(1)
	request.Machine.Consumer = tenancy.Namespace("consumer-1")
	allocation, err := e.allocator.Allocate(e.ctx, request)
	if err != nil {
		t.Fatalf("allocate for a Namespace consumer: %v", err)
	}
	refused := allocation.DeepCopyObject().(*fleetv1.MachineAllocation)
	refused.ObjectMeta = metav1.ObjectMeta{Name: "alloc-refused", Namespace: allocation.Namespace}
	refused.Spec.ConsumerRef = fleetv1.TypedReference{APIGroup: "apps", Kind: "Deployment", Name: "consumer-1"}
	if err := e.admin.Create(e.ctx, refused); !apierrors.IsInvalid(err) {
		t.Fatalf("the API took a Deployment consumer: %v", err)
	}
}

// recreatingProvider recreates the Device after the allocator selected it
// and created the allocation, before the allocator claims custody, and
// verifies custody for the new Device as an operator's resolve would.
type recreatingProvider struct {
	client.Client
	e    *env
	done bool
}

func (p *recreatingProvider) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if err := p.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	allocation, ok := object.(*fleetv1.MachineAllocation)
	if !ok || p.done {
		return nil
	}
	p.done = true
	e := p.e
	device := &inventory.Device{}
	if err := e.admin.Get(ctx, client.ObjectKey{Namespace: namespace, Name: allocation.Spec.DeviceRef.Name}, device); err != nil {
		return err
	}
	device.Finalizers = nil
	if err := e.admin.Update(ctx, device); err != nil {
		return err
	}
	if err := e.admin.Delete(ctx, device); err != nil {
		return err
	}
	recreated := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: namespace, Labels: device.Labels}, Spec: device.Spec}
	for {
		if err := e.admin.Create(ctx, recreated); err == nil {
			break
		} else if !apierrors.IsAlreadyExists(err) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	state := e.custody(device.Name)
	state.Labels[ops.LabelDeviceUID] = string(recreated.UID)
	return e.admin.Update(ctx, state)
}

// TestARecreatedDeviceIsNeverClaimedForTheEarlierIncarnation recreates the
// selected Device between selection and the custody claim. The claim
// requires the Device incarnation the allocation names, so the allocator
// selects again, and the allocation that holds custody names its live
// Device.
func TestARecreatedDeviceIsNeverClaimedForTheEarlierIncarnation(t *testing.T) {
	e := startEnv(t)
	original := e.server(1)
	e.server(2)
	e.allocator.Provider = &recreatingProvider{Client: e.admin, e: e}
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	recreated := &inventory.Device{}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(original), recreated); err != nil || recreated.UID == original.UID {
		t.Fatalf("the selected Device was not recreated: %v", err)
	}
	live := &inventory.Device{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: allocation.Spec.DeviceRef.Name}, live); err != nil {
		t.Fatal(err)
	}
	if got := allocation.Labels[fleetv1.LabelDeviceUID]; got != string(live.UID) {
		t.Fatalf("custody of %s was claimed for an allocation made against Device %s, the live Device is %s", live.Name, got, live.UID)
	}
	if holder := e.custody(live.Name).Spec.Holder; holder.Type != ops.HolderAllocation || holder.AllocationRef.Name != allocation.Name {
		t.Fatalf("holder = %+v", holder)
	}
	if holder := e.custody(original.Name).Spec.Holder; holder.Type != ops.HolderNone {
		t.Fatalf("the recreated Device's holder = %+v", holder)
	}
}

// TestTheAllocationAuthorityNamesNoIncarnation refuses an authority that
// records the Machine or consumer UID, so the allocation spec holds no
// object UID, and accepts one that names the decision by name.
func TestTheAllocationAuthorityNamesNoIncarnation(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	request := e.request(1)
	for _, authority := range []fleetv1.AllocationAuthority{
		{Principal: "fixture", Decision: "fixture:" + string(request.Machine.UID)},
		{Principal: "fixture:" + string(request.Machine.ConsumerUID), Decision: "fixture"},
	} {
		request.Authority = authority
		if _, err := e.allocator.Allocate(e.ctx, request); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("authority %+v: %v", authority, err)
		}
	}
	request.Authority = fleetv1.AllocationAuthority{Principal: "fixture:consumer-1", Decision: "fixture:default/allocation"}
	allocation, err := e.allocator.Allocate(e.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(allocation.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []types.UID{request.Machine.UID, request.Machine.ConsumerUID, types.UID(allocation.Labels[fleetv1.LabelDeviceUID])} {
		if strings.Contains(string(data), string(uid)) {
			t.Fatalf("the allocation spec records %s: %s", uid, data)
		}
	}
}
