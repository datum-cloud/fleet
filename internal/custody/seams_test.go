package custody

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRegistryRejectsIncompleteKindsAndRows(t *testing.T) {
	registry := NewRegistry()
	staff := testKinds()[0]
	if err := registry.Register(staff); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(staff); err == nil {
		t.Fatal("a kind registered twice")
	}
	noDeadline := testKinds()[1]
	noDeadline.Deadline = 0
	if err := registry.Register(noDeadline); err == nil {
		t.Fatal("a kind without a deadline registered")
	}
	noName := testKinds()[1]
	noName.Name = nil
	if err := registry.Register(noName); err == nil {
		t.Fatal("a deterministic kind without a name rule registered")
	}
	inert := testKinds()[1]
	inert.Effectful = false
	if err := registry.Register(inert); err == nil {
		t.Fatal("a non-effectful kind without Apply registered")
	}
	skipping := testKinds()[1]
	skipping.SkipsDiscovery = func(ops.Operation) bool { return true }
	if err := registry.Register(skipping); err == nil {
		t.Fatal("a kind that reaches the server skipped discovery")
	}
	if err := registry.AddRule(Rule{Kind: testStaffKind, Creator: CreatorStaff, Authority: AuthorityService, Queue: true}); err == nil {
		t.Fatal("a staff row that queues was added")
	}
	if err := registry.AddRule(Rule{Kind: "Unregistered", Creator: CreatorStaff, Authority: AuthorityService}); err == nil {
		t.Fatal("a row for an unregistered kind was added")
	}
}

// TestAddingRowsLeavesExistingRowsAlone registers the test kinds and rows on
// top of the defaults and checks every built-in kind matches exactly the same
// rows for every creator class, authority and variant.
// sameRows compares rows, with a row's Name rule compared by identity:
// reflect.DeepEqual never finds two non-nil functions equal.
func sameRows(left, right []Rule) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		a, b := left[index], right[index]
		if (a.Name == nil) != (b.Name == nil) || (a.Name != nil && reflect.ValueOf(a.Name).Pointer() != reflect.ValueOf(b.Name).Pointer()) {
			return false
		}
		a.Name, b.Name = nil, nil
		if !reflect.DeepEqual(a, b) {
			return false
		}
	}
	return true
}

func TestAddingRowsLeavesExistingRowsAlone(t *testing.T) {
	base, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	extended := testRegistry(t)
	device := &inventory.Device{}
	for _, kind := range base.Kinds() {
		for _, class := range []CreatorClass{CreatorMetalProvider, CreatorReleaseController, CreatorStaff, "Other"} {
			for _, authority := range []ops.Authority{allocated("alloc-a"), service("r")} {
				for _, decision := range []ops.RecoveryDecision{ops.DecisionSettle, ops.DecisionRetry, ops.DecisionRetire, ops.DecisionResolve} {
					operation := kind.New()
					*operation.Envelope() = envelope(device, "u", authority)
					if recovery, ok := operation.(*ops.DeviceRecovery); ok {
						recovery.Spec.Decision = decision
					}
					before := base.matchRows(kind, operation, class)
					after := extended.matchRows(extended.Kind(kind.GVK.Kind), operation, class)
					if !sameRows(before, after) {
						t.Fatalf("%s %s %+v: rows changed from %v to %v", kind.GVK.Kind, class, authority, before, after)
					}
				}
			}
		}
	}
}

func TestPrincipals(t *testing.T) {
	principals := Principals{CreatorMetalProvider: {{Username: "svc-a", UID: "uid-a"}}, CreatorReleaseController: {{Username: "svc-b"}}}
	if err := principals.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[ops.Creator]CreatorClass{
		{Username: "svc-a", UID: "uid-a"}:   CreatorMetalProvider,
		{Username: "svc-a", UID: "forged"}:  CreatorStaff,
		{Username: "svc-b", UID: "any"}:     CreatorReleaseController,
		{Username: "person", UID: "person"}: CreatorStaff,
	}
	for creator, want := range cases {
		if got := principals.Classify(creator); got != want {
			t.Fatalf("%+v classified %s, want %s", creator, got, want)
		}
	}
	for _, bad := range []Principals{{CreatorStaff: {{Username: "x"}}}, {CreatorMetalProvider: {{}}}, {CreatorMetalProvider: {{Username: "x"}}, CreatorReleaseController: {{Username: "x"}}}} {
		if bad.Validate() == nil {
			t.Fatalf("%v validated", bad)
		}
	}
}

