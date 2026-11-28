package custody

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const powerKind = "DevicePowerOperation"

var allActions = []string{"On", "ForceOff", "ForceRestart", "PowerCycle", "GracefulShutdown"}

// discoverPower records a discovery that read the recorded system with its
// power state and offered reset types, as BMC discovery would.
func (e *env) discoverPower(device *inventory.Device, key string, at time.Time, state string, resetTypes []string, mutate func(*inventory.DeviceDiscovery)) {
	e.t.Helper()
	e.discover(device, key, at)
	discovery := device.Status.Discovery
	discovery.Inventory.Systems = []inventory.ObservedSystem{{ID: "1", UUID: device.Spec.Identity.SystemUUID, PowerState: state, ResetTypes: resetTypes}}
	if mutate != nil {
		mutate(discovery)
	}
	if err := e.admin.Status().Update(e.ctx, device); err != nil {
		e.t.Fatal(err)
	}
}

// powered creates a Verified Device whose discovery offers every power
// action and reads it On.
func (e *env) powered(n int) (*inventory.Device, string) {
	e.t.Helper()
	key := fmt.Sprintf("%064x", n)
	device := e.verified(fmt.Sprintf("server-power-%03d", n), uid(n), key)
	e.discoverPower(device, key, e.clock.Now(), "On", allActions, nil)
	return device, key
}

func (e *env) staffPower(device *inventory.Device, action ops.PowerAction, expected string) *ops.DevicePowerOperation {
	e.t.Helper()
	operation := &ops.DevicePowerOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: device.Name + "-power-", Namespace: "default"}, Spec: ops.DevicePowerOperationSpec{
		OperationEnvelope: envelope(device, staffUser, service("hung in firmware during qualification")), Action: action, ExpectedSystemUUID: expected,
	}}
	e.create(operation)
	return operation
}

// namedStaffPower creates a staff power operation with an explicit name, such
// as a retry's.
func (e *env) namedStaffPower(device *inventory.Device, name string, action ops.PowerAction) *ops.DevicePowerOperation {
	e.t.Helper()
	operation := &ops.DevicePowerOperation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: ops.DevicePowerOperationSpec{
		OperationEnvelope: envelope(device, staffUser, service("retry after recovery")), Action: action, ExpectedSystemUUID: device.Spec.Identity.SystemUUID,
	}}
	e.create(operation)
	return operation
}

func (e *env) providerPower(device *inventory.Device, allocation string, generation int64, action ops.PowerAction, name string) *ops.DevicePowerOperation {
	e.t.Helper()
	if name == "" {
		name = PowerName(allocation, generation)
	}
	operation := &ops.DevicePowerOperation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: ops.DevicePowerOperationSpec{
		OperationEnvelope: envelope(device, providerUser, allocated(allocation)), Action: action, MachineGeneration: generation,
	}}
	e.create(operation)
	return operation
}

// requireCustodyUnchanged fails if anything in custody changed.
func (e *env) requireCustodyUnchanged(name string, before ops.DeviceCustodySpec) {
	e.t.Helper()
	if after := e.custody(name).Spec; !reflect.DeepEqual(after, before) {
		e.t.Fatalf("custody changed:\n before %+v\n after  %+v", before, after)
	}
}

// powerHold runs an admitted power operation to OutcomeUnknown and returns
// the hold.
func (e *env) powerHold(device *inventory.Device, operation *ops.DevicePowerOperation) ops.RecoveryHold {
	e.t.Helper()
	e.settle(device.Name)
	e.requirePhase(operation, ops.PhaseAdmitted, "")
	if err := e.result(device.Name, ops.PhaseOutcomeUnknown, ""); err != nil {
		e.t.Fatal(err)
	}
	e.settle(device.Name)
	hold := e.custody(device.Name).Spec.RecoveryHold
	if hold == nil || hold.Kind != powerKind || hold.Name != operation.Name {
		e.t.Fatalf("hold = %+v", hold)
	}
	return *hold
}

// applyRecovery admits a recovery and lets the site confirm its fence.
func (e *env) applyRecovery(device *inventory.Device, recovery *ops.DeviceRecovery) ops.OperationStatus {
	e.t.Helper()
	e.settle(device.Name)
	if active := e.custody(device.Name).Spec.ActiveOperation; active != nil {
		e.sites.report(device.Name, active.Fence)
		e.settle(device.Name)
	}
	return e.status(recovery)
}

func powerEvidence(at time.Time, before, after string, reset ops.ResetEvidence) string {
	sent := at.Add(time.Second)
	evidence := fmt.Sprintf(`{"requestSentAt":%q,"power":{"before":{"state":%q,"observedAt":%q},"after":{"state":%q,"observedAt":%q}}`,
		sent.Format(time.RFC3339), before, at.Format(time.RFC3339), after, sent.Add(30*time.Second).Format(time.RFC3339))
	if reset != "" {
		evidence += fmt.Sprintf(`,"resetEvidence":%q`, reset)
	}
	return evidence + "}"
}

