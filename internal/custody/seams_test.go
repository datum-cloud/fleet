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
					if !reflect.DeepEqual(before, after) {
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
	// Credentials that carry no UID are identified by username alone.
	byName := Principals{CreatorMetalProvider: {{Username: "svc-a"}}, CreatorReleaseController: {{Username: "svc-b"}}}
	if err := byName.Validate(); err != nil {
		t.Fatal(err)
	}
	for creator, want := range map[ops.Creator]CreatorClass{
		{Username: "svc-a"}: CreatorMetalProvider,
		{Username: "svc-b"}: CreatorReleaseController,
		{Username: "svc-c"}: CreatorStaff,
	} {
		if got := byName.Classify(creator); got != want {
			t.Fatalf("%+v classified %s, want %s", creator, got, want)
		}
	}
	// A configured UID is checked: a creator without it is staff.
	if got := principals.Classify(ops.Creator{Username: "svc-a"}); got != CreatorStaff {
		t.Fatalf("a creator without the configured UID classified %s", got)
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
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	plan := &ops.DeviceCommissioningPlan{ObjectMeta: metav1.ObjectMeta{Name: "plan", UID: "plan-uid", Generation: 1}, Spec: ops.DeviceCommissioningPlanSpec{EvidenceLifetime: metav1.Duration{Duration: 24 * time.Hour}}}
	pinned := ops.ApprovedPlanReference{PlanReference: ops.PlanReference{Name: "plan", Generation: 1, Digest: "sha256:plan"}, Approval: ApprovalName(plan)}
	passed := func(mutate func(*LatestCommissioning)) *LatestCommissioning {
		latest := &LatestCommissioning{DeviceUID: "device-uid", FinishedAt: now.Add(-time.Hour), Evidence: ops.CommissioningEvidence{
			Plan: pinned, Result: ops.CommissioningPassed,
			SerialNumber: "SERIAL-1", SystemUUID: "uuid-1", BMCPublicKeySHA256: "key", Hardware: &ops.InspectedHardware{Memory: "2Gi"}}}
		if mutate != nil {
			mutate(latest)
		}
		return latest
	}
	free := &ops.DeviceCustodySpec{State: ops.CustodyVerified, Holder: ops.Holder{Type: ops.HolderNone}}
	base := func() CapacityInputs {
		return CapacityInputs{Generation: 3, Now: now, DeviceUID: "device-uid", DeviceSerial: "serial-1", DeviceSystemUUID: "UUID-1", DeviceActive: true,
			Commissioning: passed(nil), CurrentPlan: CurrentPlanInput{Plan: plan, Reference: pinned}, Discovery: DiscoveryInput{Current: true, KeyDigest: "key"},
			Custody: free, AllocationPolicyEnabled: true, Cleaning: CleaningResolved}
	}
	if projection := ProjectCapacity(base()); !projection.Available() || projection.Hardware == nil {
		t.Fatalf("the base inputs are not Available: %+v", projection.Conditions)
	}
	for name, test := range map[string]struct {
		mutate func(*CapacityInputs)
		index  int
		reason string
		status metav1.ConditionStatus
	}{
		"never commissioned":  {func(in *CapacityInputs) { in.Commissioning = nil }, 0, "NotCommissioned", metav1.ConditionFalse},
		"another incarnation": {func(in *CapacityInputs) { in.DeviceUID = "other" }, 0, "NotCommissioned", metav1.ConditionFalse},
		"device replaced":     {func(in *CapacityInputs) { in.DeviceReplaced = true }, 0, "DeviceReplaced", metav1.ConditionFalse},
		"no hardware ID":      {func(in *CapacityInputs) { in.DeviceUnidentified = true }, 0, "DeviceUnidentified", metav1.ConditionFalse},
		"failed":              {func(in *CapacityInputs) { in.Commissioning.Evidence.Result = ops.CommissioningFailed }, 0, "CommissioningFailed", metav1.ConditionFalse},
		"no plan":             {func(in *CapacityInputs) { in.CurrentPlan = CurrentPlanInput{Reason: ReasonNoCurrentPlan} }, 0, ReasonNoCurrentPlan, metav1.ConditionFalse},
		"ambiguous":           {func(in *CapacityInputs) { in.CurrentPlan = CurrentPlanInput{Reason: ReasonPlanAmbiguous} }, 0, ReasonPlanAmbiguous, metav1.ConditionFalse},
		"plan changed":        {func(in *CapacityInputs) { in.CurrentPlan.Reference.Digest = "sha256:new" }, 0, "PlanChanged", metav1.ConditionFalse},
		"plan regenerated":    {func(in *CapacityInputs) { in.CurrentPlan.Reference.Generation = 2 }, 0, "PlanChanged", metav1.ConditionFalse},
		"plan recreated": {func(in *CapacityInputs) {
			again := *plan
			again.UID = "plan-uid-2"
			in.CurrentPlan = CurrentPlanInput{Plan: &again, Reference: ops.ApprovedPlanReference{PlanReference: pinned.PlanReference, Approval: ApprovalName(&again)}}
		}, 0, "PlanChanged", metav1.ConditionFalse},
		"expired":         {func(in *CapacityInputs) { in.Now = now.Add(48 * time.Hour) }, 1, "EvidenceExpired", metav1.ConditionFalse},
		"discovery old":   {func(in *CapacityInputs) { in.Discovery.Current = false }, 1, "DiscoveryNotCurrent", metav1.ConditionFalse},
		"key changed":     {func(in *CapacityInputs) { in.Discovery.KeyDigest = "other" }, 1, "IdentityChanged", metav1.ConditionFalse},
		"serial changed":  {func(in *CapacityInputs) { in.DeviceSerial = "other" }, 1, "IdentityChanged", metav1.ConditionFalse},
		"custody missing": {func(in *CapacityInputs) { in.Custody = nil }, 2, "CustodyMissing", metav1.ConditionFalse},
		"unverified": {func(in *CapacityInputs) {
			in.Custody = &ops.DeviceCustodySpec{State: ops.CustodyUnverified, Holder: ops.Holder{Type: ops.HolderNone}}
		}, 2, "CustodyUnverified", metav1.ConditionFalse},
		"hold":                    {func(in *CapacityInputs) { c := *free; c.RecoveryHold = &ops.RecoveryHold{}; in.Custody = &c }, 2, "RecoveryHold", metav1.ConditionFalse},
		"allocated":               {func(in *CapacityInputs) { c := *free; c.Holder.Type = ops.HolderAllocation; in.Custody = &c }, 2, "Held", metav1.ConditionFalse},
		"disabled":                {func(in *CapacityInputs) { in.AllocationPolicyEnabled = false }, 2, "AllocationDisabled", metav1.ConditionFalse},
		"not active":              {func(in *CapacityInputs) { in.DeviceActive = false }, 2, "DeviceNotActive", metav1.ConditionFalse},
		"duplicate":               {func(in *CapacityInputs) { in.Duplicate = true }, 2, "IdentityConflict", metav1.ConditionFalse},
		"cleaning unresolved":     {func(in *CapacityInputs) { in.Cleaning = CleaningUnresolved }, 2, "CleaningPolicyUnresolved", metav1.ConditionFalse},
		"cleaning owner unknown":  {func(in *CapacityInputs) { in.Cleaning = CleaningUnknown }, 2, "CleaningPolicyUnknown", metav1.ConditionUnknown},
		"cleaning state is empty": {func(in *CapacityInputs) { in.Cleaning = "" }, 2, "CleaningPolicyUnresolved", metav1.ConditionFalse},
	} {
		in := base()
		in.Commissioning = passed(nil)
		test.mutate(&in)
		projection := ProjectCapacity(in)
		condition := projection.Conditions[test.index]
		if condition.Status != test.status || condition.Reason != test.reason || projection.Available() {
			t.Fatalf("%s: %+v", name, projection.Conditions)
		}
		if test.index == 0 && projection.Hardware != nil {
			t.Fatalf("%s: unprepared capacity keeps hardware", name)
		}
		for _, condition := range projection.Conditions {
			if condition.ObservedGeneration != 3 || condition.Reason == "" {
				t.Fatalf("%s: condition = %+v", name, condition)
			}
		}
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
