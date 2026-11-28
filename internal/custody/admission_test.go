package custody

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ipamDown answers every IPAM claim read as an outage.
type ipamDown struct{ client.Client }

func (c ipamDown) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if object.GetObjectKind().GroupVersionKind().Group == bootaddress.GVK.Group {
		return apierrors.NewServiceUnavailable("ipam is down")
	}
	return c.Client.Get(ctx, key, object, opts...)
}

// requireUntouched fails if custody admitted anything or raised the fence.
func (e *env) requireUntouched(name string, fence int64) {
	e.t.Helper()
	custody := e.custody(name)
	if custody.Spec.ActiveOperation != nil || custody.Spec.Fence != fence {
		e.t.Fatalf("custody changed: active %+v fence %d, want fence %d", custody.Spec.ActiveOperation, custody.Spec.Fence, fence)
	}
}

func TestAdmissionPreconditions(t *testing.T) {
	e := startEnv(t)
	n := 0
	fresh := func() *inventory.Device {
		n++
		return e.verified(fmt.Sprintf("server-example-%03d", n), uid(n), fmt.Sprintf("%064x", n))
	}

	t.Run("admission raises the fence, sets the deadline and retains inputs and snapshot", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		before := e.custody(device.Name).Spec.Fence
		operation := e.staffOp(device, "ok")
		e.settle(device.Name)
		custody := e.custody(device.Name)
		active := custody.Spec.ActiveOperation
		if active == nil || active.Name != operation.Name || active.Fence != before+1 || custody.Spec.Fence != before+1 || custody.Spec.Holder.Type != ops.HolderService {
			t.Fatalf("custody after admission = %+v", custody.Spec)
		}
		digest, _ := SpecDigest(operation.Spec)
		if active.InputDigest != digest || !active.Deadline.Time.Equal(e.clock.Now().Add(testDeadline)) || active.Creator.Username != staffUser {
			t.Fatalf("active = %+v", active)
		}
		want := ops.DeviceSnapshot{BMCEndpoint: device.Spec.BMC.Endpoint, SystemUUID: device.Spec.Identity.SystemUUID, BMCPublicKeySHA256: fmt.Sprintf("%064x", n), DeviceGeneration: device.Generation, DiscoveryObservedAt: e.clock.Now().Format(time.RFC3339)}
		if !reflect.DeepEqual(active.Snapshot, want) {
			t.Fatalf("snapshot = %+v, want %+v", active.Snapshot, want)
		}
		status := e.requirePhase(operation, ops.PhaseAdmitted, "")
		if status.Fence != active.Fence || status.InputDigest != digest || status.Snapshot == nil || !reflect.DeepEqual(*status.Snapshot, want) {
			t.Fatalf("status copy = %+v", status)
		}
	})

	t.Run("1: Unverified custody refuses staff and holds provider operations pending", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		custody := e.custody(device.Name)
		custody.Spec.State = ops.CustodyUnverified
		if err := e.admin.Update(e.ctx, custody); err != nil {
			t.Fatal(err)
		}
		e.sites.forget(device.Name)
		staff := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(staff, ops.PhaseFailed, ReasonCustodyUnverified)
		e.requireUntouched(device.Name, custody.Spec.Fence)
	})

	t.Run("2: a creator and authority pair outside the table is refused", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		fence := e.custody(device.Name).Spec.Fence
		// Staff cannot create a provider kind.
		spoof := &DeviceTestProviderOperation{ObjectMeta: metav1.ObjectMeta{Name: "alloc-x-test-a", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, allocated("alloc-x")), Input: "a"}}
		e.create(spoof)
		// A service principal cannot create a staff kind.
		service := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: "svc-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, providerUser, service("not staff")), Input: "a"}}
		e.create(service)
		// A staff kind that names an allocationRef.
		named := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: "named-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, allocated("alloc-x")), Input: "a"}}
		e.create(named)
		// Staff cannot create the release kind either.
		wipe := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: WipeName("alloc-x", 1), Namespace: "default"}, Spec: ops.DeviceWipeSpec{OperationEnvelope: envelope(device, staffUser, allocated("alloc-x")), Step: 1, CleaningPolicyDigest: policyDigest, TargetDisks: []string{"disk-0"}, BootInterface: "primary"}}
		e.create(wipe)
		e.settle(device.Name)
		for _, operation := range []ops.Operation{spoof, service, named, wipe} {
			e.requirePhase(operation, ops.PhaseFailed, ReasonCreatorNotPermitted)
		}
		e.requireUntouched(device.Name, fence)
	})

	t.Run("2: a staff operation on an allocated or releasing server is refused", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-held"); err != nil {
			t.Fatal(err)
		}
		fence := e.custody(device.Name).Spec.Fence
		allocatedOp := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(allocatedOp, ops.PhaseFailed, ReasonHolderMismatch)
		if err := e.coord.BeginRelease(e.ctx, device.Name, "alloc-held"); err != nil {
			t.Fatal(err)
		}
		releasingOp := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(releasingOp, ops.PhaseFailed, ReasonHolderMismatch)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("3: an allocationRef must name the holder's allocation", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-mine"); err != nil {
			t.Fatal(err)
		}
		fence := e.custody(device.Name).Spec.Fence
		other := e.providerOp(device, "alloc-other", "a")
		e.settle(device.Name)
		e.requirePhase(other, ops.PhaseFailed, ReasonHolderMismatch)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("4: one active operation; provider operations wait in order", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		first := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(first, ops.PhaseAdmitted, "")
		// The first staff operation took the holder to Service.
		second := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(second, ops.PhaseFailed, ReasonHolderMismatch)

		queued := fresh()
		if err := e.coord.Claim(e.ctx, queued.Name, "alloc-queue"); err != nil {
			t.Fatal(err)
		}
		running := e.providerOp(queued, "alloc-queue", "a")
		e.settle(queued.Name)
		e.requirePhase(running, ops.PhaseAdmitted, "")
		waiting := e.providerOp(queued, "alloc-queue", "b")
		e.settle(queued.Name)
		e.requirePhase(waiting, ops.PhasePending, ReasonOperationInProgress)
		if err := e.result(queued.Name, ops.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		e.settle(queued.Name)
		e.requirePhase(running, ops.PhaseSucceeded, "")
		e.requirePhase(waiting, ops.PhaseAdmitted, "")
	})

	t.Run("5: the Device UID must match deviceRef.uid", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		fence := e.custody(device.Name).Spec.Fence
		operation := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: "stale-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, service("old record")), Input: "a"}}
		operation.Spec.DeviceRef.UID = "00000000-0000-4000-8000-ffffffffffff"
		e.create(operation)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonDeviceIdentityMismatch)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("5: identity must match current discovery", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		fence := e.custody(device.Name).Spec.Fence
		// Discovery older than the maximum age.
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now().Add(-DefaultDiscoveryMaxAge-time.Minute))
		old := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(old, ops.PhaseFailed, ReasonDeviceIdentityMismatch)
		// Discovery that did not match identity.
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		device.Status.Conditions[0].Status = metav1.ConditionFalse
		if err := e.admin.Status().Update(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		mismatched := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(mismatched, ops.PhaseFailed, ReasonDeviceIdentityMismatch)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("6: no other live Device may record the same BMC key", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		key := fmt.Sprintf("%064x", n)
		fence := e.custody(device.Name).Spec.Fence
		twin := e.device(fmt.Sprintf("server-twin-%03d", n), uid(900+n), key)
		shared := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(shared, ops.PhaseFailed, ReasonSharedPublicKey)
		e.requireUntouched(device.Name, fence)
		// A retired Device no longer counts.
		for _, lifecycle := range []inventory.DeviceLifecycle{inventory.DeviceDecommissioning, inventory.DeviceRetired} {
			if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(twin), twin); err != nil {
				t.Fatal(err)
			}
			twin.Spec.Lifecycle = lifecycle
			if err := e.admin.Update(e.ctx, twin); err != nil {
				t.Fatal(err)
			}
		}
		e.discover(device, key, e.clock.Now())
		admitted := e.staffOp(device, "ok")
		e.settle(device.Name)
		e.requirePhase(admitted, ops.PhaseAdmitted, "")
	})

	t.Run("names: deterministic kinds must use their rule, staff kinds a generated name", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-name"); err != nil {
			t.Fatal(err)
		}
		fence := e.custody(device.Name).Spec.Fence
		wrong := &DeviceTestProviderOperation{ObjectMeta: metav1.ObjectMeta{Name: "authored-name", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, providerUser, allocated("alloc-name")), Input: "a"}}
		e.create(wrong)
		e.settle(device.Name)
		e.requirePhase(wrong, ops.PhaseFailed, ReasonNameMismatch)
		e.requireUntouched(device.Name, fence)

		free := fresh()
		authored := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{Name: "authored-staff", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(free, staffUser, service("authored")), Input: "a"}}
		e.create(authored)
		e.settle(free.Name)
		e.requirePhase(authored, ops.PhaseFailed, ReasonNameMismatch)
	})

	t.Run("kind-specific checks refuse with their own reason", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		fence := e.custody(device.Name).Spec.Fence
		operation := e.staffOp(device, "refuse")
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, reasonTestRefuse)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("bootstrap stays refused", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-boot"); err != nil {
			t.Fatal(err)
		}
		fence := e.custody(device.Name).Spec.Fence
		operation := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: InstallationName("alloc-boot", "request-1"), Namespace: "default"}, Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: envelope(device, providerUser, allocated("alloc-boot")), RequestUID: "request-1",
			Image: ops.ImageInput{Name: "debian-12", Digest: imageDigest}, Bootstrap: &ops.SealedInput{Ref: "sealed-example", Digest: imageDigest}, BootInterface: "primary", TargetDisks: []string{"disk-0"}, Delivery: ops.DeliveryInput{Method: ops.DeliveryNetworkBoot},
		}}
		e.create(operation)
		missing := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: InstallationName("alloc-boot", "request-2"), Namespace: "default"}, Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: envelope(device, providerUser, allocated("alloc-boot")), RequestUID: "request-2",
			Image: ops.ImageInput{Name: "debian-12", Digest: imageDigest}, BootInterface: "absent", TargetDisks: []string{"disk-0"}, Delivery: ops.DeliveryInput{Method: ops.DeliveryNetworkBoot},
		}}
		e.create(missing)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonBootstrapUnsupported)
		e.requirePhase(missing, ops.PhaseFailed, ReasonBootInterfaceUnknown)
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a boot address must still be bound to its claim", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-net"); err != nil {
			t.Fatal(err)
		}
		fence := e.custody(device.Name).Spec.Fence
		name := func(request string) string { return InstallationName("alloc-net", request) }
		// claim creates the operation's own claim and returns the input that
		// names it.
		claim := func(request, phase, address string) *ops.BootNetworkInput {
			created := e.ipClaim(bootaddress.ClaimName(name(request)), name(request), "DeviceInstallation", phase, address)
			return &ops.BootNetworkInput{AddressClaim: bootaddress.Reference(created.GetName()), Address: address}
		}
		withAddress := func(request string, input *ops.BootNetworkInput) *ops.DeviceInstallation {
			operation := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: name(request), Namespace: "default"}, Spec: ops.DeviceInstallationSpec{
				OperationEnvelope: envelope(device, providerUser, allocated("alloc-net")), RequestUID: request,
				Image: ops.ImageInput{Name: "debian-12", Digest: imageDigest}, BootInterface: "primary", TargetDisks: []string{"disk-0"}, Delivery: ops.DeliveryInput{Method: ops.DeliveryNetworkBoot}, BootNetwork: input,
			}}
			e.create(operation)
			return operation
		}
		// A claim deleted and recreated under its name, with the same labels
		// and address, after its operation was created is not the claim the
		// operation was built from.
		recreated := claim("recreated", "Bound", "198.51.100.10")
		recreatedOperation := withAddress("recreated", recreated)
		old := &unstructured.Unstructured{}
		old.SetGroupVersionKind(bootaddress.GVK)
		if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: "default", Name: recreated.AddressClaim.Name}, old); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Delete(e.ctx, old); err != nil {
			t.Fatal(err)
		}
		// Creation times have one-second resolution.
		time.Sleep(1100 * time.Millisecond)
		claim("recreated", "Bound", "198.51.100.10")
		otherAddress := claim("other-address", "Bound", "198.51.100.11")
		otherAddress.Address = "198.51.100.99"
		good := claim("bound", "Bound", "198.51.100.13")
		refused := []*ops.DeviceInstallation{
			recreatedOperation,
			withAddress("other-address", otherAddress),
			withAddress("unbound", claim("unbound", "Pending", "198.51.100.12")),
			withAddress("missing", &ops.BootNetworkInput{AddressClaim: bootaddress.Reference(bootaddress.ClaimName(name("missing"))), Address: good.Address}),
			// Another operation's bound claim is not this operation's.
			withAddress("foreign", good),
		}
		e.settle(device.Name)
		for _, operation := range refused {
			e.requirePhase(operation, ops.PhaseFailed, ReasonBootAddressMismatch)
		}
		e.requireUntouched(device.Name, fence)
		admitted := withAddress("bound", good)
		// While IPAM cannot be read, the claim's state is unknown: the
		// operation waits, admitted no further, and is checked again.
		outage := e.coordinator(ipamDown{e.admin})
		if outcome := settleWith(t, e.ctx, outage, device.Name); outcome.RequeueAfter == 0 {
			t.Fatalf("an undecided check did not ask to run again: %+v", outcome)
		}
		e.requirePhase(admitted, ops.PhasePending, ReasonBootAddress)
		e.requireUntouched(device.Name, fence)
		e.settle(device.Name)
		if active := e.custody(device.Name).Spec.ActiveOperation; active == nil || active.Name != admitted.Name || active.Snapshot.BootMACAddress == "" {
			t.Fatalf("active = %+v, want the bound installation with the boot MAC from inventory", active)
		}
		// A wipe network-boots too, so its boot interface must have a MAC.
		releasing := fresh()
		if err := e.coord.Claim(e.ctx, releasing.Name, "alloc-net-wipe"); err != nil {
			t.Fatal(err)
		}
		if err := e.coord.BeginRelease(e.ctx, releasing.Name, "alloc-net-wipe"); err != nil {
			t.Fatal(err)
		}
		wipeFence := e.custody(releasing.Name).Spec.Fence
		wipe := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: WipeName("alloc-net-wipe", 1), Namespace: "default"}, Spec: ops.DeviceWipeSpec{
			OperationEnvelope: envelope(releasing, releaseUser, allocated("alloc-net-wipe")), Step: 1, CleaningPolicyDigest: policyDigest, TargetDisks: []string{"disk-0"}, BootInterface: "absent",
		}}
		e.create(wipe)
		e.settle(releasing.Name)
		e.requirePhase(wipe, ops.PhaseFailed, ReasonBootInterfaceUnknown)
		e.requireUntouched(releasing.Name, wipeFence)
	})

	t.Run("a boot address claim of a kind with no check fails for good", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		if err := e.coord.Claim(e.ctx, device.Name, "alloc-kind"); err != nil {
			t.Fatal(err)
		}
		fence := e.custody(device.Name).Spec.Fence
		name := InstallationName("alloc-kind", "request-1")
		// A claim of the unknown kind exists with the operation's labels and
		// address; no check can read it, so it never counts.
		e.ipClaim(bootaddress.ClaimName(name), name, "DeviceInstallation", "Bound", "198.51.100.20")
		operation := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: envelope(device, providerUser, allocated("alloc-kind")), RequestUID: "request-1",
			Image: ops.ImageInput{Name: "debian-12", Digest: imageDigest}, BootInterface: "primary", TargetDisks: []string{"disk-0"}, Delivery: ops.DeliveryInput{Method: ops.DeliveryNetworkBoot},
			BootNetwork: &ops.BootNetworkInput{AddressClaim: fleetv1.TypedReference{APIGroup: "networking.example.com", Kind: "AddressLease", Name: bootaddress.ClaimName(name)}, Address: "198.51.100.20"},
		}}
		e.create(operation)
		if outcome := e.settle(device.Name); outcome.RequeueAfter != 0 {
			t.Fatalf("an unsupported claim kind asked to be checked again: %+v", outcome)
		}
		status := e.requirePhase(operation, ops.PhaseFailed, ReasonBootAddressUnsupported)
		if !strings.Contains(status.Message, "networking.example.com/AddressLease") {
			t.Fatalf("message = %q, want the claim's group and kind", status.Message)
		}
		e.requireUntouched(device.Name, fence)
	})

	t.Run("a staff operation not admitted within five minutes expires", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		fence := e.custody(device.Name).Spec.Fence
		operation := e.staffOp(device, "ok")
		// The coordinator was down for five minutes.
		e.clock.Advance(DefaultStaffExpiry + time.Minute)
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseCancelled, ReasonExpired)
		e.requireUntouched(device.Name, fence)
	})
}