func TestPowerOperations(t *testing.T) {
	e := startEnv(t)
	n := 1100
	fresh := func(e *env) (*inventory.Device, string) {
		n++
		return e.powered(n)
	}

	t.Run("staff power is admitted from None with its inputs and what the reset-type check read", func(t *testing.T) {
		e := e.sub(t)
		device, _ := fresh(e)
		before := e.custody(device.Name).Spec.Fence
		operation := e.staffPower(device, ops.PowerForceRestart, device.Spec.Identity.SystemUUID)
		e.settle(device.Name)
		custody := e.custody(device.Name)
		active := custody.Spec.ActiveOperation
		if active == nil || active.Kind != powerKind || active.Fence != before+1 || custody.Spec.Holder.Type != ops.HolderService {
			t.Fatalf("custody after admission = %+v", custody.Spec)
		}
		digest, _ := SpecDigest(operation.Spec)
		if active.InputDigest != digest || !active.Deadline.Time.Equal(e.clock.Now().Add(PowerDeadline)) {
			t.Fatalf("active = %+v", active)
		}
		want := map[string]string{SnapshotResetTypes: "On,ForceOff,ForceRestart,PowerCycle", SnapshotSystemID: "1", SnapshotResetTypesObservedAt: e.clock.Now().Format(time.RFC3339)}
		if !reflect.DeepEqual(active.Snapshot.Extra, want) || active.Snapshot.SystemUUID != device.Spec.Identity.SystemUUID {
			t.Fatalf("snapshot = %+v", active.Snapshot)
		}
		status := e.requirePhase(operation, ops.PhaseAdmitted, "")
		if status.Snapshot == nil || !reflect.DeepEqual(status.Snapshot.Extra, want) {
			t.Fatalf("status snapshot = %+v", status.Snapshot)
		}
		// The site's evidence is recorded in custody and copied to status.
		if err := e.result(device.Name, ops.PhaseSucceeded, powerEvidence(e.clock.Now(), "On", "On", ops.ResetLastResetTime)); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		status = e.requirePhase(operation, ops.PhaseSucceeded, "")
		var evidence ops.PowerEvidence
		if err := jsonUnmarshal(status.Evidence.Raw, &evidence); err != nil || evidence.ResetEvidence != ops.ResetLastResetTime || evidence.Power.After.State != ops.PowerStateOn {
			t.Fatalf("evidence = %s (%v)", status.Evidence.Raw, err)
		}
		if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderNone {
			t.Fatalf("holder = %s", holder)
		}
	})

	t.Run("each power refusal admits nothing and leaves custody unchanged", func(t *testing.T) {
		for _, refusal := range []struct {
			name, reason string
			action       ops.PowerAction
			expected     func(*inventory.Device) string
			discovery    func(*inventory.DeviceDiscovery)
		}{
			{name: "the expected system UUID is not the recorded one", reason: ReasonSystemUUIDMismatch, expected: func(*inventory.Device) string { return uid(9999) }},
			{name: "discovery read the inventory at another endpoint", reason: ReasonDiscoveryNotCurrent, discovery: func(d *inventory.DeviceDiscovery) { d.Inventory.Endpoint = "https://[2001:db8::99]" }},
			{name: "discovery did not read the recorded system", reason: ReasonDiscoveryNotCurrent, discovery: func(d *inventory.DeviceDiscovery) { d.Inventory.Systems[0].UUID = uid(9998) }},
			{name: "the BMC does not offer the action", reason: ReasonResetTypeNotOffered, action: ops.PowerCycle, discovery: func(d *inventory.DeviceDiscovery) {
				d.Inventory.Systems[0].ResetTypes = []string{"On", "ForceOff", "ForceRestart"}
			}},
			{name: "discovery offers no reset types", reason: ReasonResetTypeNotOffered, discovery: func(d *inventory.DeviceDiscovery) { d.Inventory.Systems[0].ResetTypes = nil }},
		} {
			t.Run(refusal.name, func(t *testing.T) {
				e := e.sub(t)
				device, key := fresh(e)
				if refusal.discovery != nil {
					e.discoverPower(device, key, e.clock.Now(), "On", allActions, refusal.discovery)
				}
				before := e.custody(device.Name).Spec
				action, expected := ops.PowerOn, device.Spec.Identity.SystemUUID
				if refusal.action != "" {
					action = refusal.action
				}
				if refusal.expected != nil {
					expected = refusal.expected(device)
				}
				operation := e.staffPower(device, action, expected)
				e.settle(device.Name)
				e.requirePhase(operation, ops.PhaseFailed, refusal.reason)
				e.requireCustodyUnchanged(device.Name, before)
			})
		}
	})

	t.Run("DiscoveryNotCurrent uses the injected clock and twice the discovery interval", func(t *testing.T) {
		e := e.sub(t)
		device, key := fresh(e)
		// Power needs a read within twice the ten-minute interval. Its check
		// runs before rule 5, so its reason is the one reported.
		e.discoverPower(device, key, e.clock.Now().Add(-30*time.Minute), "On", allActions, nil)
		coordinator := e.coordinator(e.admin)
		coordinator.Config.DiscoveryInterval = 10 * time.Minute
		before := e.custody(device.Name).Spec
		stale := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
		settleWith(t, e.ctx, coordinator, device.Name)
		e.requirePhase(stale, ops.PhaseFailed, ReasonDiscoveryNotCurrent)
		e.requireCustodyUnchanged(device.Name, before)
		// The same read is current for an operation decided ten minutes
		// earlier on the injected clock.
		e.clock.Advance(-11 * time.Minute)
		current := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
		settleWith(t, e.ctx, coordinator, device.Name)
		e.requirePhase(current, ops.PhaseAdmitted, "")
	})

	t.Run("a staff request for a Device with no recorded system UUID is refused SystemUUIDNotRecorded", func(t *testing.T) {
		e := e.sub(t)
		n++
		device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("server-power-%03d", n), Namespace: "default"}, Spec: inventory.DeviceSpec{
			ClaimRef: inventory.LocalReference{Name: "claim-power"}, Identity: inventory.DeviceIdentity{Model: "example", Serial: "serial-power"},
			BMC: &inventory.BMC{Endpoint: "https://[2001:db8::10]"},
		}}
		e.create(device)
		e.discover(device, fmt.Sprintf("%064x", n), e.clock.Now())
		e.settle(device.Name)
		e.sites.report(device.Name, 0)
		e.settle(device.Name)
		before := e.custody(device.Name).Spec
		operation := e.staffPower(device, ops.PowerOn, uid(n))
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonSystemUUIDNotRecorded)
		e.requireCustodyUnchanged(device.Name, before)
	})

	t.Run("power uses the shared reasons for a shared key, a holder and an expired request", func(t *testing.T) {
		e := e.sub(t)
		device, key := fresh(e)
		e.device(fmt.Sprintf("server-twin-%03d", n), uid(5000+n), key)
		before := e.custody(device.Name).Spec
		shared := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
		e.settle(device.Name)
		e.requirePhase(shared, ops.PhaseFailed, ReasonSharedPublicKey)
		e.requireCustodyUnchanged(device.Name, before)

		allocated, _ := fresh(e)
		if err := e.coord.Claim(e.ctx, allocated.Name, allocated.UID, "alloc-held"); err != nil {
			t.Fatal(err)
		}
		before = e.custody(allocated.Name).Spec
		customer := e.staffPower(allocated, ops.PowerOn, allocated.Spec.Identity.SystemUUID)
		e.settle(allocated.Name)
		e.requirePhase(customer, ops.PhaseFailed, ReasonHolderMismatch)
		e.requireCustodyUnchanged(allocated.Name, before)

		late, _ := fresh(e)
		before = e.custody(late.Name).Spec
		expired := e.staffPower(late, ops.PowerOn, late.Spec.Identity.SystemUUID)
		// Expiry counts from the server's creation time, which can be ahead
		// of the injected clock.
		e.clock.Advance(expired.CreationTimestamp.Add(DefaultStaffExpiry).Sub(e.clock.Now()) + time.Second)
		e.settle(late.Name)
		e.requirePhase(expired, ops.PhaseCancelled, ReasonExpired)
		e.requireCustodyUnchanged(late.Name, before)
	})

	t.Run("only the two power rows admit; every other creator and authority pair is refused", func(t *testing.T) {
		e := e.sub(t)
		device, _ := fresh(e)
		allocation := "alloc-rows-" + device.Name
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, allocation); err != nil {
			t.Fatal(err)
		}
		before := e.custody(device.Name).Spec
		var refused []*ops.DevicePowerOperation
		for _, pair := range []struct {
			user      string
			authority ops.Authority
		}{
			{staffUser, allocated(allocation)},
			{releaseUser, allocated(allocation)},
			{providerUser, service("not staff")},
			{releaseUser, service("not staff")},
		} {
			operation := &ops.DevicePowerOperation{ObjectMeta: metav1.ObjectMeta{Name: PowerName(allocation, int64(len(refused)+1)), Namespace: "default"}, Spec: ops.DevicePowerOperationSpec{
				OperationEnvelope: envelope(device, pair.user, pair.authority), Action: ops.PowerOn,
			}}
			if pair.authority.ServiceRequest != nil {
				operation.Name, operation.GenerateName = "", device.Name+"-power-"
				operation.Spec.ExpectedSystemUUID = device.Spec.Identity.SystemUUID
			} else {
				operation.Spec.MachineGeneration = int64(len(refused) + 1)
			}
			e.create(operation)
			refused = append(refused, operation)
		}
		// Pending together, none replaces another: replacement runs only
		// for operations a row admits.
		e.settle(device.Name)
		for _, operation := range refused {
			e.requirePhase(operation, ops.PhaseFailed, ReasonCreatorNotPermitted)
		}
		e.requireCustodyUnchanged(device.Name, before)
		// The provider row: allocationRef, holder Allocation, named from the
		// Machine generation.
		misnamed := e.providerPower(device, allocation, 7, ops.PowerOn, allocation+"-power-other")
		e.settle(device.Name)
		e.requirePhase(misnamed, ops.PhaseFailed, ReasonNameMismatch)
		provider := e.providerPower(device, allocation, 8, ops.PowerForceOff, "")
		e.settle(device.Name)
		e.requirePhase(provider, ops.PhaseAdmitted, "")
		if holder := e.custody(device.Name).Spec.Holder; holder.Type != ops.HolderAllocation || holder.AllocationRef.Name != allocation {
			t.Fatalf("holder = %+v", holder)
		}
		// The staff row needs a generated name.
		free, _ := fresh(e)
		named := e.namedStaffPower(free, free.Name+"-chosen", ops.PowerOn)
		e.settle(free.Name)
		e.requirePhase(named, ops.PhaseFailed, ReasonNameMismatch)
	})

	t.Run("staff power races an allocation and exactly one takes the server", func(t *testing.T) {
		e := e.sub(t)
		for range 5 {
			device, _ := fresh(e)
			operation := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
			coordinator := e.coordinator(e.client())
			allocator := e.coordinator(e.client())
			var claimErr, reconcileErr error
			var group sync.WaitGroup
			group.Add(2)
			go func() { defer group.Done(); reconcileErr = settleQuiet(e.ctx, coordinator, device.Name) }()
			go func() {
				defer group.Done()
				claimErr = allocator.Claim(e.ctx, device.Name, device.UID, "alloc-power-race")
			}()
			group.Wait()
			if reconcileErr != nil {
				t.Fatal(reconcileErr)
			}
			e.settle(device.Name)
			status := e.status(operation)
			custody := e.custody(device.Name)
			switch {
			case claimErr == nil:
				if status.Phase != ops.PhaseFailed || status.Reason != ReasonHolderMismatch || custody.Spec.Holder.Type != ops.HolderAllocation || custody.Spec.ActiveOperation != nil {
					t.Fatalf("claim won but power is %s/%s and custody %+v", status.Phase, status.Reason, custody.Spec)
				}
			case errors.Is(claimErr, ErrUnavailable):
				if status.Phase != ops.PhaseAdmitted || custody.Spec.Holder.Type != ops.HolderService {
					t.Fatalf("power won but is %s and custody %+v", status.Phase, custody.Spec)
				}
			default:
				t.Fatal(claimErr)
			}
		}
	})

	t.Run("a newer pending provider power operation replaces an older one, never an installation", func(t *testing.T) {
		e := e.sub(t)
		device, _ := fresh(e)
		allocation := "alloc-replace-" + device.Name
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, allocation); err != nil {
			t.Fatal(err)
		}
		running := e.installation(device, allocation, "request-a")
		e.settle(device.Name)
		e.requirePhase(running, ops.PhaseAdmitted, "")
		older := e.providerPower(device, allocation, 1, ops.PowerOn, "")
		e.clock.Advance(time.Second)
		pendingInstall := e.installation(device, allocation, "request-b")
		newer := e.providerPower(device, allocation, 2, ops.PowerForceOff, "")
		e.settle(device.Name)
		e.requirePhase(older, ops.PhaseCancelled, ReasonSuperseded)
		e.requirePhase(newer, ops.PhasePending, ReasonOperationInProgress)
		e.requirePhase(pendingInstall, ops.PhasePending, ReasonOperationInProgress)
		e.requirePhase(running, ops.PhaseAdmitted, "")
	})

	t.Run("a staff request naming a customer's allocation never replaces the provider's pending operation", func(t *testing.T) {
		e := e.sub(t)
		device, _ := fresh(e)
		allocation := "alloc-cust-" + device.Name
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, allocation); err != nil {
			t.Fatal(err)
		}
		running := e.installation(device, allocation, "request-running")
		e.settle(device.Name)
		e.requirePhase(running, ops.PhaseAdmitted, "")
		customer := e.providerPower(device, allocation, 1, ops.PowerOn, "")
		e.settle(device.Name)
		e.requirePhase(customer, ops.PhasePending, ReasonOperationInProgress)
		// Newer by at least the API server's one-second timestamp.
		time.Sleep(1100 * time.Millisecond)
		staff := &ops.DevicePowerOperation{ObjectMeta: metav1.ObjectMeta{Name: PowerName(allocation, 2), Namespace: "default"}, Spec: ops.DevicePowerOperationSpec{
			OperationEnvelope: envelope(device, staffUser, allocated(allocation)), Action: ops.PowerForceOff, MachineGeneration: 2,
		}}
		e.create(staff)
		e.settle(device.Name)
		e.requirePhase(staff, ops.PhaseFailed, ReasonCreatorNotPermitted)
		e.requirePhase(customer, ops.PhasePending, ReasonOperationInProgress)
	})

	t.Run("a newer staff power request never cancels an older one", func(t *testing.T) {
		e := e.sub(t)
		device, _ := fresh(e)
		older := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
		time.Sleep(1100 * time.Millisecond)
		newer := e.staffPower(device, ops.PowerForceOff, device.Spec.Identity.SystemUUID)
		e.settle(device.Name)
		// Each is decided on its own: the older is admitted, and the newer
		// then finds a Service holder.
		e.requirePhase(older, ops.PhaseAdmitted, "")
		e.requirePhase(newer, ops.PhaseFailed, ReasonHolderMismatch)
	})

	t.Run("under a provider power hold, newer operations wait and never replace the retry that settles it", func(t *testing.T) {
		e := e.sub(t)
		device, key := fresh(e)
		allocation := "alloc-hold-" + device.Name
		if err := e.coord.Claim(e.ctx, device.Name, device.UID, allocation); err != nil {
			t.Fatal(err)
		}
		held := e.providerPower(device, allocation, 1, ops.PowerOn, "")
		hold := e.powerHold(device, held)
		waiting := e.providerPower(device, allocation, 2, ops.PowerForceOff, "")
		e.settle(device.Name)
		e.requirePhase(waiting, ops.PhasePending, ReasonRecoveryHold)
		// A Retry lets the provider create one settling operation.
		e.discoverPower(device, key, e.clock.Now(), "On", allActions, nil)
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		if status := e.applyRecovery(device, retry); status.Phase != ops.PhaseSucceeded {
			t.Fatalf("retry = %+v", status)
		}
		e.clock.Advance(time.Second)
		newest := e.providerPower(device, allocation, 3, ops.PowerForceOff, "")
		settling := e.providerPower(device, allocation, 1, ops.PowerOn, RetryName(hold, 1))
		e.settle(device.Name)
		e.requirePhase(waiting, ops.PhaseCancelled, ReasonSuperseded)
		e.requirePhase(newest, ops.PhasePending, ReasonRecoveryHold)
		e.requirePhase(settling, ops.PhaseAdmitted, "")
		if active := e.custody(device.Name).Spec.ActiveOperation; active.Settles == nil || *active.Settles != heldReference(hold) {
			t.Fatalf("active = %+v", active)
		}
		if err := e.result(device.Name, ops.PhaseSucceeded, powerEvidence(e.clock.Now(), "Off", "On", "")); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		custody := e.custody(device.Name)
		if custody.Spec.RecoveryHold != nil || custody.Spec.Holder.Type != ops.HolderAllocation {
			t.Fatalf("after the retry succeeded: %+v", custody.Spec)
		}
		e.requirePhase(newest, ops.PhaseAdmitted, "")
	})

	t.Run("a staff power operation settles a staff power hold after Retry", func(t *testing.T) {
		e := e.sub(t)
		device, key := fresh(e)
		hold := e.powerHold(device, e.staffPower(device, ops.PowerCycle, device.Spec.Identity.SystemUUID))
		if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderService {
			t.Fatalf("holder = %s", holder)
		}
		// From holder Service, a staff power operation that does not settle
		// the hold is refused.
		before := e.custody(device.Name).Spec
		blocked := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
		e.settle(device.Name)
		e.requirePhase(blocked, ops.PhaseFailed, ReasonHolderMismatch)
		e.requireCustodyUnchanged(device.Name, before)
		// The staff operation named for the Retry repeats the held action.
		// Created before the Retry is applied, it waits, even past the staff
		// expiry, and is admitted once the Retry is.
		retried := e.namedStaffPower(device, RetryName(hold, 1), ops.PowerCycle)
		e.settle(device.Name)
		e.requirePhase(retried, ops.PhasePending, ReasonRecoveryHold)
		past := retried.CreationTimestamp.Add(DefaultStaffExpiry).Sub(e.clock.Now()) + time.Second
		e.clock.Advance(past)
		e.settle(device.Name)
		e.requirePhase(retried, ops.PhasePending, ReasonRecoveryHold)
		// Back to the present, so the staff Retry created next is current.
		e.clock.Advance(-past)
		e.discoverPower(device, key, e.clock.Now(), "On", allActions, nil)
		retry := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionRetry, Hold: heldRef(hold)})
		if status := e.applyRecovery(device, retry); status.Phase != ops.PhaseSucceeded {
			t.Fatalf("retry = %+v", status)
		}
		e.settle(device.Name)
		e.requirePhase(retried, ops.PhaseAdmitted, "")
		if active := e.custody(device.Name).Spec.ActiveOperation; active.Settles == nil || *active.Settles != heldReference(hold) {
			t.Fatalf("active = %+v", active)
		}
		if err := e.result(device.Name, ops.PhaseSucceeded, powerEvidence(e.clock.Now(), "On", "On", ops.ResetObservedOff)); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		custody := e.custody(device.Name)
		if custody.Spec.RecoveryHold != nil || custody.Spec.Holder.Type != ops.HolderNone {
			t.Fatalf("after the retry succeeded: %+v", custody.Spec)
		}
	})

	t.Run("Settle accepts only discovery read after the operation ended, at the current endpoint, in a settled state", func(t *testing.T) {
		for _, settle := range []struct {
			name      string
			readAt    time.Duration // after the hold began
			state     string
			cited     string
			discovery func(*inventory.DeviceDiscovery)
			reason    string
		}{
			{name: "fresh evidence", readAt: time.Minute, state: "Off", cited: "Off"},
			{name: "stale evidence", readAt: -time.Minute, state: "Off", cited: "Off", reason: ReasonPowerEvidenceNotCurrent},
			{name: "evidence from the wrong endpoint", readAt: time.Minute, state: "Off", cited: "Off", reason: ReasonPowerEvidenceWrongEndpoint,
				discovery: func(d *inventory.DeviceDiscovery) { d.Inventory.Endpoint = "https://[2001:db8::99]" }},
			{name: "an unsettled state", readAt: time.Minute, state: "PoweringOn", cited: "PoweringOn", reason: ReasonPowerStateNotSettled},
			{name: "a cited state discovery did not read", readAt: time.Minute, state: "Off", cited: "On", reason: ReasonPowerEvidenceMismatch},
		} {
			t.Run(settle.name, func(t *testing.T) {
				e := e.sub(t)
				device, key := fresh(e)
				hold := e.powerHold(device, e.staffPower(device, ops.PowerForceOff, device.Spec.Identity.SystemUUID))
				e.clock.Advance(2 * time.Minute)
				readAt := hold.Since.Time.Add(settle.readAt)
				// Identity stays current, so only the settle hook decides.
				e.discoverPower(device, key, readAt, settle.state, allActions, settle.discovery)
				recovery := e.recovery(device, ops.DeviceRecoverySpec{Decision: ops.DecisionSettle, Hold: heldRef(hold), Evidence: ops.RecoveryEvidence{
					ObservedAt: metav1.NewTime(readAt), Summary: "discovery read the power state", Data: map[string]string{SettlePowerState: settle.cited},
				}})
				fence := e.custody(device.Name).Spec.Fence
				status := e.applyRecovery(device, recovery)
				custody := e.custody(device.Name)
				if settle.reason == "" {
					if status.Phase != ops.PhaseSucceeded || custody.Spec.RecoveryHold != nil || custody.Spec.Holder.Type != ops.HolderNone {
						t.Fatalf("settle = %+v, custody %+v", status, custody.Spec)
					}
					return
				}
				e.requirePhase(recovery, ops.PhaseFailed, settle.reason)
				if custody.Spec.RecoveryHold == nil || heldReference(*custody.Spec.RecoveryHold) != heldReference(hold) {
					t.Fatalf("hold cleared: %+v", custody.Spec)
				}
				e.requireUntouched(device.Name, fence)
			})
		}
	})

	t.Run("a success with no evidence, or evidence that does not fit the operation, proves nothing", func(t *testing.T) {
		for name, evidence := range map[string]string{
			"no evidence":                "",
			"a field outside the schema": `{"power":{"after":{"state":"On","observedAt":"2026-01-02T03:04:05Z"}},"raw":"bmc body"}`,
			"reset proof for a power-on": powerEvidence(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "Off", "On", ops.ResetLastResetTime),
		} {
			t.Run(name, func(t *testing.T) {
				e := e.sub(t)
				device, _ := fresh(e)
				operation := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
				e.settle(device.Name)
				if err := e.result(device.Name, ops.PhaseSucceeded, evidence); err != nil {
					t.Fatal(err)
				}
				e.settle(device.Name)
				e.requirePhase(operation, ops.PhaseOutcomeUnknown, ReasonInvalidEvidence)
				if hold := e.custody(device.Name).Spec.RecoveryHold; hold == nil || hold.Name != operation.Name {
					t.Fatalf("hold = %+v", hold)
				}
			})
		}
	})

	t.Run("a refusal needs no evidence", func(t *testing.T) {
		e := e.sub(t)
		device, _ := fresh(e)
		operation := e.staffPower(device, ops.PowerForceRestart, device.Spec.Identity.SystemUUID)
		e.settle(device.Name)
		if err := e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device.Name, Operation: e.custody(device.Name).Spec.ActiveOperation.OperationReference,
			Fence: e.custody(device.Name).Spec.Fence, Phase: ops.PhaseFailed, Reason: ReasonSystemUUIDMismatch}); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonSystemUUIDMismatch)
		if custody := e.custody(device.Name); custody.Spec.RecoveryHold != nil || custody.Spec.Holder.Type != ops.HolderNone {
			t.Fatalf("custody = %+v", custody.Spec)
		}
	})
}

