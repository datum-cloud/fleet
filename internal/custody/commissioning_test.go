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
	digest, err := PlanDigest(plan)
	if err != nil {
		e.t.Fatal(err)
	}
	approval := &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: plan.Name, Namespace: "default"}, Spec: ops.DeviceCommissioningPlanApprovalSpec{
		Creator: ops.Creator{Username: approver, UID: approver + "-uid"},
		PlanRef: ops.PlanReference{Name: plan.Name, UID: string(plan.UID), Digest: digest},
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

func (e *env) commission(device *inventory.Device, plan *ops.DeviceCommissioningPlan, content ops.CommissioningPlanContent) *ops.DeviceCommissioning {
	e.t.Helper()
	digest, err := PlanDigest(plan)
	if err != nil {
		e.t.Fatal(err)
	}
	operation := &ops.DeviceCommissioning{ObjectMeta: metav1.ObjectMeta{GenerateName: device.Name + "-commission-", Namespace: "default"}, Spec: ops.DeviceCommissioningSpec{
		OperationEnvelope: envelope(device, staffUser, service("intake")),
		Plan:              ops.CommissioningPlanInput{PlanReference: ops.PlanReference{Name: plan.Name, UID: string(plan.UID), Digest: digest}, Content: content},
	}}
	e.create(operation)
	return operation
}

func commissioningEvidence(t *testing.T, operation *ops.DeviceCommissioning, result ops.CommissioningResult) string {
	t.Helper()
	evidence := ops.CommissioningEvidence{Plan: operation.Spec.Plan.PlanReference, Result: result, SerialNumber: "serial", SystemUUID: "uuid",
		Hardware: &ops.InspectedHardware{CPU: ops.InspectedCPU{Architecture: "amd64", Cores: 2}, Memory: "2Gi"},
		BurnIn:   &ops.BurnInEvidence{Result: ops.BurnInPassed}}
	if result == ops.CommissioningFailed {
		evidence.FailedPhase, evidence.Mismatches = ops.PhaseBurnIn, []string{"memory test failed"}
		evidence.BurnIn.Result = ops.BurnInFailed
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCommissioning(t *testing.T) {
	root := startEnv(t)
	n := 0
	next := func(t *testing.T, model string) (*env, *inventory.Device) {
		e := root.sub(t)
		n++
		name := "commission-" + string(rune('a'+n))
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
		approval := &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: "crd-other-name", Namespace: "default"}, Spec: ops.DeviceCommissioningPlanApprovalSpec{
			Creator: ops.Creator{Username: planApprover, UID: "u"}, PlanRef: ops.PlanReference{Name: "crd-valid", UID: "u", Digest: burnInDigest}}}
		if err := e.admin.Create(e.ctx, approval); !apierrors.IsInvalid(err) {
			t.Fatalf("an approval named for another plan: %v, want Invalid", err)
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

	t.Run("an unapproved plan and a plan approved under another digest are not qualified", func(t *testing.T) {
		e, device := next(t, "unapproved")
		plan := e.plan("unapproved-v1", "unapproved", planAuthor)
		e.requirePhase(func() ops.Operation {
			o := e.commission(device, plan, plan.Spec.Content)
			e.settle(device.Name)
			return o
		}(), ops.PhaseFailed, ReasonPlanNotQualified)
		approval := &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: plan.Name, Namespace: "default"}, Spec: ops.DeviceCommissioningPlanApprovalSpec{
			Creator: ops.Creator{Username: planApprover, UID: "x"}, PlanRef: ops.PlanReference{Name: plan.Name, UID: string(plan.UID), Digest: burnInDigest}}}
		e.create(approval)
		operation := e.commission(device, plan, plan.Spec.Content)
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseFailed, ReasonPlanNotQualified)
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
		if err := e.admin.Delete(e.ctx, &ops.DeviceCommissioningPlanApproval{ObjectMeta: metav1.ObjectMeta{Name: "super-v2", Namespace: "default"}}); err != nil {
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
		current, reason, _ := CurrentPlan(e.ctx, e.admin, device)
		if current != nil || reason != ReasonPlanAmbiguous {
			t.Fatalf("current = %v, reason %s", current, reason)
		}
		// Mutual supersession is ambiguous too, not a cycle that picks one.
		e.approve(e.plan("cycle-a", "cycle", planAuthor, "cycle-b"), planApprover)
		e.approve(e.plan("cycle-b", "cycle", planAuthor, "cycle-a"), planApprover)
		e.model(device, "cycle")
		if current, reason, _ := CurrentPlan(e.ctx, e.admin, device); current != nil || reason != ReasonPlanAmbiguous {
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
			if err := e.result(device.Name, ops.PhaseSucceeded, commissioningEvidence(t, operation, result)); err != nil {
				t.Fatal(err)
			}
			e.settle(device.Name)
			e.requirePhase(operation, ops.PhaseSucceeded, "")
			if custody := e.custody(device.Name); custody.Spec.Holder.Type != ops.HolderNone || custody.Spec.RecoveryHold != nil {
				t.Fatalf("custody after a %s verdict: %+v", result, custody.Spec)
			}
		}
	})

	t.Run("a passed verdict without hardware proves nothing", func(t *testing.T) {
		e, device := next(t, "proof")
		plan := e.plan("proof-v1", "proof", planAuthor)
		e.approve(plan, planApprover)
		operation := e.commission(device, plan, plan.Spec.Content)
		e.settle(device.Name)
		data, _ := json.Marshal(ops.CommissioningEvidence{Plan: operation.Spec.Plan.PlanReference, Result: ops.CommissioningPassed})
		if err := e.result(device.Name, ops.PhaseSucceeded, string(data)); err != nil {
			t.Fatal(err)
		}
		e.settle(device.Name)
		e.requirePhase(operation, ops.PhaseOutcomeUnknown, ReasonInvalidEvidence)
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
		if err := e.result(device.Name, ops.PhaseSucceeded, commissioningEvidence(t, second, ops.CommissioningPassed)); err != nil {
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
			if err := e.result(device.Name, ops.PhaseSucceeded, commissioningEvidence(t, operation, ops.CommissioningPassed)); err != nil {
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
