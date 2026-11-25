package custody

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
		wipe := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: WipeName("alloc-x", 1), Namespace: "default"}, Spec: ops.DeviceWipeSpec{OperationEnvelope: envelope(device, staffUser, allocated("alloc-x")), Step: 1, Policy: ops.CleaningPolicyReference{Name: "tenant-reuse-v1", Generation: 1, Digest: policyDigest}, TargetDisks: []string{"disk-0"}}}
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
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-held"); err != nil {
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
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-mine"); err != nil {
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
		if err := e.coord.Claim(e.ctx, queued.Name, queued.UID, "alloc-queue"); err != nil {
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

	t.Run("5: the Device UID must match the operation's device-uid label", func(t *testing.T) {
		e := e.sub(t)
		device := fresh()
		fence := e.custody(device.Name).Spec.Fence
		operation := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: "stale-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, service("old record")), Input: "a"}}
		labelDevice(operation, "00000000-0000-4000-8000-ffffffffffff")
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
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-name"); err != nil {
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
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, "alloc-boot"); err != nil {
			t.Fatal(err)
		}
		fence := e.custody(device.Name).Spec.Fence
		operation := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: InstallationName("alloc-boot", "request-1"), Namespace: "default", Labels: map[string]string{ops.LabelInstallationUID: "request-1"}}, Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: envelope(device, providerUser, allocated("alloc-boot")),
			Image:             ops.ImageInput{Name: "debian-12", Digest: imageDigest}, Bootstrap: &ops.SealedInput{Ref: "sealed-example", Digest: imageDigest}, BootInterface: "primary", TargetDisks: []string{"disk-0"},
		}}
		e.create(operation)
		missing := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: InstallationName("alloc-boot", "request-2"), Namespace: "default", Labels: map[string]string{ops.LabelInstallationUID: "request-2"}}, Spec: ops.DeviceInstallationSpec{
			OperationEnvelope: envelope(device, providerUser, allocated("alloc-boot")),
			Image:             ops.ImageInput{Name: "debian-12", Digest: imageDigest}, BootInterface: "absent", TargetDisks: []string{"disk-0"},
		}}
		e.create(missing)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonBootstrapUnsupported)
		e.requirePhase(missing, ops.PhaseFailed, ReasonBootInterfaceUnknown)
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
