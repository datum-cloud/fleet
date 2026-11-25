package redfishwork

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var labBurnIn = "sha256:" + strings.Repeat("d", 64)

func commissioningRunner(t *testing.T, l *lab) *site.Runner {
	t.Helper()
	r := runner(t, l)
	config := r.Kinds["DeviceWipe"].(Wipe).Config
	r.Kinds["DeviceCommissioning"] = Commissioning{Config: config, BurnInDigest: labBurnIn}
	return r
}

func planSpec(t *testing.T, mutate func(*ops.CommissioningPlanContent)) ops.DeviceCommissioningSpec {
	t.Helper()
	content := ops.CommissioningPlanContent{
		Inspection: ops.CommissioningInspection{CPU: ops.InspectionCPU{Architecture: "amd64", Cores: 2}, Memory: ops.InspectionValue{Capacity: "2Gi"},
			Disks: ops.InspectionDisks{Count: 1, MinCapacity: "32Gi"}, Interfaces: ops.InspectionCount{Count: 1}},
		Firmware: []ops.FirmwareBaseline{{Component: "BMC", Version: "1.0.0"}},
		BurnIn:   ops.CommissioningBurnIn{Image: ops.ImageInput{Name: "burn-in", Digest: labBurnIn}, Duration: metav1.Duration{Duration: time.Minute}},
	}
	if mutate != nil {
		mutate(&content)
	}
	return ops.DeviceCommissioningSpec{Plan: ops.CommissioningPlanInput{PlanReference: ops.PlanReference{Name: "plan-example", UID: "plan-uid", Digest: "sha256:" + strings.Repeat("e", 64)}, Content: content}}
}

func commission(t *testing.T, l *lab, r *site.Runner, uid string, spec ops.DeviceCommissioningSpec) (custody.SiteResult, ops.CommissioningEvidence) {
	t.Helper()
	work := assignment(l, "DeviceCommissioning", uid, 1, spec)
	work.Snapshot.Extra = map[string]string{custody.SnapshotSerialNumber: "serial-001"}
	result, err := r.Run(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	var evidence ops.CommissioningEvidence
	if result.Phase == ops.PhaseSucceeded {
		if err := json.Unmarshal(result.Evidence, &evidence); err != nil {
			t.Fatal(err)
		}
		// The evidence passes the kind's own validation.
		operation := &ops.DeviceCommissioning{Spec: spec}
		if _, err := custody.DeviceCommissioningKind().Evidence(operation, result.Evidence); err != nil {
			t.Fatalf("the kind rejects the worker's evidence: %v", err)
		}
	}
	return result, evidence
}

func TestCommissioningPassesAServerThatMeetsThePlan(t *testing.T) {
	l := newLab(t)
	r := commissioningRunner(t, l)
	result, evidence := commission(t, l, r, "op-commission", planSpec(t, nil))
	if result.Phase != ops.PhaseSucceeded || evidence.Result != ops.CommissioningPassed || l.onCount() != 1 {
		t.Fatalf("result = %+v, evidence %+v, %d power-ons", result, evidence, l.onCount())
	}
	hardware := evidence.Hardware
	if hardware.CPU.Architecture != "amd64" || hardware.CPU.Cores != 2 || hardware.Memory != "2Gi" || len(hardware.Disks) != 1 || hardware.Disks[0].Capacity != "64Gi" || len(hardware.Interfaces) != 1 {
		t.Fatalf("hardware = %+v", hardware)
	}
	if evidence.SerialNumber != "SERIAL-001" || evidence.BurnIn.Result != ops.BurnInPassed || len(evidence.Firmware) != 1 || evidence.Firmware[0].Version != "1.0.0" {
		t.Fatalf("evidence = %+v", evidence)
	}
	if _, open, _ := OpenAdmission(r.Store, mac, time.Now()); open {
		t.Fatal("the burn-in admission stayed open")
	}
}

// A mismatch found by inspection or the firmware check is a proven verdict.
// It sends nothing to the server.
func TestCommissioningFailsAMismatchWithoutSendingAnything(t *testing.T) {
	for name, setup := range map[string]struct {
		lab    func(*lab)
		plan   func(*ops.CommissioningPlanContent)
		phase  ops.CommissioningPhase
		expect string
	}{
		"cores":     {plan: func(p *ops.CommissioningPlanContent) { p.Inspection.CPU.Cores = 4 }, phase: ops.PhaseInspect, expect: "2 cores, want 4"},
		"memory":    {plan: func(p *ops.CommissioningPlanContent) { p.Inspection.Memory.Capacity = "4Gi" }, phase: ops.PhaseInspect, expect: "MiB of memory"},
		"disks":     {plan: func(p *ops.CommissioningPlanContent) { p.Inspection.Disks.Count = 2 }, phase: ops.PhaseInspect, expect: "disks of at least"},
		"unhealthy": {lab: func(l *lab) { l.unhealthy = "DIMM0" }, phase: ops.PhaseInspect, expect: "Memory DIMM0"},
		"serial":    {lab: func(l *lab) { l.serial = "OTHER" }, phase: ops.PhaseInspect, expect: "serial number"},
		"firmware":  {lab: func(l *lab) { l.bmcFW = "0.9.0" }, phase: ops.PhaseFirmware, expect: `BMC is "0.9.0"`},
		"missing":   {plan: func(p *ops.CommissioningPlanContent) { p.Firmware = append(p.Firmware, ops.FirmwareBaseline{Component: "BIOS", Version: "1"}) }, phase: ops.PhaseFirmware, expect: "BIOS is not reported"},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLab(t)
			if setup.lab != nil {
				setup.lab(l)
			}
			r := commissioningRunner(t, l)
			result, evidence := commission(t, l, r, "op-"+name, planSpec(t, setup.plan))
			if result.Phase != ops.PhaseSucceeded || evidence.Result != ops.CommissioningFailed || evidence.FailedPhase != setup.phase || !strings.Contains(strings.Join(evidence.Mismatches, ";"), setup.expect) {
				t.Fatalf("result = %+v, evidence %+v", result, evidence)
			}
			if l.effects != 0 {
				t.Fatalf("%d effectful requests reached the BMC", l.effects)
			}
		})
	}
}

