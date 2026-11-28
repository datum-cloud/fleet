package simwork

import (
	"context"
	"encoding/json"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
)

// Commissioning is the simulator's DeviceCommissioning worker. It inspects
// the hardware and firmware the site enrolls for the Device, then records
// one burn-in step in the fenced journal. The simulated burn-in always
// passes. It is never a production executor.
type Commissioning struct{}

var _ site.Kind = Commissioning{}

func commissioningSpec(work *site.Work) (ops.DeviceCommissioningSpec, *custody.Refusal) {
	var spec ops.DeviceCommissioningSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil || spec.Plan.Digest == "" {
		return spec, &custody.Refusal{Reason: custody.ReasonInputsUnreadable, Message: "the admitted inputs are not a DeviceCommissioning spec"}
	}
	return spec, nil
}

func (Commissioning) Check(_ context.Context, work *site.Work) *custody.Refusal {
	if _, refusal := commissioningSpec(work); refusal != nil {
		return refusal
	}
	if connection(work) == nil || connection(work).Target.Hardware == nil {
		return &custody.Refusal{Reason: custody.ReasonTargetUnsupported, Message: "the site enrolls no hardware for the Device"}
	}
	return nil
}

func (Commissioning) Run(ctx context.Context, work *site.Work) (any, error) {
	spec, refusal := commissioningSpec(work)
	if refusal != nil {
		return nil, refusal
	}
	target := connection(work).Target
	inspected := *target.Hardware
	evidence := ops.CommissioningEvidence{Plan: spec.Plan.ApprovedPlanReference, SerialNumber: work.Snapshot.Extra[custody.SnapshotSerialNumber], SystemUUID: target.SystemUUID,
		BMCPublicKeySHA256: target.BMCPublicKeySHA256, Hardware: &inspected, Firmware: slices.Clone(target.Firmware)}
	if phase, mismatches := fixture.Inspect(spec.Plan.Content, target.Hardware, target.Firmware); len(mismatches) > 0 {
		evidence.Result, evidence.FailedPhase, evidence.Mismatches = ops.CommissioningFailed, phase, mismatches
		return evidence, nil
	}
	// The step name marks the evidence as simulated, so a simulated pass is
	// never read as a real burn-in.
	step := site.Step{Name: "simulated-burnin", Send: func(context.Context) error { return nil }, Idempotent: true}
	if err := work.Step(ctx, step); err != nil {
		return nil, err
	}
	at, _ := work.JournaledAt(step.Name)
	evidence.Steps = []ops.StepEvidence{{Name: step.Name, CompletedAt: metav1.NewTime(at.UTC())}}
	// The simulated burn-in takes no time; its evidence records the plan's
	// duration, as a real image would report after running it.
	evidence.BurnIn = &ops.BurnInEvidence{Result: ops.BurnInPassed, StartedAt: metav1.NewTime(at.UTC()), CompletedAt: metav1.NewTime(at.UTC().Add(spec.Plan.Content.BurnIn.Duration.Duration))}
	evidence.Result = ops.CommissioningPassed
	return evidence, nil
}
