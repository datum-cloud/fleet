package custody

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	planAuthor   = "plan-author@example.com"
	planApprover = "plan-approver@example.com"
	modelLabel   = "fleet.datumapis.com/hardware-model"
	burnInDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

func planContent(burnIn time.Duration) ops.CommissioningPlanContent {
	return ops.CommissioningPlanContent{
		Inspection: ops.CommissioningInspection{CPU: ops.InspectionCPU{Architecture: "amd64", Cores: 2}, Memory: ops.InspectionValue{Capacity: "2Gi"},
			Disks: ops.InspectionDisks{Count: 1, MinCapacity: "2Gi"}, Interfaces: ops.InspectionCount{Count: 1}},
		Firmware: []ops.FirmwareBaseline{{Component: "BMC", Version: "1.0.0"}},
		BurnIn:   ops.CommissioningBurnIn{Image: ops.ImageInput{Name: "burn-in", Digest: burnInDigest}, Duration: metav1.Duration{Duration: burnIn}},
	}
}

// plan creates a plan for one hardware model.
func (e *env) plan(name, model, author string, supersedes ...string) *ops.DeviceCommissioningPlan {
	e.t.Helper()
	plan := &ops.DeviceCommissioningPlan{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: ops.DeviceCommissioningPlanSpec{
		Creator:          ops.Creator{Username: author, UID: author + "-uid"},
		DeviceSelector:   metav1.LabelSelector{MatchLabels: map[string]string{modelLabel: model}},
		Supersedes:       supersedes,
		Content:          planContent(10 * time.Minute),
		EvidenceLifetime: metav1.Duration{Duration: 24 * time.Hour},
	}}
	e.create(plan)
	return plan
}

// approve qualifies the plan as the approver.
func (e *env) approve(plan *ops.DeviceCommissioningPlan, approver string) *ops.DeviceCommissioningPlanApproval {
	e.t.Helper()
	approval := &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: ApprovalName(plan), Namespace: "default"}, Spec: ops.DeviceCommissioningPlanApprovalSpec{
		Creator: ops.Creator{Username: approver, UID: approver + "-uid"},
		PlanRef: e.reference(plan),
	}}
	e.create(approval)
	return approval
}

// model labels the Device with its hardware model.
func (e *env) model(device *inventory.Device, model string) {
	e.t.Helper()
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
		e.t.Fatal(err)
	}
	key := device.Status.Discovery.TLS.PublicKeySHA256
	device.Labels = map[string]string{modelLabel: model}
	if err := e.admin.Update(e.ctx, device); err != nil {
		e.t.Fatal(err)
	}
	e.discover(device, key, e.clock.Now())
}

// reference is the reference to the plan's current content.
func (e *env) reference(plan *ops.DeviceCommissioningPlan) ops.PlanReference {
	e.t.Helper()
	reference, err := PlanReferenceOf(plan)
	if err != nil {
		e.t.Fatal(err)
	}
	return reference
}

func (e *env) commission(device *inventory.Device, plan *ops.DeviceCommissioningPlan, content ops.CommissioningPlanContent) *ops.DeviceCommissioning {
	e.t.Helper()
	return e.commissionUnder(device, ops.ApprovedPlanReference{PlanReference: e.reference(plan), Approval: ApprovalName(plan)}, content)
}

// commissionUnder commissions the Device under an exact plan reference.
func (e *env) commissionUnder(device *inventory.Device, reference ops.ApprovedPlanReference, content ops.CommissioningPlanContent) *ops.DeviceCommissioning {
	e.t.Helper()
	operation := &ops.DeviceCommissioning{ObjectMeta: metav1.ObjectMeta{GenerateName: device.Name + "-commission-", Namespace: "default"}, Spec: ops.DeviceCommissioningSpec{
		OperationEnvelope: envelope(device, staffUser, service("intake")),
		Plan:              ops.CommissioningPlanInput{ApprovedPlanReference: reference, Content: content},
	}}
	e.create(operation)
	return operation
}

