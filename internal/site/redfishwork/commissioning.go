package redfishwork

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var burnInPhase = phase{"burnin", "Pxe", "FLEET-BURNIN:", "burnin"}

// Commissioning is the DeviceCommissioning worker. It inspects the server and
// checks its firmware with GETs only, then runs the plan's burn-in image
// with one network boot. A proven verdict, pass or fail, is a success; the
// evidence carries the result.
type Commissioning struct {
	Config
	// BurnInDigest is the digest of the boot image the site serves. A plan
	// whose burn-in image has another digest is refused before anything is
	// sent.
	BurnInDigest string
}

var _ site.Kind = Commissioning{}

func (k Commissioning) Check(_ context.Context, work *site.Work) *custody.Refusal {
	if conn(work) == nil {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "this site does not reach the server over Redfish"}
	}
	spec, err := commissioningSpec(work)
	if err != nil {
		return &custody.Refusal{Reason: ReasonInputs, Message: "the admitted inputs are not a DeviceCommissioning spec"}
	}
	if k.BurnInDigest == "" || spec.Plan.Content.BurnIn.Image.Digest != k.BurnInDigest {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the site does not serve the plan's burn-in image"}
	}
	if conn(work).Target.Console == "" {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the site's enrollment names no console for receipts"}
	}
	if !contains(conn(work).System.BootTargets, "Pxe") || !conn(work).System.OffersOnce() {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the system does not offer a one-time network boot"}
	}
	for _, need := range []string{"On", "ForceOff"} {
		if !contains(conn(work).System.ResetTypes, need) {
			return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the system does not offer reset " + need}
		}
	}
	return nil
}

func commissioningSpec(work *site.Work) (ops.DeviceCommissioningSpec, error) {
	var spec ops.DeviceCommissioningSpec
	err := json.Unmarshal(work.Inputs, &spec)
	return spec, err
}

func (k Commissioning) Run(ctx context.Context, work *site.Work) (any, error) {
	spec, err := commissioningSpec(work)
	if err != nil {
		return nil, &custody.Refusal{Reason: ReasonInputs, Message: "the admitted inputs are not a DeviceCommissioning spec"}
	}
	plan := spec.Plan.Content
	evidence := ops.CommissioningEvidence{Plan: spec.Plan.PlanReference, BMCPublicKeySHA256: work.Snapshot.BMCPublicKeySHA256}
	fail := func(phase ops.CommissioningPhase, mismatches []string) (any, error) {
		evidence.Result, evidence.FailedPhase = ops.CommissioningFailed, phase
		evidence.Mismatches = bound(mismatches)
		return evidence, nil
	}

	// Inspect. GETs only.
	hw, err := conn(work).BMC.Hardware(ctx, conn(work).System)
	if err != nil {
		return nil, fmt.Errorf("inspection could not read the server: %w", err)
	}
	evidence.SerialNumber, evidence.SystemUUID = hw.SerialNumber, hw.UUID
	evidence.Hardware = inspected(hw)
	if mismatches := inspect(plan.Inspection, hw, work.Snapshot); len(mismatches) > 0 {
		return fail(ops.PhaseInspect, mismatches)
	}

	// Check firmware. GETs only.
	firmware, err := conn(work).BMC.Firmware(ctx)
	if err != nil {
		return nil, fmt.Errorf("the firmware inventory could not be read: %w", err)
	}
	reported, mismatches := checkFirmware(plan.Firmware, firmware)
	evidence.Firmware = reported
	if len(mismatches) > 0 {
		return fail(ops.PhaseFirmware, mismatches)
	}

	// Burn in: one network boot of the plan's image.
	steps, startedAt, result, err := k.burnIn(ctx, work, plan.BurnIn.Duration.Duration)
	if err != nil {
		return nil, err
	}
	evidence.Steps = steps
	evidence.BurnIn = &ops.BurnInEvidence{Result: result, StartedAt: metaNow(startedAt), CompletedAt: metaNow(k.now())}
	if result != ops.BurnInPassed {
		return fail(ops.PhaseBurnIn, []string{"the burn-in image reported a failure"})
	}
	after, err := conn(work).BMC.Hardware(ctx, conn(work).System)
	if err != nil {
		return nil, fmt.Errorf("the server could not be read after burn-in: %w", err)
	}
	if !after.Healthy {
		return fail(ops.PhaseBurnIn, append([]string{"unhealthy after burn-in"}, after.Unhealthy...))
	}
	evidence.Result = ops.CommissioningPassed
	return evidence, nil
}

// burnIn boots the burn-in image once and reads its one result receipt.
func (k Commissioning) burnIn(ctx context.Context, work *site.Work, duration time.Duration) ([]ops.StepEvidence, time.Time, ops.BurnInResult, error) {
	boot, err := k.boot(ctx, work, burnInPhase.mode)
	if err != nil {
		return nil, time.Time{}, "", err
	}
	if boot.DurationSeconds == 0 {
		boot.DurationSeconds = int64(duration / time.Second)
		if err := k.saveBoot(work, boot); err != nil {
			return nil, time.Time{}, "", err
		}
	}
	steps, err := k.run(ctx, work, []phase{burnInPhase})
	if err != nil {
		return nil, time.Time{}, "", err
	}
	result, err := k.burnInResult(work, boot)
	if err != nil {
		return nil, time.Time{}, "", err
	}
	return steps, boot.Created, result, nil
}

