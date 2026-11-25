package allocation

import (
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/cleaning"
	"go.datum.net/fleet/internal/custody"
)

func (e *env) policy() *ops.CleaningPolicy {
	e.t.Helper()
	policy := &ops.CleaningPolicy{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: "example-cleaning"}, policy); err != nil {
		e.t.Fatal(err)
	}
	return policy
}

// TestAllocationPinsItsCleaningPolicy pins a copy of the capacity's policy
// and its digest, keeps it when the policy is deleted, and wipes under it.
func TestAllocationPinsItsCleaningPolicy(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	policy := e.policy()

	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := cleaning.Digest(policy.Spec)
	if err != nil {
		t.Fatal(err)
	}
	pinned := allocation.Spec.CleaningPolicy
	if pinned.Name != policy.Name || pinned.Generation != policy.Generation || pinned.Generation < 1 || pinned.Digest != digest || pinned.Spec.Rules[0].Name != "nvme-ssd" {
		t.Fatalf("pinned = %+v", pinned)
	}

	// The spec cannot change, so a pinned digest always names what a
	// provider can still read.
	changed := policy.DeepCopyObject().(*ops.CleaningPolicy)
	changed.Spec.Assurance = ops.AssurancePurge
	if err := e.admin.Update(e.ctx, changed); !apierrors.IsInvalid(err) {
		t.Fatalf("updating a policy spec = %v, want Invalid", err)
	}

	// Release does not need the policy to exist.
	if err := e.admin.Delete(e.ctx, policy); err != nil {
		t.Fatal(err)
	}
	e.release(allocation, machine(1))
	wipe := &ops.DeviceWipe{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: custody.WipeName(allocation.Name, 1)}, wipe); err != nil {
		t.Fatal(err)
	}
	if wipe.Spec.Policy != pinned.CleaningPolicyReference || wipe.Spec.Assurance != ops.AssuranceClear {
		t.Fatalf("wipe policy = %+v, want %+v", wipe.Spec.Policy, pinned.CleaningPolicyReference)
	}
	// The manifest pinned the server's one drive, and the wipe plans it with
	// the pinned rule's method and check.
	if len(allocation.Spec.Drives) != 1 || allocation.Spec.Drives[0].Name != "disk-0" || allocation.Spec.Drives[0].SerialNumber == "" {
		t.Fatalf("manifest = %+v", allocation.Spec.Drives)
	}
	if len(wipe.Spec.Drives) != 1 || wipe.Spec.Drives[0].ManifestDrive != allocation.Spec.Drives[0] || wipe.Spec.Drives[0].State != "" ||
		wipe.Spec.Drives[0].Verify == nil || wipe.Spec.Drives[0].Verify.Mode != ops.VerifyFull {
		t.Fatalf("plan = %+v", wipe.Spec.Drives)
	}
}

// TestCapacityWithoutAUsableCleaningPolicyIsNotAllocated skips capacity
// whose policy is missing or invalid, or whose drives the policy does not
// cover, and claims nothing.
func TestCapacityWithoutAUsableCleaningPolicyIsNotAllocated(t *testing.T) {
	cases := map[string]func(e *env){
		"missing policy": func(e *env) {
			if err := e.admin.Delete(e.ctx, e.policy()); err != nil {
				e.t.Fatal(err)
			}
		},
		"invalid policy": func(e *env) {
			if err := e.admin.Delete(e.ctx, e.policy()); err != nil {
				e.t.Fatal(err)
			}
			// The CRD admits it; only the webhook, absent here, would refuse
			// Overwrite with Canary. The allocator checks again at pin time.
			invalid := examplePolicy(namespace)
			invalid.Spec.Rules[0].Verify = ops.VerifySpec{Mode: ops.VerifyCanary, Samples: 64}
			e.create(invalid)
		},
	}
	cases["drives not reported"] = func(e *env) {
		device := e.device("server-example-001")
		device.Status.Discovery.Inventory.Systems[0].Storage = nil
		if err := e.admin.Status().Update(e.ctx, device); err != nil {
			e.t.Fatal(err)
		}
	}
	cases["a drive no rule covers"] = func(e *env) {
		device := e.device("server-example-001")
		device.Status.Discovery.Inventory.Systems[0].Storage.Drives[0].Protocol = "SAS"
		if err := e.admin.Status().Update(e.ctx, device); err != nil {
			e.t.Fatal(err)
		}
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := startEnv(t)
			device := e.server(1)
			setup(e)
			if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) || !errors.Is(err, ErrCleaningUncovered) {
				t.Fatalf("allocate = %v, want ErrInsufficientCapacity that says the cleaning policy was not usable", err)
			}
			if found, err := e.allocator.Get(e.ctx, machine(1)); err != nil || found != nil {
				t.Fatalf("allocation = %v, %v", found, err)
			}
			if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderNone {
				t.Fatalf("holder = %s, want None", holder)
			}
		})
	}
}