func TestABurnInFailureIsAProvenVerdictAndLeavesTheServerOff(t *testing.T) {
	l := newLab(t)
	l.burnIn = "failed"
	r := commissioningRunner(t, l)
	result, evidence := commission(t, l, r, "op-burn-fail", planSpec(t, nil))
	if result.Phase != ops.PhaseSucceeded || evidence.Result != ops.CommissioningFailed || evidence.FailedPhase != ops.PhaseBurnIn || evidence.BurnIn.Result != ops.BurnInFailed {
		t.Fatalf("result = %+v, evidence %+v", result, evidence)
	}
	if l.power != "Off" || l.onCount() != 1 {
		t.Fatalf("power %s after %d power-ons", l.power, l.onCount())
	}
}

// The site serves one burn-in image. A plan that names another is refused
// before anything is sent.
func TestCommissioningRefusesABurnInImageTheSiteDoesNotServe(t *testing.T) {
	l := newLab(t)
	r := commissioningRunner(t, l)
	spec := planSpec(t, func(p *ops.CommissioningPlanContent) { p.BurnIn.Image.Digest = "sha256:" + strings.Repeat("f", 64) })
	result, _ := commission(t, l, r, "op-other-image", spec)
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonUnsupportedTarget || l.effects != 0 {
		t.Fatalf("result = %+v, %d effects", result, l.effects)
	}
}

func TestTheBootServerPassesTheBurnInDuration(t *testing.T) {
	l := newLab(t)
	r := commissioningRunner(t, l)
	commission(t, l, r, "op-duration", planSpec(t, nil))
	var boot Boot
	if _, err := r.Store.GetRecord(device, "boot", l.operation, &boot); err != nil || boot.Mode != "burnin" || boot.DurationSeconds != 60 {
		t.Fatalf("boot = %+v, %v", boot, err)
	}
}
