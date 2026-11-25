package simwork

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
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
	if mismatches := inspect(spec.Plan.Content, target.Hardware, target.Firmware); len(mismatches) > 0 {
		evidence.Result, evidence.Mismatches = ops.CommissioningFailed, mismatches
		evidence.FailedPhase = ops.PhaseInspect
		if mismatches[0] == firmwareMismatch {
			evidence.FailedPhase, evidence.Mismatches = ops.PhaseFirmware, mismatches[1:]
		}
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

const firmwareMismatch = "firmware"

// inspect compares the enrolled hardware and firmware with the plan, as the
// Redfish worker compares what the BMC reports.
func inspect(plan ops.CommissioningPlanContent, hw *ops.InspectedHardware, firmware []ops.FirmwareBaseline) []string {
	var out []string
	want := plan.Inspection
	if hw.CPU.Architecture != want.CPU.Architecture || hw.CPU.Cores != want.CPU.Cores {
		out = append(out, fmt.Sprintf("%s with %d cores, want %s with %d", hw.CPU.Architecture, hw.CPU.Cores, want.CPU.Architecture, want.CPU.Cores))
	}
	if have, err := resource.ParseQuantity(hw.Memory); err != nil || have.Cmp(resource.MustParse(want.Memory.Capacity)) != 0 {
		out = append(out, fmt.Sprintf("%s of memory, want %s", hw.Memory, want.Memory.Capacity))
	}
	minimum := resource.MustParse(want.Disks.MinCapacity)
	var disks int64
	for _, disk := range hw.Disks {
		if have, err := resource.ParseQuantity(disk.Capacity); err == nil && have.Cmp(minimum) >= 0 {
			disks++
		}
	}
	if disks < want.Disks.Count {
		out = append(out, fmt.Sprintf("%d disks of at least %s, want %d", disks, want.Disks.MinCapacity, want.Disks.Count))
	}
	if int64(len(hw.Interfaces)) < want.Interfaces.Count {
		out = append(out, fmt.Sprintf("%d network interfaces, want %d", len(hw.Interfaces), want.Interfaces.Count))
	}
	if len(out) > 0 {
		return out
	}
	for _, baseline := range plan.Firmware {
		if !slices.Contains(firmware, baseline) {
			out = append(out, baseline.Component+" does not report version "+baseline.Version)
		}
	}
	if len(out) > 0 {
		return append([]string{firmwareMismatch}, out...)
	}
	return nil
}