// commissioningEvidence is evidence the kind accepts for the operation: the
// identity admission recorded, hardware that meets planContent, and a
// burn-in as long as the plan's.
func (e *env) commissioningEvidence(operation *ops.DeviceCommissioning, result ops.CommissioningResult) string {
	e.t.Helper()
	snapshot := e.status(operation).Snapshot
	started := metav1.NewTime(e.clock.Now())
	evidence := ops.CommissioningEvidence{Plan: operation.Spec.Plan.ApprovedPlanReference, Result: result,
		SerialNumber: snapshot.Extra[SnapshotSerialNumber], SystemUUID: snapshot.SystemUUID, BMCPublicKeySHA256: snapshot.BMCPublicKeySHA256,
		Hardware: &ops.InspectedHardware{CPU: ops.InspectedCPU{Architecture: "amd64", Cores: 2}, Memory: "2Gi",
			Disks: []ops.InspectedDisk{{Name: "disk-0", Capacity: "2Gi"}}, Interfaces: []ops.InspectedInterface{{Name: "nic-0"}}},
		Firmware: []ops.FirmwareBaseline{{Component: "BMC", Version: "1.0.0"}},
		BurnIn:   &ops.BurnInEvidence{Result: ops.BurnInPassed, StartedAt: started, CompletedAt: metav1.NewTime(started.Add(operation.Spec.Plan.Content.BurnIn.Duration.Duration))}}
	if result == ops.CommissioningFailed {
		evidence.FailedPhase, evidence.Mismatches = ops.PhaseBurnIn, []string{"memory test failed"}
		evidence.BurnIn.Result = ops.BurnInFailed
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

func TestCommissioning(t *testing.T) {
	root := startEnv(t)
	n := 0
	next := func(t *testing.T, model string) (*env, *inventory.Device) {
		e := root.sub(t)
		n++
		name := fmt.Sprintf("commission-%d", n)
		device := e.verified(name, uid(700+n), fmt.Sprintf("%064x", 1000+n))
		e.model(device, model)
		return e, device
	}

	t.Run("the CRDs refuse malformed plans and approvals", func(t *testing.T) {
		e := root.sub(t)
		valid := func(name string) *ops.DeviceCommissioningPlan {
			return &ops.DeviceCommissioningPlan{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: ops.DeviceCommissioningPlanSpec{
				Creator: ops.Creator{Username: planAuthor, UID: "u"}, DeviceSelector: metav1.LabelSelector{MatchLabels: map[string]string{modelLabel: "crd"}},
				Content: planContent(time.Hour), EvidenceLifetime: metav1.Duration{Duration: time.Hour}}}
		}
		for name, mutate := range map[string]func(*ops.DeviceCommissioningPlan){
			"empty-selector": func(p *ops.DeviceCommissioningPlan) { p.Spec.DeviceSelector = metav1.LabelSelector{} },
			"short-burn-in":  func(p *ops.DeviceCommissioningPlan) { p.Spec.Content.BurnIn.Duration.Duration = time.Second },
			"long-burn-in":   func(p *ops.DeviceCommissioningPlan) { p.Spec.Content.BurnIn.Duration.Duration = 25 * time.Hour },
			"long-lifetime":  func(p *ops.DeviceCommissioningPlan) { p.Spec.EvidenceLifetime.Duration = 366 * 24 * time.Hour },
			"bad-digest":     func(p *ops.DeviceCommissioningPlan) { p.Spec.Content.BurnIn.Image.Digest = "latest" },
		} {
			plan := valid("crd-" + name)
			mutate(plan)
			if err := e.admin.Create(e.ctx, plan); !apierrors.IsInvalid(err) {
				t.Fatalf("%s: create = %v, want Invalid", name, err)
			}
		}
		plan := valid("crd-valid")
		e.create(plan)
		plan.Spec.Supersedes = []string{"crd-old"}
		if err := e.admin.Update(e.ctx, plan); !apierrors.IsInvalid(err) {
			t.Fatalf("plan update = %v, want Invalid", err)
		}
		for _, name := range []string{"crd-valid", "crd-other-0123456789abcdef", "crd-valid-0123456789abcde"} {
			approval := &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: ops.DeviceCommissioningPlanApprovalSpec{
				Creator: ops.Creator{Username: planApprover, UID: "u"}, PlanRef: ops.PlanReference{Name: "crd-valid", Generation: 1, Digest: burnInDigest}}}
			if err := e.admin.Create(e.ctx, approval); !apierrors.IsInvalid(err) {
				t.Fatalf("an approval named %s: %v, want Invalid", name, err)
			}
		}
		long := valid(strings.Repeat("p", 237))
		if err := e.admin.Create(e.ctx, long); !apierrors.IsInvalid(err) {
			t.Fatalf("a plan name with no room for its approval's suffix: %v, want Invalid", err)
		}
	})

	t.Run("a plan its author approved is not qualified", func(t *testing.T) {
		e, device := next(t, "self-approved")
		plan := e.plan("self-approved-v1", "self-approved", planAuthor)
		e.approve(plan, planAuthor)
		operation := e.commission(device, plan, plan.Spec.Content)
		before := e.custody(device.Name).Spec.Fence
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonPlanNotQualified)
		if custody := e.custody(device.Name); custody.Spec.Fence != before || custody.Spec.Holder.Type != ops.HolderNone {
			t.Fatalf("a refusal changed custody: %+v", custody.Spec)
		}
	})

	t.Run("an approver with the author's UID or another case of the username is the author", func(t *testing.T) {
		for _, approver := range []ops.Creator{{Username: "other-name@example.com", UID: planAuthor + "-uid"}, {Username: strings.ToUpper(planAuthor), UID: "different-uid"}} {
			e, device := next(t, "same-person")
			plan := e.plan(fmt.Sprintf("same-person-%d", n), "same-person", planAuthor)
			e.create(&ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: ApprovalName(plan), Namespace: "default"}, Spec: ops.DeviceCommissioningPlanApprovalSpec{
				Creator: approver, PlanRef: e.reference(plan)}})
			operation := e.commission(device, plan, plan.Spec.Content)
			e.settle(device.Name)
			e.requirePhase(operation, ops.PhaseFailed, ReasonPlanNotQualified)
			// Remove the plan so the next iteration's Device has one current plan.
			if err := e.admin.Delete(e.ctx, plan); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("an unapproved plan and a plan approved under another digest are not qualified", func(t *testing.T) {
		e, device := next(t, "unapproved")
		plan := e.plan("unapproved-v1", "unapproved", planAuthor)
		e.requirePhase(func() ops.Operation {
			o := e.commission(device, plan, plan.Spec.Content)
			e.settle(device.Name)
			return o
		}(), ops.PhaseFailed, ReasonPlanNotQualified)
		approval := &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: ApprovalName(plan), Namespace: "default"}, Spec: ops.DeviceCommissioningPlanApprovalSpec{
			Creator: ops.Creator{Username: planApprover, UID: "x"}, PlanRef: ops.PlanReference{Name: plan.Name, Generation: plan.Generation, Digest: burnInDigest}}}
		e.create(approval)
		operation := e.commission(device, plan, plan.Spec.Content)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonPlanNotQualified)
	})

	t.Run("a plan recreated under the same name inherits no approval", func(t *testing.T) {
		e, device := next(t, "recreated")
		plan := e.plan("recreated-v1", "recreated", planAuthor)
		e.approve(plan, planApprover)
		old := ops.ApprovedPlanReference{PlanReference: e.reference(plan), Approval: ApprovalName(plan)}
		if err := e.admin.Delete(e.ctx, plan); err != nil {
			t.Fatal(err)
		}
		again := e.plan("recreated-v1", "recreated", planAuthor)
		if again.UID == plan.UID || e.reference(again) != old.PlanReference {
			t.Fatalf("the recreated plan must differ only in its incarnation: %s %+v", again.UID, e.reference(again))
		}
		if ApprovalName(again) == old.Approval {
			t.Fatal("the recreated plan has the old plan's approval name")
		}
		// The old approval still exists, and qualifies neither the recreated
		// plan nor a commissioning that names it.
		if ok, _, err := Qualified(e.ctx, e.admin, again); err != nil || ok {
			t.Fatalf("the recreated plan is qualified by the old approval: %v %v", ok, err)
		}
		for _, reference := range []ops.ApprovedPlanReference{old, {PlanReference: old.PlanReference, Approval: ApprovalName(again)}} {
			operation := e.commissionUnder(device, reference, again.Spec.Content)
			e.settle(device.Name)
			e.requirePhase(operation, ops.PhaseFailed, ReasonPlanNotQualified)
		}
		// Once the recreated plan is approved, a commissioning that names the
		// old incarnation's approval is still refused, and only one naming the
		// new approval pins the recreated plan.
		e.approve(again, planApprover)
		if ok, why, err := Qualified(e.ctx, e.admin, again); err != nil || !ok {
			t.Fatalf("the re-approved plan is not qualified: %s %v", why, err)
		}
		if pinned, err := Pins(old, again); err != nil || pinned {
			t.Fatalf("the old approval pins the recreated plan: %v %v", pinned, err)
		}
		if pinned, err := Pins(ops.ApprovedPlanReference{PlanReference: old.PlanReference, Approval: ApprovalName(again)}, again); err != nil || !pinned {
			t.Fatalf("the new approval does not pin the recreated plan: %v %v", pinned, err)
		}
		before := e.custody(device.Name).Spec.Fence
		operation := e.commissionUnder(device, old, again.Spec.Content)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonPlanNotQualified)
		if fence := e.custody(device.Name).Spec.Fence; fence != before {
			t.Fatalf("a refused commissioning raised the fence from %d to %d", before, fence)
		}
	})

	t.Run("copied content that differs from the plan is refused", func(t *testing.T) {
		e, device := next(t, "mismatch")
		plan := e.plan("mismatch-v1", "mismatch", planAuthor)
		e.approve(plan, planApprover)
		altered := planContent(time.Minute)
		operation := e.commission(device, plan, altered)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonPlanMismatch)
	})

	t.Run("an inactive Device is refused", func(t *testing.T) {
		e, device := next(t, "inactive")
		plan := e.plan("inactive-v1", "inactive", planAuthor)
		e.approve(plan, planApprover)
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		device.Spec.Lifecycle = inventory.DeviceDecommissioning
		if err := e.admin.Update(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		e.discover(device, e.custodyKey(device), e.clock.Now())
		operation := e.commission(device, plan, plan.Spec.Content)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonDeviceNotActive)
	})

	t.Run("a superseded plan is not current for the Devices the new plan selects", func(t *testing.T) {
		e, device := next(t, "super")
		other := root.sub(t)
		n++
		bystander := other.verified("commission-bystander", uid(800), fmt.Sprintf("%064x", 800))
		other.model(bystander, "super-other")
		old := e.plan("super-v1", "super", planAuthor)
		e.approve(old, planApprover)
		// v1 also selects the bystander's model through a broad selector.
		broad := e.plan("broad-v1", "super-other", planAuthor)
		e.approve(broad, planApprover)
		replacement := e.plan("super-v2", "super", planAuthor, "super-v1", "broad-v1")
		e.approve(replacement, planApprover)
		operation := e.commission(device, old, old.Spec.Content)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonPlanNotCurrent)
		// The supersession reaches only Devices super-v2 selects.
		current, reason, err := CurrentPlan(e.ctx, e.admin, bystander)
		if err != nil || current == nil || current.Name != "broad-v1" {
			t.Fatalf("bystander's current plan = %v, %s, %v (labels %v)", current, reason, err, bystander.Labels)
		}
		// Revoking super-v2 makes super-v1 current again.
		if err := e.admin.Delete(e.ctx, &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: ApprovalName(replacement), Namespace: "default"}}); err != nil {
			t.Fatal(err)
		}
		current, _, err = CurrentPlan(e.ctx, e.admin, device)
		if err != nil || current == nil || current.Name != "super-v1" {
			t.Fatalf("after revocation the current plan = %v, %v", current, err)
		}
	})

	t.Run("two plans that apply leave no current plan", func(t *testing.T) {
		e, device := next(t, "ambiguous")
		for _, name := range []string{"ambiguous-a", "ambiguous-b"} {
			e.approve(e.plan(name, "ambiguous", planAuthor), planApprover)
		}
		current, reason, err := CurrentPlan(e.ctx, e.admin, device)
		if current != nil || reason != ReasonPlanAmbiguous || err != nil {
			t.Fatalf("current = %v, reason %s", current, reason)
		}
		// Mutual supersession is ambiguous too, not a cycle that picks one.
		e.approve(e.plan("cycle-a", "cycle", planAuthor, "cycle-b"), planApprover)
		e.approve(e.plan("cycle-b", "cycle", planAuthor, "cycle-a"), planApprover)
		e.model(device, "cycle")
		if current, reason, err := CurrentPlan(e.ctx, e.admin, device); current != nil || reason != ReasonPlanAmbiguous || err != nil {
			t.Fatalf("a supersession cycle gave %v, %s", current, reason)
		}
	})

	t.Run("a proven verdict succeeds and returns the Device to None", func(t *testing.T) {
		for _, result := range []ops.CommissioningResult{ops.CommissioningPassed, ops.CommissioningFailed} {
			model := "verdict-" + strings.ToLower(string(result))
			e, device := next(t, model)
			plan := e.plan(model+"-v1", model, planAuthor)
			e.approve(plan, planApprover)
			operation := e.commission(device, plan, plan.Spec.Content)
			e.settle(device.Name)
			status := e.requirePhase(operation, ops.PhaseAdmitted, "")
			if got := status.Deadline.Sub(status.AdmittedAt.Time); got != CommissioningOverhead+10*time.Minute {
				t.Fatalf("deadline is %s after admission", got)
			}
			if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderService {
				t.Fatalf("holder = %s, want Service", holder)
			}
			if err := e.result(device.Name, ops.PhaseSucceeded, e.commissioningEvidence(operation, result)); err != nil {
				t.Fatal(err)
			}
			e.settle(device.Name)
			e.requirePhase(operation, ops.PhaseSucceeded, "")
			if custody := e.custody(device.Name); custody.Spec.Holder.Type != ops.HolderNone || custody.Spec.RecoveryHold != nil {
				t.Fatalf("custody after a %s verdict: %+v", result, custody.Spec)
			}
		}
	})

	t.Run("evidence that does not prove its verdict proves nothing", func(t *testing.T) {
		for name, mutate := range map[string]func(*ops.CommissioningEvidence){
			"no hardware":           func(v *ops.CommissioningEvidence) { v.Hardware = nil },
			"no serial":             func(v *ops.CommissioningEvidence) { v.SerialNumber = "" },
			"another serial":        func(v *ops.CommissioningEvidence) { v.SerialNumber = "serial-other" },
			"another key":           func(v *ops.CommissioningEvidence) { v.BMCPublicKeySHA256 = strings.Repeat("0", 64) },
			"too few cores":         func(v *ops.CommissioningEvidence) { v.Hardware.CPU.Cores = 1 },
			"other memory":          func(v *ops.CommissioningEvidence) { v.Hardware.Memory = "4Gi" },
			"small disk":            func(v *ops.CommissioningEvidence) { v.Hardware.Disks[0].Capacity = "1Gi" },
			"missing firmware":      func(v *ops.CommissioningEvidence) { v.Firmware = nil },
			"short burn-in":         func(v *ops.CommissioningEvidence) { v.BurnIn.CompletedAt = v.BurnIn.StartedAt },
			"failed burn-in passed": func(v *ops.CommissioningEvidence) { v.BurnIn.Result = ops.BurnInFailed },
			"another plan":          func(v *ops.CommissioningEvidence) { v.Plan.Digest = burnInDigest },
			"failed without phase":  func(v *ops.CommissioningEvidence) { v.Result = ops.CommissioningFailed; v.Mismatches = []string{"x"} },
			"failed without mismatch": func(v *ops.CommissioningEvidence) {
				v.Result, v.FailedPhase = ops.CommissioningFailed, ops.PhaseInspect
			},
		} {
			model := "proof-" + strings.ReplaceAll(name, " ", "-")
			e, device := next(t, model)
			plan := e.plan(model+"-v1", model, planAuthor)
			e.approve(plan, planApprover)
			operation := e.commission(device, plan, plan.Spec.Content)
			e.settle(device.Name)
			var evidence ops.CommissioningEvidence
			if err := json.Unmarshal([]byte(e.commissioningEvidence(operation, ops.CommissioningPassed)), &evidence); err != nil {
				t.Fatal(err)
			}
			mutate(&evidence)
			data, _ := json.Marshal(evidence)
			if err := e.result(device.Name, ops.PhaseSucceeded, string(data)); err != nil {
				t.Fatal(err)
			}
			e.settle(device.Name)
			if status := e.status(operation); status.Phase != ops.PhaseOutcomeUnknown || status.Reason != ReasonInvalidEvidence {
				t.Fatalf("%s: %s %s (%s)", name, status.Phase, status.Reason, status.Message)
			}
		}
	})

	t.Run("a later commissioning settles a commissioning hold", func(t *testing.T) {
		e, device := next(t, "hold")
		plan := e.plan("hold-v1", "hold", planAuthor)
		e.approve(plan, planApprover)
		first := e.commission(device, plan, plan.Spec.Content)
		e.settle(device.Name)
		e.clock.Advance(CommissioningOverhead + 10*time.Minute + DefaultSkewMargin + time.Second)
		e.discover(device, e.custodyKey(device), e.clock.Now())
		e.settle(device.Name)
		e.requirePhase(first, ops.PhaseOutcomeUnknown, ReasonDeadlineExceeded)
		if custody := e.custody(device.Name); custody.Spec.RecoveryHold == nil || custody.Spec.Holder.Type != ops.HolderService {
			t.Fatalf("custody after the deadline: %+v", custody.Spec)
		}
		// Staff operations expire five minutes after they are created, by
		// the API server's clock, so bring the coordinator's clock back.
		e.clock.Advance(-(CommissioningOverhead + 10*time.Minute + DefaultSkewMargin + time.Second))
		e.discover(device, e.custodyKey(device), e.clock.Now())
		second := e.commission(device, plan, plan.Spec.Content)
		e.settle(device.Name)
		e.requirePhase(second, ops.PhaseAdmitted, "")
		if err := e.result(device.Name, ops.PhaseSucceeded, e.commissioningEvidence(second, ops.CommissioningPassed)); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		if custody := e.custody(device.Name); custody.Spec.RecoveryHold != nil || custody.Spec.Holder.Type != ops.HolderNone {
			t.Fatalf("custody after the settling commissioning: %+v", custody.Spec)
		}
	})

	t.Run("retention keeps each Device's latest succeeded commissioning", func(t *testing.T) {
		e, device := next(t, "retain")
		plan := e.plan("retain-v1", "retain", planAuthor)
		e.approve(plan, planApprover)
		var runs []*ops.DeviceCommissioning
		for range 2 {
			operation := e.commission(device, plan, plan.Spec.Content)
			e.settle(device.Name)
			if err := e.result(device.Name, ops.PhaseSucceeded, e.commissioningEvidence(operation, ops.CommissioningPassed)); err != nil {
				t.Fatal(err)
			}
			e.settle(device.Name)
			runs = append(runs, operation)
			e.clock.Advance(time.Minute)
		}
		e.clock.Advance(DefaultRetention + time.Hour)
		if _, err := e.coord.SweepRetention(e.ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(runs[0]), runs[0]); !apierrors.IsNotFound(err) {
			t.Fatalf("the older commissioning was kept: %v", err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(runs[1]), runs[1]); err != nil {
			t.Fatalf("the latest commissioning was deleted: %v", err)
		}
		// Once the Device is gone, its latest is evidence nobody reads.
		if err := e.admin.Delete(e.ctx, device); err != nil {
			t.Fatal(err)
		}
		if _, err := e.coord.SweepRetention(e.ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(runs[1]), runs[1]); !apierrors.IsNotFound(err) {
			t.Fatalf("a deleted Device's commissioning was kept: %v", err)
		}
	})

	t.Run("the latest success is per Device incarnation", func(t *testing.T) {
		older := &ops.DeviceCommissioning{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{ops.LabelDeviceUID: "first"}}, Spec: ops.DeviceCommissioningSpec{OperationEnvelope: ops.OperationEnvelope{DeviceRef: ops.DeviceReference{Name: "server"}}},
			Status: ops.OperationStatus{Phase: ops.PhaseSucceeded, FinishedAt: &metav1.Time{Time: time.Now()}}}
		newer := older.DeepCopyObject().(*ops.DeviceCommissioning)
		newer.Name, newer.Labels[ops.LabelDeviceUID] = "b", "second"
		newer.Status.FinishedAt = &metav1.Time{Time: time.Now().Add(-time.Hour)}
		latest := LatestSucceeded([]ops.Operation{older, newer})
		if len(latest) != 2 || latest[DeviceIncarnation{Name: "server", UID: "second"}].GetName() != "b" {
			t.Fatalf("latest = %v", latest)
		}
	})
}

// custodyKey is the BMC key discovery last recorded for the Device.
func (e *env) custodyKey(device *inventory.Device) string {
	e.t.Helper()
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
		e.t.Fatal(err)
	}
	return device.Status.Discovery.TLS.PublicKeySHA256
}