// TestARecreatedPolicyChangesNoPin recreates a policy under the same name
// with other content. The allocation keeps the copy and digest it pinned, its
// release wipes under that pin, and a new allocation pins the new content.
func TestARecreatedPolicyChangesNoPin(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	e.server(2)
	first, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	original := first.Spec.CleaningPolicy
	if err := e.admin.Delete(e.ctx, e.policy()); err != nil {
		t.Fatal(err)
	}
	recreated := examplePolicy(namespace)
	recreated.Spec.Rules[0].TimePerTiB.Duration *= 2
	e.create(recreated)
	second, err := e.allocator.Allocate(e.ctx, e.request(2))
	if err != nil {
		t.Fatal(err)
	}
	newDigest, err := cleaning.Digest(recreated.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if second.Spec.CleaningPolicy.Name != original.Name || second.Spec.CleaningPolicy.Digest != newDigest || second.Spec.CleaningPolicy.Digest == original.Digest {
		t.Fatalf("a new allocation must pin the recreated content: %+v", second.Spec.CleaningPolicy.CleaningPolicyReference)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(first), first); err != nil {
		t.Fatal(err)
	}
	if first.Spec.CleaningPolicy.CleaningPolicyReference != original.CleaningPolicyReference || first.Spec.CleaningPolicy.Spec.Rules[0].TimePerTiB != original.Spec.Rules[0].TimePerTiB {
		t.Fatalf("the first allocation's pin changed: %+v", first.Spec.CleaningPolicy)
	}
	e.release(first, machine(1))
	wipe := &ops.DeviceWipe{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: custody.WipeName(first.Name, 1)}, wipe); err != nil {
		t.Fatal(err)
	}
	if wipe.Spec.Policy != original.CleaningPolicyReference {
		t.Fatalf("the release must wipe under the original pin: %+v, want %+v", wipe.Spec.Policy, original.CleaningPolicyReference)
	}
}

// TestReleaseWaitsWhileADriveCannotBeCleaned creates no wipe while a pinned
// drive is missing, so a gap in discovery spends no wipe step, and plans
// again when the drive returns.
func TestReleaseWaitsWhileADriveCannotBeCleaned(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	saved := e.device(device.Name).Status.Discovery.Inventory.Systems[0].Storage.Drives
	setDrives := func(drives []inventory.ObservedDrive) {
		t.Helper()
		current := e.device(device.Name)
		current.Status.Discovery.Inventory.Systems[0].Storage.Drives = drives
		if err := e.admin.Status().Update(e.ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	setDrives(nil)
	var blocked error
	e.until("release reports the missing drive", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
			t.Fatal(err)
		}
		_, blocked = e.allocator.Release(e.ctx, allocation, machine(1))
		return errors.Is(blocked, ErrCleaningBlocked)
	})
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
		t.Fatal(err)
	}
	condition := apiMeta.FindStatusCondition(allocation.Status.Conditions, ConditionCleaning)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != custody.ReasonDriveMissing || allocation.Status.Wipe != nil {
		t.Fatalf("condition = %+v, wipe = %+v", condition, allocation.Status.Wipe)
	}
	wipes := &ops.DeviceWipeList{}
	if err := e.admin.List(e.ctx, wipes); err != nil || len(wipes.Items) != 0 {
		t.Fatalf("no wipe may exist while a drive cannot be cleaned: %d, %v", len(wipes.Items), err)
	}
	setDrives(saved)
	e.release(allocation, machine(1))
	if apiMeta.FindStatusCondition(allocation.Status.Conditions, ConditionCleaning) != nil {
		t.Fatal("the Cleaning condition must clear once a wipe is planned")
	}
}

// TestPlanMismatchSpendsNoWipeStep counts only failures that are not plan
// mismatches toward the release's limit.
func TestPlanMismatchSpendsNoWipeStep(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	for step, reason := range map[int32]string{1: custody.ReasonPlanMismatch, 2: custody.ReasonPlanMismatch, 3: "TargetDisksUnknown"} {
		wipe := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: custody.WipeName("alloc-steps", step), Namespace: namespace}, Spec: ops.DeviceWipeSpec{
			OperationEnvelope: ops.OperationEnvelope{Creator: ops.Creator{Username: releaseUser, UID: uid(802)}, DeviceRef: ops.DeviceReference{Name: device.Name},
				Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: "alloc-steps"}}},
			Step: step, Policy: ops.CleaningPolicyReference{Name: "p", Generation: 1, Digest: "sha256:" + strings.Repeat("a", 64)}, Assurance: ops.AssuranceClear,
			Drives: []ops.PlannedDrive{{ManifestDrive: ops.ManifestDrive{Name: "disk-0", SerialNumber: "SN", Model: "M", Firmware: "F", Protocol: ops.ProtocolNVMe, MediaType: ops.MediaSSD, CapacityBytes: 1}, State: ops.DriveMissing}}}}
		e.create(wipe)
		wipe.Status = ops.OperationStatus{Phase: ops.PhaseFailed, Reason: reason}
		if err := e.admin.Status().Update(e.ctx, wipe); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := e.allocator.failedSteps(e.ctx, "alloc-steps", 3)
	if err != nil || failed != 1 {
		t.Fatalf("failed steps = %d, %v; want 1", failed, err)
	}
}

