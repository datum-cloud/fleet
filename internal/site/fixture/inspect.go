package fixture

import (
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// Inspect compares a target's enrolled hardware and firmware with the plan,
// as the Redfish worker compares what the BMC reports. It returns the phase
// that failed and what failed, or no phase when the target meets the plan.
// A synthetic Device has no BMC, so its enrollment is what a commissioning
// inspects.
func Inspect(plan ops.CommissioningPlanContent, hw *ops.InspectedHardware, firmware []ops.FirmwareBaseline) (ops.CommissioningPhase, []string) {
	if hw == nil {
		return ops.PhaseInspect, []string{"the site enrolls no hardware for the Device"}
	}
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
		return ops.PhaseInspect, out
	}
	for _, baseline := range plan.Firmware {
		if !slices.Contains(firmware, baseline) {
			out = append(out, baseline.Component+" does not report version "+baseline.Version)
		}
	}
	if len(out) > 0 {
		return ops.PhaseFirmware, out
	}
	return "", nil
}
