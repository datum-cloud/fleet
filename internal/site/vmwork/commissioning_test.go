package vmwork

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
)

// localHardware is what the site enrolls for the local guest.
var localHardware = &ops.InspectedHardware{CPU: ops.InspectedCPU{Architecture: "amd64", Cores: 2}, Memory: "2Gi",
	Disks: []ops.InspectedDisk{{Name: "disk-0", Capacity: "2Gi"}}, Interfaces: []ops.InspectedInterface{{Name: "primary", MACAddress: "02:00:00:00:00:01"}}}

// commissioningRig is a rig whose site store clock advances a minute on each
// read, so a burn-in the fake guest finishes at once spans the plan's
// duration in the journal. tick false keeps the real clock.
func commissioningRig(t *testing.T, tick bool) *rig {
	r := newRig(t)
	if tick {
		var mu sync.Mutex
		now := time.Now().UTC()
		store, err := site.OpenStore(filepath.Join(filepath.Dir(r.config.Disks), "store"), func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(time.Minute)
			return now
		})
		if err != nil {
			t.Fatal(err)
		}
		r.store, r.runner.Store, r.runner.Results = store, store, site.NewResults(store)
	}
	access := r.runner.Access.(fixture.Access)
	target := access.Enrollment[device]
	target.Hardware = localHardware
	access.Enrollment[device] = target
	r.runner.Kinds["DeviceCommissioning"] = Commissioning{r.config}
	return r
}

func commissioningSpec(mutate func(*ops.CommissioningPlanContent)) ops.DeviceCommissioningSpec {
	name, digest, _ := strings.Cut(image, "@")
	content := ops.CommissioningPlanContent{
		Inspection: ops.CommissioningInspection{CPU: ops.InspectionCPU{Architecture: "amd64", Cores: 2}, Memory: ops.InspectionValue{Capacity: "2Gi"},
			Disks: ops.InspectionDisks{Count: 1, MinCapacity: "1Gi"}, Interfaces: ops.InspectionCount{Count: 1}},
		BurnIn: ops.CommissioningBurnIn{Image: ops.ImageInput{Name: name, Digest: digest}, Duration: metav1.Duration{Duration: time.Minute}},
	}
	if mutate != nil {
		mutate(&content)
	}
	envelope := envelope()
	envelope.Authority = ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: "intake"}}
	return ops.DeviceCommissioningSpec{OperationEnvelope: envelope, Plan: ops.CommissioningPlanInput{
		ApprovedPlanReference: ops.ApprovedPlanReference{PlanReference: ops.PlanReference{Name: "local-plan", Generation: 1, Digest: "sha256:" + key}, Approval: "local-plan-approval"},
		Content:               content}}
}

func (r *rig) commission(uid string, spec ops.DeviceCommissioningSpec) (custody.SiteResult, ops.CommissioningEvidence) {
	r.t.Helper()
	work := r.assignment("DeviceCommissioning", uid, 2, spec)
	work.Snapshot.Extra = map[string]string{custody.SnapshotSerialNumber: "serial-001"}
	result, err := r.runner.Run(context.Background(), work)
	if err != nil {
		r.t.Fatal(err)
	}
	var evidence ops.CommissioningEvidence
	if result.Phase == ops.PhaseSucceeded {
		if err := json.Unmarshal(result.Evidence, &evidence); err != nil {
			r.t.Fatal(err)
		}
		// The evidence passes the kind's own validation.
		operation := &ops.DeviceCommissioning{Spec: spec, Status: ops.OperationStatus{Snapshot: &work.Snapshot}}
		if _, err := custody.DeviceCommissioningKind().Evidence(operation, result.Evidence); err != nil {
			r.t.Fatalf("the kind rejects the worker's evidence: %v", err)
		}
	}
	return result, evidence
}

// TestCommissioningBurnsInOnceAndLeavesNoDisk inspects the enrolled guest,
// boots the site image once in burn-in mode, and passes on its receipt.
func TestCommissioningBurnsInOnceAndLeavesNoDisk(t *testing.T) {
	r := commissioningRig(t, true)
	result, evidence := r.commission("00000000-0000-4000-8000-000000000301", commissioningSpec(nil))
	if result.Phase != ops.PhaseSucceeded || evidence.Result != ops.CommissioningPassed || evidence.BurnIn == nil || evidence.BurnIn.Result != ops.BurnInPassed {
		t.Fatalf("result = %+v, evidence %+v", result, evidence)
	}
	if r.vm.creates != 1 {
		t.Fatalf("boots = %d, want one burn-in boot", r.vm.creates)
	}
	xml, err := os.ReadFile(filepath.Join(r.config.directory(device, r.assignment("DeviceCommissioning", "00000000-0000-4000-8000-000000000301", 2, nil).Key()), burnInStage+".xml"))
	if err != nil || !strings.Contains(string(xml), "fleet.mode=burnin fleet.duration=60") {
		t.Fatalf("the burn-in boot does not pass the mode and duration: %v", err)
	}
	if disks, _ := filepath.Glob(filepath.Join(r.config.Disks, "*", "*", "disk.raw")); len(disks) != 0 {
		t.Fatalf("the commissioning left disks %v", disks)
	}
}

// TestABurnInFailureOrAShortBurnInFails records a failed burn-in, and one
// that reported before the plan's duration, as proven failures.
func TestABurnInFailureOrAShortBurnInFails(t *testing.T) {
	failing := commissioningRig(t, true)
	failing.vm.burnInFails = true
	if result, evidence := failing.commission("00000000-0000-4000-8000-000000000302", commissioningSpec(nil)); result.Phase != ops.PhaseSucceeded ||
		evidence.Result != ops.CommissioningFailed || evidence.FailedPhase != ops.PhaseBurnIn || evidence.BurnIn.Result != ops.BurnInFailed {
		t.Fatalf("a failed burn-in = %+v, %+v", result, evidence)
	}
	// With the real clock the fake guest reports at once, well short of the
	// plan's minute.
	short := commissioningRig(t, false)
	if result, evidence := short.commission("00000000-0000-4000-8000-000000000303", commissioningSpec(nil)); result.Phase != ops.PhaseSucceeded ||
		evidence.Result != ops.CommissioningFailed || evidence.FailedPhase != ops.PhaseBurnIn {
		t.Fatalf("a short burn-in = %+v, %+v", result, evidence)
	}
}

// TestCommissioningChecksBeforeAnyBoot fails a guest that does not meet the
// plan, and refuses a burn-in image the site does not serve, without booting.
func TestCommissioningChecksBeforeAnyBoot(t *testing.T) {
	r := commissioningRig(t, true)
	result, evidence := r.commission("00000000-0000-4000-8000-000000000304", commissioningSpec(func(c *ops.CommissioningPlanContent) { c.Inspection.CPU.Cores = 4 }))
	if result.Phase != ops.PhaseSucceeded || evidence.Result != ops.CommissioningFailed || evidence.FailedPhase != ops.PhaseInspect || r.vm.creates != 0 {
		t.Fatalf("a mismatched guest = %+v, %+v, boots %d", result, evidence, r.vm.creates)
	}
	refused, _ := r.commission("00000000-0000-4000-8000-000000000305", commissioningSpec(func(c *ops.CommissioningPlanContent) {
		c.BurnIn.Image.Digest = "sha256:" + strings.Repeat("c", 64)
	}))
	if refused.Phase != ops.PhaseFailed || refused.Reason != custody.ReasonTargetUnsupported || r.vm.creates != 0 {
		t.Fatalf("another burn-in image = %+v, boots %d", refused, r.vm.creates)
	}
}