// burnInResult reads the one burn-in receipt's result.
func (c Config) burnInResult(work *site.Work, boot Boot) (ops.BurnInResult, error) {
	data, err := os.ReadFile(filepath.Join(c.ConsoleRoot, filepath.Base(conn(work).Target.Console)))
	if err != nil {
		return "", err
	}
	prefix := burnInPhase.marker + boot.Receipt + ":"
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case prefix + "passed":
			return ops.BurnInPassed, nil
		case prefix + "failed":
			return ops.BurnInFailed, nil
		}
	}
	return "", fmt.Errorf("no burn-in result for this operation")
}

func bound(list []string) []string {
	if len(list) > 32 {
		list = list[:32]
	}
	for i, value := range list {
		if len(value) > 256 {
			list[i] = value[:256]
		}
	}
	return list
}

func mib(capacity string) (int64, bool) {
	q, err := resource.ParseQuantity(capacity)
	if err != nil {
		return 0, false
	}
	return q.Value() / (1 << 20), true
}

func quantity(bytes int64) string {
	return resource.NewQuantity(bytes, resource.BinarySI).String()
}

// inspect compares what the BMC reported with the plan and the Device's
// recorded identity. CPU and memory must match exactly; disks and interfaces
// are minimums.
func inspect(plan ops.CommissioningInspection, hw redfish.Hardware, snapshot ops.DeviceSnapshot) []string {
	var out []string
	if serial := snapshot.Extra[custody.SnapshotSerialNumber]; serial == "" || !strings.EqualFold(strings.TrimSpace(serial), hw.SerialNumber) {
		out = append(out, "the BMC reports another serial number than the Device records")
	}
	if !strings.EqualFold(hw.UUID, snapshot.SystemUUID) {
		out = append(out, "the BMC reports another system UUID than the Device records")
	}
	if !hw.Healthy {
		out = append(out, "unhealthy: "+strings.Join(hw.Unhealthy, ", "))
	}
	var cores int64
	for _, cpu := range hw.Processors {
		cores += cpu.Cores
		if cpu.Architecture != plan.CPU.Architecture {
			out = append(out, fmt.Sprintf("processor %s is %q, want %s", cpu.ID, cpu.Architecture, plan.CPU.Architecture))
		}
	}
	if cores != plan.CPU.Cores {
		out = append(out, fmt.Sprintf("%d cores, want %d", cores, plan.CPU.Cores))
	}
	if want, ok := mib(plan.Memory.Capacity); !ok || hw.MemoryMiB != want {
		out = append(out, fmt.Sprintf("%d MiB of memory, want %s", hw.MemoryMiB, plan.Memory.Capacity))
	}
	minimum, _ := mib(plan.Disks.MinCapacity)
	var disks int64
	for _, disk := range hw.Disks {
		if disk.CapacityBytes/(1<<20) >= minimum {
			disks++
		}
	}
	if disks < plan.Disks.Count {
		out = append(out, fmt.Sprintf("%d disks of at least %s, want %d", disks, plan.Disks.MinCapacity, plan.Disks.Count))
	}
	if int64(len(hw.Interfaces)) < plan.Interfaces.Count {
		out = append(out, fmt.Sprintf("%d network interfaces, want %d", len(hw.Interfaces), plan.Interfaces.Count))
	}
	return out
}

func inspected(hw redfish.Hardware) *ops.InspectedHardware {
	out := &ops.InspectedHardware{Memory: quantity(hw.MemoryMiB << 20)}
	for _, cpu := range hw.Processors {
		out.CPU.Sockets++
		out.CPU.Cores += cpu.Cores
		if out.CPU.Architecture == "" {
			out.CPU.Architecture, out.CPU.Model = cpu.Architecture, cpu.Model
		}
	}
	for _, disk := range hw.Disks {
		if len(out.Disks) < 64 {
			out.Disks = append(out.Disks, ops.InspectedDisk{Name: disk.Name, Capacity: quantity(disk.CapacityBytes)})
		}
	}
	for _, nic := range hw.Interfaces {
		if len(out.Interfaces) < 64 {
			iface := ops.InspectedInterface{Name: nic.ID, MACAddress: nic.MACAddress}
			if nic.SpeedMbps > 0 {
				iface.Speed = strconv.FormatInt(nic.SpeedMbps, 10) + "Mbps"
			}
			out.Interfaces = append(out.Interfaces, iface)
		}
	}
	return out
}

// checkFirmware returns every baseline component as the BMC reported it, and
// what does not match. A component matches by Id or by Name.
func checkFirmware(baseline []ops.FirmwareBaseline, inventory []redfish.Firmware) ([]ops.FirmwareBaseline, []string) {
	var reported []ops.FirmwareBaseline
	var mismatches []string
	for _, want := range baseline {
		found := false
		for _, item := range inventory {
			if item.ID == want.Component || item.Name == want.Component {
				found = true
				reported = append(reported, ops.FirmwareBaseline{Component: want.Component, Version: item.Version})
				if item.Version != want.Version {
					mismatches = append(mismatches, fmt.Sprintf("%s is %q, want %q", want.Component, item.Version, want.Version))
				}
				break
			}
		}
		if !found {
			mismatches = append(mismatches, want.Component+" is not reported")
		}
	}
	return reported, mismatches
}

var _ = metav1.Now