// TestInstallTargetMustBeAPinnedDrive skips capacity whose drives do not
// include the disk installations target.
func TestInstallTargetMustBeAPinnedDrive(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	e.allocator.TargetDisks = []string{"disk-1"}
	if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) || !errors.Is(err, ErrCleaningUncovered) {
		t.Fatalf("allocate = %v", err)
	}
}

// TestARetriedWipeAdoptsTheExistingOne keeps a wipe whose create response was
// lost even when discovery changed before the retry: admission plans it again.
func TestARetriedWipeAdoptsTheExistingOne(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	reference := fleetv1.WipeReference{Name: custody.WipeName(allocation.Name, 1), Step: 1}
	existing := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: reference.Name, Namespace: namespace}, Spec: ops.DeviceWipeSpec{
		OperationEnvelope: e.allocator.envelope(allocation, e.allocator.ReleaseCreator), Step: 1,
		Policy: allocation.Spec.CleaningPolicy.CleaningPolicyReference, Assurance: ops.AssuranceClear,
		Drives: []ops.PlannedDrive{{ManifestDrive: allocation.Spec.Drives[0], State: ops.DriveMissing}}}}
	e.create(existing)
	if err := e.allocator.wipe(e.ctx, allocation, reference); err != nil {
		t.Fatalf("wipe = %v, want the existing wipe adopted", err)
	}
	got := &ops.DeviceWipe{}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(existing), got); err != nil || got.Spec.Drives[0].State != ops.DriveMissing {
		t.Fatalf("the existing wipe must stand: %+v, %v", got.Spec.Drives, err)
	}
}

// TestAWipeIsNeverPlannedForARecreatedDevice deletes the allocated Device and
// creates another under its name. The release controller must not plan a
// wipe from the new Device's drives: the allocation's device-uid label names
// the server it pinned.
func TestAWipeIsNeverPlannedForARecreatedDevice(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	device := e.device("server-example-001")
	if err := e.admin.Delete(e.ctx, device); err != nil {
		t.Fatal(err)
	}
	replacement := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: namespace}, Spec: device.Spec}
	e.until("the Device is recreated", func() bool { return e.admin.Create(e.ctx, replacement) == nil })
	if replacement.UID == device.UID {
		t.Fatal("the recreated Device has the old UID")
	}
	// The new Device reports the same drives, so only the incarnation check
	// stands between it and a wipe.
	replacement.Status = device.Status
	if err := e.admin.Status().Update(e.ctx, replacement); err != nil {
		t.Fatal(err)
	}
	reference := fleetv1.WipeReference{Name: custody.WipeName(allocation.Name, 1), Step: 1}
	if err := e.allocator.wipe(e.ctx, allocation, reference); !errors.Is(err, ErrCustodyLost) {
		t.Fatalf("wipe = %v, want ErrCustodyLost", err)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: reference.Name}, &ops.DeviceWipe{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no wipe may be created for a recreated Device: %v", err)
	}
}

// TestAPulledDriveIsDisposedAndReleaseFinishes holds the server once a pinned
// drive stays missing past the grace, and finishes release after staff record
// the drive's disposal.
func TestAPulledDriveIsDisposedAndReleaseFinishes(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	e.allocator.CleaningGrace = &zero
	pulled := e.device(device.Name)
	pulled.Status.Discovery.Inventory.Systems[0].Storage.Drives = nil
	if err := e.admin.Status().Update(e.ctx, pulled); err != nil {
		t.Fatal(err)
	}
	var hold *ops.RecoveryHold
	e.until("the release holds the server for the missing drive", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
			t.Fatal(err)
		}
		if _, err := e.allocator.Release(e.ctx, allocation, machine(1)); err != nil && !errors.Is(err, ErrCleaningBlocked) {
			t.Logf("release: %v", err)
		}
		hold = e.custody(device.Name).Spec.RecoveryHold
		return hold != nil && hold.Reason == custody.ReasonDriveMissing
	})
	staff := ops.Creator{Username: "staff-example@example.com", UID: uid(803)}
	recovery := &ops.DeviceRecovery{ObjectMeta: metav1.ObjectMeta{GenerateName: "dispose-", Namespace: namespace}, Spec: ops.DeviceRecoverySpec{
		OperationEnvelope: ops.OperationEnvelope{Creator: staff, DeviceRef: ops.DeviceReference{Name: device.Name, UID: string(device.UID)},
			Authority: ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: "drive pulled during the allocation"}}},
		Decision: ops.DecisionDisposeDrive,
		Hold:     &ops.HeldOperationReference{Kind: hold.Kind, Name: hold.Name, Fence: hold.Fence},
		Evidence: ops.RecoveryEvidence{ObservedAt: metav1.Now(), Summary: "drive destroyed after removal"},
		Drive:    &ops.DriveDisposal{Name: "disk-0", Method: ops.DisposalDestroyed, Reference: "ticket-example-1"},
	}}
	e.create(recovery)
	e.release(allocation, machine(1))
	state := e.custody(device.Name)
	if state.Spec.Holder.Type != ops.HolderNone || len(state.Spec.Holder.DisposedDrives) != 0 {
		t.Fatalf("after release: holder %+v", state.Spec.Holder)
	}
}