func TestCandidatesOrderOldestFirstWithADeterministicTieBreak(t *testing.T) {
	registry := testRegistry(t)
	at := func(seconds int) metav1.Time { return metav1.NewTime(time.Unix(1_700_000_000+int64(seconds), 0)) }
	newCandidate := func(kind, name, uid string, created metav1.Time) candidate {
		k := registry.Kind(kind)
		operation := k.New()
		operation.SetName(name)
		operation.SetUID(typesUID(uid))
		operation.SetCreationTimestamp(created)
		return candidate{operation: operation, kind: k}
	}
	list := []candidate{
		newCandidate("DeviceWipe", "b", "2", at(1)),
		newCandidate("DeviceInstallation", "z", "9", at(1)),
		newCandidate("DeviceWipe", "b", "1", at(1)),
		newCandidate("DeviceWipe", "a", "3", at(1)),
		newCandidate("DeviceInstallation", "y", "8", at(0)),
	}
	for range 5 {
		shuffled := slices.Clone(list)
		slices.Reverse(shuffled)
		orderCandidates(shuffled)
		var got []string
		for _, c := range shuffled {
			got = append(got, fmt.Sprintf("%s/%s/%s", c.kind.GVK.Kind, c.operation.GetName(), c.operation.GetUID()))
		}
		want := []string{"DeviceInstallation/y/8", "DeviceInstallation/z/9", "DeviceWipe/a/3", "DeviceWipe/b/1", "DeviceWipe/b/2"}
		if !slices.Equal(got, want) {
			t.Fatalf("order = %v", got)
		}
	}
}

func TestEvidenceIsTypedAndBounded(t *testing.T) {
	kind := DeviceWipeKind()
	if _, err := kind.Evidence(&ops.DeviceWipe{}, []byte(`{"wipedDisks":["disk-0"]}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"bmcResponse":"raw"}`, `{"wipedDisks":["disk-0"]} {}`, `{"steps":[{"name":""}]}`, fmt.Sprintf(`{"policyDigest":"%0*d"}`, evidenceLimit, 0)} {
		if _, err := kind.Evidence(&ops.DeviceWipe{}, []byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestCapacityProjection(t *testing.T) {
	plan := "sha256:plan"
	commissionings := map[string]*CommissioningEvidence{"none": nil, "old plan": {PlanDigest: "sha256:old", HealthCurrent: true}, "unhealthy": {PlanDigest: plan}, "current": {PlanDigest: plan, HealthCurrent: true}}
	hold := &ops.RecoveryHold{Reason: "x"}
	custodies := map[string]*ops.DeviceCustodySpec{
		"missing":    nil,
		"unverified": {State: ops.CustodyUnverified, Holder: ops.Holder{Type: ops.HolderNone}},
		"held":       {State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderNone}, RecoveryHold: hold},
		"allocated":  {State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderAllocation}},
		"releasing":  {State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderRelease}},
		"service":    {State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderService}},
		"retired":    {State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderRetired}},
		"free":       {State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderNone}},
	}
	available := 0
	for commissioningName, commissioning := range commissionings {
		for custodyName, custody := range custodies {
			for _, discovery := range []bool{false, true} {
				for _, enabled := range []bool{false, true} {
					conditions := ProjectCapacity(CapacityInputs{Generation: 3, Commissioning: commissioning, CurrentPlanDigest: plan, DiscoveryCurrent: discovery, Custody: custody, AllocationPolicyEnabled: enabled})
					prepared := conditions[0].Status == metav1.ConditionTrue
					healthy := conditions[1].Status == metav1.ConditionTrue
					free := conditions[2].Status == metav1.ConditionTrue
					if prepared != (commissioningName == "current" || commissioningName == "unhealthy") {
						t.Fatalf("%s: Prepared = %v", commissioningName, prepared)
					}
					if healthy != (commissioningName != "none" && commissioning.HealthCurrent && discovery) {
						t.Fatalf("%s discovery %v: Healthy = %v", commissioningName, discovery, healthy)
					}
					want := prepared && healthy && custodyName == "free" && enabled
					if free != want {
						t.Fatalf("%s %s discovery %v enabled %v: Available = %v (%s)", commissioningName, custodyName, discovery, enabled, free, conditions[2].Reason)
					}
					if free {
						available++
					}
					for _, condition := range conditions {
						if condition.ObservedGeneration != 3 || condition.Reason == "" {
							t.Fatalf("condition = %+v", condition)
						}
					}
				}
			}
		}
	}
	if available != 1 {
		t.Fatalf("%d combinations are Available, want exactly one", available)
	}
}