// TestPowerCRDRules checks the power inputs the API server enforces.
func TestPowerCRDRules(t *testing.T) {
	e := startEnv(t)
	device, _ := e.powered(1300)
	staff := func(mutate func(*ops.DevicePowerOperationSpec)) error {
		spec := ops.DevicePowerOperationSpec{OperationEnvelope: envelope(device, staffUser, service("r")), Action: ops.PowerOn, ExpectedSystemUUID: device.Spec.Identity.SystemUUID}
		mutate(&spec)
		return e.admin.Create(e.ctx, &ops.DevicePowerOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: "p-", Namespace: "default"}, Spec: spec})
	}
	for name, mutate := range map[string]func(*ops.DevicePowerOperationSpec){
		"staff without expectedSystemUUID":   func(s *ops.DevicePowerOperationSpec) { s.ExpectedSystemUUID = "" },
		"staff with machineGeneration":       func(s *ops.DevicePowerOperationSpec) { s.MachineGeneration = 1 },
		"staff without a reason":             func(s *ops.DevicePowerOperationSpec) { s.Authority.ServiceRequest.Reason = "" },
		"provider with expectedSystemUUID":   func(s *ops.DevicePowerOperationSpec) { s.Authority = allocated("alloc-a"); s.MachineGeneration = 1 },
		"provider without machineGeneration": func(s *ops.DevicePowerOperationSpec) { s.Authority = allocated("alloc-a"); s.ExpectedSystemUUID = "" },
		"provider with an allocation name too long to name it": func(s *ops.DevicePowerOperationSpec) {
			s.Authority, s.ExpectedSystemUUID, s.MachineGeneration = allocated(strings.Repeat("a", 201)), "", 1
		},
		"an action that is not a power action": func(s *ops.DevicePowerOperationSpec) { s.Action = "GracefulShutdown" },
		"an expected UUID not in canonical form": func(s *ops.DevicePowerOperationSpec) {
			s.ExpectedSystemUUID = "00000000-0000-4000-8000-00000000000A"
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := staff(mutate); !apierrors.IsInvalid(err) {
				t.Fatalf("want Invalid, got %v", err)
			}
		})
	}
	t.Run("the spec is immutable", func(t *testing.T) {
		operation := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
		operation.Spec.Action = ops.PowerForceOff
		if err := e.admin.Update(e.ctx, operation); !apierrors.IsInvalid(err) {
			t.Fatalf("want Invalid, got %v", err)
		}
	})
	t.Run("evidence outside the schema is pruned from status", func(t *testing.T) {
		operation := e.staffPower(device, ops.PowerOn, device.Spec.Identity.SystemUUID)
		operation.Status.Phase = ops.PhasePending
		operation.Status.Evidence = rawExtension([]byte(`{"resetEvidence":"ObservedOff","raw":"bmc body"}`))
		if err := e.admin.Status().Update(e.ctx, operation); err != nil {
			t.Fatal(err)
		}
		stored := &ops.DevicePowerOperation{}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(stored.Status.Evidence.Raw), "raw") {
			t.Fatalf("evidence = %s", stored.Status.Evidence.Raw)
		}
	})
}

