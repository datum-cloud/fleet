package redfishwork

import (
	"context"
	"encoding/json"
	"errors"
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
	content := spec.Plan.Content
	if d := content.BurnIn.Duration.Duration; d < time.Minute || d > 24*time.Hour {
		return &custody.Refusal{Reason: ReasonInputs, Message: "the burn-in duration is outside 1 minute to 24 hours"}
	}
	if _, ok := mib(content.Inspection.Memory.Capacity); !ok {
		return &custody.Refusal{Reason: ReasonInputs, Message: "the plan's memory capacity does not parse"}
	}
	if _, ok := mib(content.Inspection.Disks.MinCapacity); !ok {
		return &custody.Refusal{Reason: ReasonInputs, Message: "the plan's minimum disk capacity does not parse"}
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
	fail := func(evidence ops.CommissioningEvidence, phase ops.CommissioningPhase, mismatches []string) (any, error) {
		evidence.Result, evidence.FailedPhase = ops.CommissioningFailed, phase
		evidence.Mismatches = bound(mismatches)
		return fit(evidence), nil
	}

	// Inspection and the firmware check run once, before any effect, and
	// their passing evidence is stored before the first burn-in step. A
	// resumed worker reuses it instead of reading the server again, so
	// nothing it reads after an effect can become an inspection verdict.
	var record preflight
	found, err := work.Store().GetRecord(work.Device, preflightRecords, work.Key(), &record)
	if err != nil {
		return nil, err
	}
	if !found {
		if work.Journaled(burnInPhase.name + "-power-off") {
			return nil, errors.New("burn-in began without a stored inspection")
		}
		evidence, phase, mismatches, err := k.preflight(ctx, work, spec)
		if err != nil {
			return nil, err
		}
		if len(mismatches) > 0 {
			return fail(evidence, phase, mismatches)
		}
		record = preflight{Evidence: evidence}
		if err := work.Store().PutRecord(work.Device, work.Fence, preflightRecords, work.Key(), record); err != nil {
			return nil, err
		}
	}
	evidence := record.Evidence

	// Burn in: one network boot of the plan's image.
	steps, result, err := k.burnIn(ctx, work, plan.BurnIn.Duration.Duration)
	if err != nil {
		return nil, err
	}
	evidence.Steps = steps
	started, _ := work.JournaledAt(burnInPhase.name + "-power-on")
	completed, _ := work.JournaledAt(burnInPhase.name + "-verified")
	evidence.BurnIn = &ops.BurnInEvidence{Result: result, StartedAt: metaNow(started), CompletedAt: metaNow(completed)}
	if result != ops.BurnInPassed {
		return fail(evidence, ops.PhaseBurnIn, []string{"the burn-in image reported a failure"})
	}
	if completed.Sub(started) < plan.BurnIn.Duration.Duration {
		return fail(evidence, ops.PhaseBurnIn, []string{"the burn-in image reported before the plan's duration"})
	}
	after, err := conn(work).BMC.Hardware(ctx, conn(work).System)
	if err != nil {
		return nil, fmt.Errorf("the server could not be read after burn-in: %w", err)
	}
	if !after.Healthy {
		return fail(evidence, ops.PhaseBurnIn, append([]string{"unhealthy after burn-in"}, after.Unhealthy...))
	}
	evidence.Result = ops.CommissioningPassed
	return fit(evidence), nil
}

// preflightRecords holds each operation's passing inspection and firmware
// evidence, stored before burn-in sends anything.
const preflightRecords = "commissioning"

type preflight struct {
	Evidence ops.CommissioningEvidence `json:"evidence"`
}

// preflight inspects the server and checks its firmware with GETs only. It
// returns the failing phase and what failed, if anything did.
func (k Commissioning) preflight(ctx context.Context, work *site.Work, spec ops.DeviceCommissioningSpec) (ops.CommissioningEvidence, ops.CommissioningPhase, []string, error) {
	evidence := ops.CommissioningEvidence{Plan: spec.Plan.PlanReference, BMCPublicKeySHA256: work.Snapshot.BMCPublicKeySHA256}
	hw, err := conn(work).BMC.Hardware(ctx, conn(work).System)
	if err != nil {
		return evidence, "", nil, fmt.Errorf("inspection could not read the server: %w", err)
	}
	evidence.SerialNumber, evidence.SystemUUID = clip(hw.SerialNumber), hw.UUID
	evidence.Hardware = inspected(hw)
	if mismatches := inspect(spec.Plan.Content.Inspection, hw, work.Snapshot); len(mismatches) > 0 {
		return evidence, ops.PhaseInspect, mismatches, nil
	}
	firmware, err := conn(work).BMC.Firmware(ctx)
	if err != nil {
		return evidence, "", nil, fmt.Errorf("the firmware inventory could not be read: %w", err)
	}
	reported, mismatches := checkFirmware(spec.Plan.Content.Firmware, firmware)
	evidence.Firmware = reported
	if len(mismatches) > 0 {
		return evidence, ops.PhaseFirmware, mismatches, nil
	}
	return evidence, "", nil, nil
}

// burnIn boots the burn-in image once and reads its one result receipt.
func (k Commissioning) burnIn(ctx context.Context, work *site.Work, duration time.Duration) ([]ops.StepEvidence, ops.BurnInResult, error) {
	boot, err := k.boot(ctx, work, burnInPhase.mode)
	if err != nil {
		return nil, "", err
	}
	if boot.DurationSeconds == 0 {
		boot.DurationSeconds = int64(duration / time.Second)
		if err := k.saveBoot(work, boot); err != nil {
			return nil, "", err
		}
	}
	steps, err := k.run(ctx, work, []phase{burnInPhase})
	if err != nil {
		return nil, "", err
	}
	result, err := k.burnInResult(work, boot)
	if err != nil {
		return nil, "", err
	}
	return steps, result, nil
}

// burnInResult reads the result of the operation's one burn-in receipt.
// More than one receipt, of either result, proves nothing.
func (c Config) burnInResult(work *site.Work, boot Boot) (ops.BurnInResult, error) {
	if n, err := c.receipts(work, boot, burnInPhase); err != nil {
		return "", err
	} else if n != 1 {
		return "", fmt.Errorf("found %d burn-in receipts for this operation, want one", n)
	}
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
	return "", errors.New("no burn-in result for this operation")
}

// maxEvidence is the kind's evidence limit, less room for the envelope.
const maxEvidence = 15 << 10

// clip bounds a string the BMC supplied.
func clip(value string) string {
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

// fit keeps the evidence within the kind's limit. It drops the inspected
// component lists before it would drop the verdict.
func fit(evidence ops.CommissioningEvidence) ops.CommissioningEvidence {
	if data, err := json.Marshal(evidence); err == nil && len(data) <= maxEvidence {
		return evidence
	}
	if evidence.Hardware != nil {
		hw := *evidence.Hardware
		hw.Disks, hw.Interfaces = nil, nil
		evidence.Hardware = &hw
	}
	if data, err := json.Marshal(evidence); err == nil && len(data) <= maxEvidence {
		return evidence
	}
	evidence.Mismatches = evidence.Mismatches[:min(len(evidence.Mismatches), 4)]
	evidence.Firmware = nil
	return evidence
}

func bound(list []string) []string {
	if len(list) > 32 {
		list = list[:32]
	}
	for i, value := range list {
		list[i] = clip(value)
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
	minimum, ok := mib(plan.Disks.MinCapacity)
	if !ok {
		out = append(out, "the plan's minimum disk capacity does not parse")
	}
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
			out.CPU.Architecture, out.CPU.Model = clip(cpu.Architecture), clip(cpu.Model)
		}
	}
	for _, disk := range hw.Disks {
		if len(out.Disks) < 64 {
			out.Disks = append(out.Disks, ops.InspectedDisk{Name: clip(disk.Name), Capacity: quantity(disk.CapacityBytes)})
		}
	}
	for _, nic := range hw.Interfaces {
		if len(out.Interfaces) < 64 {
			iface := ops.InspectedInterface{Name: clip(nic.ID), MACAddress: nic.MACAddress}
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
				reported = append(reported, ops.FirmwareBaseline{Component: want.Component, Version: clip(item.Version)})
				if item.Version != want.Version {
					mismatches = append(mismatches, fmt.Sprintf("%s is %q, want %q", want.Component, clip(item.Version), want.Version))
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