func TestCRDRules(t *testing.T) {
	e := startEnv(t)
	ctx := context.Background()
	device := e.verified("server-example-600", uid(600), fmt.Sprintf("%064x", 600))
	requireInvalid := func(t *testing.T, err error) {
		t.Helper()
		if !apierrors.IsInvalid(err) {
			t.Fatalf("want Invalid, got %v", err)
		}
	}
	t.Run("operation spec is immutable", func(t *testing.T) {
		operation := e.staffOp(device, "a")
		operation.Spec.Input = "b"
		requireInvalid(t, e.admin.Update(ctx, operation))
	})
	t.Run("authority is exactly one of allocationRef and serviceRequest", func(t *testing.T) {
		both := ops.Authority{AllocationRef: &ops.LocalReference{Name: "a"}, ServiceRequest: &ops.ServiceRequest{Reason: "r"}}
		requireInvalid(t, e.admin.Create(ctx, &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: "x-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, both), Input: "a"}}))
		requireInvalid(t, e.admin.Create(ctx, &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: "x-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, ops.Authority{}), Input: "a"}}))
	})
	t.Run("operation phases move forward only and ended phases are final", func(t *testing.T) {
		operation := e.staffOp(device, "a")
		write := func(phase ops.Phase) error {
			if err := e.admin.Get(ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
				t.Fatal(err)
			}
			operation.Status.Phase = phase
			return e.admin.Status().Update(ctx, operation)
		}
		for _, phase := range []ops.Phase{ops.PhasePending, ops.PhaseAdmitted, ops.PhaseVerifying} {
			if err := write(phase); err != nil {
				t.Fatal(err)
			}
		}
		requireInvalid(t, write(ops.PhaseDispatched))
		requireInvalid(t, write(ops.PhasePending))
		if err := write(ops.PhaseOutcomeUnknown); err != nil {
			t.Fatal(err)
		}
		requireInvalid(t, write(ops.PhaseSucceeded))
		requireInvalid(t, write("Invented"))
	})
	t.Run("recovery inputs match the decision", func(t *testing.T) {
		base := ops.DeviceRecoverySpec{OperationEnvelope: envelope(device, staffUser, service("r")), Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(time.Now()), Summary: "s"}}
		for _, spec := range []func(ops.DeviceRecoverySpec) ops.DeviceRecoverySpec{
			func(s ops.DeviceRecoverySpec) ops.DeviceRecoverySpec { s.Decision = ops.DecisionSettle; return s },
			func(s ops.DeviceRecoverySpec) ops.DeviceRecoverySpec { s.Decision = ops.DecisionResolve; return s },
			func(s ops.DeviceRecoverySpec) ops.DeviceRecoverySpec {
				s.Decision, s.Hold, s.DiskDisposal = ops.DecisionSettle, &ops.HeldOperationReference{Kind: "k", Name: "n"}, &ops.DiskDisposal{Method: ops.DisposalDestroyed, Reference: "r"}
				return s
			},
		} {
			requireInvalid(t, e.admin.Create(ctx, &ops.DeviceRecovery{ObjectMeta: metav1.ObjectMeta{GenerateName: "r-", Namespace: "default"}, Spec: spec(base)}))
		}
	})
	t.Run("custody fence never decreases, names its Device, and Retired is terminal", func(t *testing.T) {
		custody := e.custody(device.Name)
		custody.Spec.Fence--
		requireInvalid(t, e.admin.Update(ctx, custody))
		custody = e.custody(device.Name)
		custody.Spec.DeviceRef.Name = "another"
		requireInvalid(t, e.admin.Update(ctx, custody))
		requireInvalid(t, e.admin.Create(ctx, &ops.DeviceCustody{ObjectMeta: metav1.ObjectMeta{Name: "mismatch", Namespace: "default"}, Spec: ops.DeviceCustodySpec{DeviceRef: ops.DeviceReference{Name: "other"}, State: ops.CustodyUnverified, Holder: ops.Holder{Type: ops.HolderNone}}}))
		custody = e.custody(device.Name)
		custody.Spec.Holder = ops.Holder{Type: ops.HolderAllocation}
		requireInvalid(t, e.admin.Update(ctx, custody))
	})
}

func typesUID(value string) types.UID { return types.UID(value) }