// TestPowerChecks covers power's admission checks on their own.
func TestPowerChecks(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	uuid := uid(1)
	device := func() *inventory.Device {
		return &inventory.Device{Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{SystemUUID: uuid}, BMC: &inventory.BMC{Endpoint: "https://[2001:db8::10]"}},
			Status: inventory.DeviceStatus{Discovery: &inventory.DeviceDiscovery{Inventory: &inventory.ObservedInventory{ObservedAt: metav1.NewTime(now.Add(-time.Hour)), Endpoint: "https://[2001:db8::10]",
				Systems: []inventory.ObservedSystem{{ID: "1", UUID: strings.ToUpper(uuid), PowerState: "On", ResetTypes: []string{"On", "ForceOff"}}}}}}}
	}
	staff := func(action ops.PowerAction, expected string) ops.Operation {
		return &ops.DevicePowerOperation{Spec: ops.DevicePowerOperationSpec{OperationEnvelope: ops.OperationEnvelope{Authority: service("r")}, Action: action, ExpectedSystemUUID: expected}}
	}
	provider := &ops.DevicePowerOperation{Spec: ops.DevicePowerOperationSpec{OperationEnvelope: ops.OperationEnvelope{Authority: allocated("alloc-a")}, Action: ops.PowerOn, MachineGeneration: 3}}
	for _, check := range []struct {
		name      string
		operation ops.Operation
		device    func(*inventory.Device)
		now       time.Time
		want      string
	}{
		{name: "admitted", operation: staff(ops.PowerOn, uuid)},
		{name: "a provider operation names no UUID", operation: provider},
		{name: "no recorded UUID", operation: staff(ops.PowerOn, uuid), device: func(d *inventory.Device) { d.Spec.Identity.SystemUUID = "" }, want: ReasonSystemUUIDNotRecorded},
		{name: "another UUID", operation: staff(ops.PowerOn, uid(2)), want: ReasonSystemUUIDMismatch},
		{name: "older than twice the interval", operation: staff(ops.PowerOn, uuid), now: now.Add(61 * time.Minute), want: ReasonDiscoveryNotCurrent},
		{name: "no inventory", operation: provider, device: func(d *inventory.Device) { d.Status.Discovery.Inventory = nil }, want: ReasonDiscoveryNotCurrent},
		{name: "not offered", operation: staff(ops.PowerForceRestart, uuid), want: ReasonResetTypeNotOffered},
	} {
		t.Run(check.name, func(t *testing.T) {
			target := device()
			if check.device != nil {
				check.device(target)
			}
			at := now
			if !check.now.IsZero() {
				at = check.now
			}
			refusal := checkPower(context.Background(), &Admission{Operation: check.operation, Device: target, Now: at, DiscoveryInterval: time.Hour})
			if got := ""; refusal != nil {
				got = refusal.Reason
				if got != check.want {
					t.Fatalf("refusal = %v, want %s", refusal, check.want)
				}
			} else if check.want != "" {
				t.Fatalf("admitted, want %s", check.want)
			}
		})
	}
	// The snapshot records only the power actions offered.
	extra := powerSnapshot(&Admission{Device: device()})
	if extra[SnapshotResetTypes] != "On,ForceOff" || extra[SnapshotSystemID] != "1" || extra[SnapshotResetTypesObservedAt] != "2026-01-02T02:04:05Z" {
		t.Fatalf("snapshot = %v", extra)
	}
}

// TestPowerInputDigestIsStable pins the digest of one input, so a change to
// the encoding or to the fields it covers is a deliberate one.
func TestPowerInputDigestIsStable(t *testing.T) {
	spec := ops.DevicePowerOperationSpec{
		OperationEnvelope: ops.OperationEnvelope{Creator: ops.Creator{Username: staffUser, UID: "staff-uid"}, DeviceRef: ops.DeviceReference{Name: "server-example-001"},
			Authority: ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: "hung in firmware"}}},
		Action: ops.PowerForceRestart, ExpectedSystemUUID: uid(1),
	}
	kind := DevicePowerOperationKind()
	digest, err := kind.Digest(&ops.DevicePowerOperation{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:48414d36478756aef5e66e75143abfb579c299061a875a3b887d014d55a78e86"
	if digest != want {
		t.Fatalf("digest = %s, want %s", digest, want)
	}
	// A round trip through JSON, as the API server stores it, keeps it.
	data, _ := jsonMarshal(&ops.DevicePowerOperation{ObjectMeta: metav1.ObjectMeta{Name: "a", ResourceVersion: "7"}, Spec: spec})
	stored := &ops.DevicePowerOperation{}
	if err := jsonUnmarshal(data, stored); err != nil {
		t.Fatal(err)
	}
	if again, _ := kind.Digest(stored); again != digest {
		t.Fatalf("digest changed across a round trip: %s", again)
	}
	changed := spec
	changed.Action = ops.PowerCycle
	if other, _ := kind.Digest(&ops.DevicePowerOperation{Spec: changed}); other == digest {
		t.Fatal("the digest does not cover the action")
	}
}

func TestPowerEvidenceIsTypedAndBounded(t *testing.T) {
	kind := DevicePowerOperationKind()
	if !kind.EvidenceRequired {
		t.Fatal("power evidence must be required")
	}
	operation := func(action ops.PowerAction) ops.Operation {
		return &ops.DevicePowerOperation{Spec: ops.DevicePowerOperationSpec{Action: action}}
	}
	restart := operation(ops.PowerForceRestart)
	validate := func(raw []byte) ([]byte, error) { return kind.Evidence(restart, raw) }
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := validate([]byte(powerEvidence(at, "On", "On", ops.ResetLastResetTime))); err != nil {
		t.Fatal(err)
	}
	// A refusal before the command carries only the state read before it.
	if _, err := validate([]byte(`{"power":{"before":{"state":"Off","observedAt":"2026-01-02T03:04:05Z"}}}`)); err != nil {
		t.Fatal(err)
	}
	// Reset proof belongs only to a restart or a power cycle.
	for _, action := range []ops.PowerAction{ops.PowerOn, ops.PowerForceOff} {
		if _, err := kind.Evidence(operation(action), []byte(powerEvidence(at, "Off", "On", ops.ResetObservedOff))); err == nil {
			t.Errorf("%s accepted reset proof", action)
		}
	}
	if _, err := kind.Evidence(operation(ops.PowerCycle), []byte(powerEvidence(at, "On", "On", ops.ResetObservedOff))); err != nil {
		t.Fatal(err)
	}
	for name, evidence := range map[string]string{
		"an unknown field":                           `{"power":{},"bmcResponse":"x"}`,
		"trailing data":                              `{} {}`,
		"oversized":                                  `{"resetEvidence":"` + strings.Repeat("x", 1100) + `"}`,
		"an invented power state":                    `{"power":{"after":{"state":"Sleeping","observedAt":"2026-01-02T03:04:05Z"}}}`,
		"an observation with no time":                `{"power":{"after":{"state":"On"}}}`,
		"an invented reset evidence":                 `{"requestSentAt":"2026-01-02T03:04:05Z","resetEvidence":"Guessed"}`,
		"reset evidence with no command":             `{"resetEvidence":"ObservedOff"}`,
		"after read before before":                   `{"power":{"before":{"state":"On","observedAt":"2026-01-02T03:04:05Z"},"after":{"state":"On","observedAt":"2026-01-02T03:04:00Z"}}}`,
		"the command sent before the state was read": `{"requestSentAt":"2026-01-02T03:04:00Z","power":{"before":{"state":"On","observedAt":"2026-01-02T03:04:05Z"}}}`,
		"after read before the command":              `{"requestSentAt":"2026-01-02T03:04:05Z","power":{"after":{"state":"On","observedAt":"2026-01-02T03:04:00Z"}}}`,
	} {
		if _, err := validate([]byte(evidence)); err == nil {
			t.Errorf("%s: accepted %s", name, evidence)
		}
	}
}

// TestPowerRegistration checks the registry entry and rows.
func TestPowerRegistration(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	kind := registry.Kind(powerKind)
	if kind == nil || !kind.Effectful || !kind.Supersedes || !kind.Retryable || !kind.EvidenceRequired || kind.Naming != GeneratedName || kind.RecoverySettle == nil || kind.Deadline != PowerDeadline {
		t.Fatalf("kind = %+v", kind)
	}
	// A power success settles only a power hold, after a Retry.
	if !reflect.DeepEqual(kind.Settles, []SettleRule{{Held: powerKind, RequiresRetry: true}}) {
		t.Fatalf("settles = %+v", kind.Settles)
	}
	for _, other := range registry.Kinds() {
		for _, rule := range other.Settles {
			if rule.Held == powerKind && other.GVK.Kind != powerKind {
				t.Fatalf("%s settles a power hold", other.GVK.Kind)
			}
		}
	}
	var rows []Rule
	for _, rule := range registry.Rules() {
		if rule.Kind == powerKind {
			rows = append(rows, rule)
		}
	}
	// Functions never compare equal, so compare the rows without their
	// name rule and check that separately.
	withoutName := func(rows []Rule) []Rule {
		out := slices.Clone(rows)
		for index := range out {
			out[index].Name = nil
		}
		return out
	}
	if len(rows) != 3 || !reflect.DeepEqual(withoutName(rows), withoutName(PowerRules())) {
		t.Fatalf("rows = %+v", rows)
	}
	for _, reason := range []string{ReasonSystemUUIDNotRecorded, ReasonSystemUUIDMismatch, ReasonDiscoveryNotCurrent, ReasonResetTypeNotOffered, ReasonPowerEvidenceNotCurrent, ReasonPowerEvidenceWrongEndpoint, ReasonPowerStateNotSettled, ReasonPowerEvidenceMismatch} {
		if !registry.Declared(powerKind, reason, RaisedByCoordinator) {
			t.Fatalf("%s is not declared", reason)
		}
	}
	for _, reason := range []string{ReasonSystemUUIDMismatch, ReasonResetTypeNotOffered} {
		if !registry.Declared(powerKind, reason, RaisedByWorker) {
			t.Fatalf("%s is not declared for the worker", reason)
		}
	}
	// Staff rows keep the kind's generated names; the provider row names
	// its operations from the allocation and the Machine generation.
	provider := &ops.DevicePowerOperation{Spec: ops.DevicePowerOperationSpec{OperationEnvelope: ops.OperationEnvelope{Authority: allocated("alloc-a")}, MachineGeneration: 4}}
	for _, row := range rows {
		switch row.Creator {
		case CreatorMetalProvider:
			if row.Naming != DeterministicName || row.Name(provider) != "alloc-a-power-4" {
				t.Fatalf("provider row naming = %v", row.Naming)
			}
		default:
			if row.Naming != KindNaming || row.Name != nil {
				t.Fatalf("staff row naming = %v", row.Naming)
			}
		}
	}
}
