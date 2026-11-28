package vmwork

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
)

// burnInStage is the commissioning's one boot: the site image in burn-in
// mode, booted from the network like the installer.
const burnInStage = "burnin"

// burnInMarker prefixes the burn-in image's one result line.
const burnInMarker = "FLEET-BURNIN:"

// Commissioning is the local VM's DeviceCommissioning worker. The local VM
// has no BMC, so inspection compares the hardware and firmware the site
// enrolls for the Device, which describe the guest the site defines, with the
// plan. Burn-in is real: one network boot of the site image in burn-in mode
// on a scratch disk, proven by its console receipt. The scratch disk is
// removed afterwards, so a commissioning leaves no disk behind.
type Commissioning struct{ Config }

var _ site.Kind = Commissioning{}

func (k Commissioning) spec(work *site.Work) (ops.DeviceCommissioningSpec, *custody.Refusal) {
	var spec ops.DeviceCommissioningSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil || spec.Plan.Digest == "" {
		return spec, &custody.Refusal{Reason: custody.ReasonInputsUnreadable, Message: "the admitted inputs are not a DeviceCommissioning spec"}
	}
	return spec, nil
}

func (k Commissioning) Check(_ context.Context, work *site.Work) *custody.Refusal {
	spec, refusal := k.spec(work)
	if refusal != nil {
		return refusal
	}
	connection, _ := work.Connection.(*fixture.Connection)
	if connection == nil {
		return &custody.Refusal{Reason: custody.ReasonTargetUnsupported, Message: "this site does not run local VMs"}
	}
	if connection.Target.Hardware == nil || len(connection.Target.Disks) != 1 {
		return &custody.Refusal{Reason: custody.ReasonTargetUnsupported, Message: "the site enrolls no hardware, or not exactly one disk, for the Device"}
	}
	burnIn := spec.Plan.Content.BurnIn
	if burnIn.Image.Name+"@"+burnIn.Image.Digest != k.Image {
		return &custody.Refusal{Reason: custody.ReasonTargetUnsupported, Message: "the site does not serve the plan's burn-in image"}
	}
	if d := burnIn.Duration.Duration; d < time.Minute || d > 24*time.Hour {
		return &custody.Refusal{Reason: custody.ReasonInputsUnreadable, Message: "the burn-in duration is outside 1 minute to 24 hours"}
	}
	return nil
}

func (k Commissioning) Run(ctx context.Context, work *site.Work) (any, error) {
	spec, refusal := k.spec(work)
	if refusal != nil {
		return nil, refusal
	}
	target := work.Connection.(*fixture.Connection).Target
	plan := spec.Plan.Content
	inspected := *target.Hardware
	evidence := ops.CommissioningEvidence{Plan: spec.Plan.ApprovedPlanReference, SerialNumber: work.Snapshot.Extra[custody.SnapshotSerialNumber], SystemUUID: target.SystemUUID,
		BMCPublicKeySHA256: target.BMCPublicKeySHA256, Hardware: &inspected, Firmware: slices.Clone(target.Firmware)}
	if phase, mismatches := fixture.Inspect(plan, target.Hardware, target.Firmware); len(mismatches) > 0 {
		evidence.Result, evidence.FailedPhase, evidence.Mismatches = ops.CommissioningFailed, phase, mismatches
		return evidence, nil
	}
	result, err := k.burnIn(ctx, work, plan.BurnIn.Duration.Duration)
	if err != nil {
		return nil, err
	}
	for _, step := range []string{burnInStage + "-start", burnInStage + "-verified", burnInStage + "-disk-removed"} {
		at, _ := work.JournaledAt(step)
		evidence.Steps = append(evidence.Steps, ops.StepEvidence{Name: step, CompletedAt: metav1.NewTime(at.UTC())})
	}
	started, _ := work.JournaledAt(burnInStage + "-start")
	completed, _ := work.JournaledAt(burnInStage + "-verified")
	evidence.BurnIn = &ops.BurnInEvidence{Result: result, StartedAt: metav1.NewTime(started.UTC()), CompletedAt: metav1.NewTime(completed.UTC())}
	switch {
	case result != ops.BurnInPassed:
		evidence.Result, evidence.FailedPhase, evidence.Mismatches = ops.CommissioningFailed, ops.PhaseBurnIn, []string{"the burn-in image reported a failure"}
	case completed.Sub(started) < plan.BurnIn.Duration.Duration:
		evidence.Result, evidence.FailedPhase, evidence.Mismatches = ops.CommissioningFailed, ops.PhaseBurnIn, []string{"the burn-in reported before the plan's duration"}
	default:
		evidence.Result = ops.CommissioningPassed
	}
	return evidence, nil
}

// bootRecord reads the commissioning's boot admission, creating it once in
// burn-in mode. The receipt is fixed before any boot, so a replay waits for
// the same receipt.
func (k Commissioning) bootRecord(work *site.Work, duration time.Duration) (Boot, error) {
	var boot Boot
	found, err := work.Store().GetRecord(work.Device, bootRecords, Key(work.Key()), &boot)
	if err != nil || found {
		return boot, err
	}
	receipt, token := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(receipt); err != nil {
		return boot, err
	}
	if _, err := rand.Read(token); err != nil {
		return boot, err
	}
	boot = Boot{Operation: work.Key(), Fence: work.Fence, Receipt: hex.EncodeToString(receipt), Token: hex.EncodeToString(token), Expires: k.now().Add(k.window()),
		Mode: burnInStage, DurationSeconds: int64(duration / time.Second)}
	return boot, Installation(k).save(work, boot)
}

// burnIn boots the site image in burn-in mode once on a scratch disk and
// reads its one result. Each effect is a journaled step, so a resumed worker
// continues instead of booting again.
func (k Commissioning) burnIn(ctx context.Context, work *site.Work, duration time.Duration) (ops.BurnInResult, error) {
	install := Installation(k)
	boot, err := k.bootRecord(work, duration)
	if err != nil {
		return "", err
	}
	dir := install.directory(work.Device, work.Key())
	disk := filepath.Join(dir, "disk.raw")
	if !work.Journaled(burnInStage + "-verified") {
		if err := work.Step(ctx, site.Step{
			Name: burnInStage + "-disk",
			Send: func(context.Context) error { return createDisk(dir, disk) },
			Observe: func(context.Context, json.RawMessage) (bool, error) {
				_, err := os.Stat(disk)
				return err == nil, nil
			},
		}); err != nil {
			return "", err
		}
		if k.PXE && !work.Journaled(burnInStage+"-start") {
			if boot.Closed || !k.now().Before(boot.Expires) {
				return "", errors.New("the boot admission expired or closed before the network boot")
			}
			if err := work.Step(ctx, site.Step{Name: burnInStage + "-network", Send: func(ctx context.Context) error { return install.prepareNetwork(ctx, work, boot) }, Idempotent: true}); err != nil {
				return "", err
			}
			boot.Open = true
			if err := install.save(work, boot); err != nil {
				return "", err
			}
		}
		xml := install.domainXML(work, boot, burnInStage)
		if err := os.WriteFile(filepath.Join(dir, burnInStage+".xml"), []byte(xml), 0o600); err != nil {
			return "", err
		}
		// A failed network boot closes its admission at once, as an
		// installer's does.
		fail := func(err error) error {
			if k.PXE && boot.Open && (ctx.Err() == nil || site.DeadlinePassed(ctx)) {
				boot.Open, boot.Closed = false, true
				_ = install.save(work, boot)
			}
			return err
		}
		if err := work.Step(ctx, site.Step{
			Name: burnInStage + "-start",
			Send: func(ctx context.Context) error {
				_, err := k.virsh(ctx, "create", filepath.Join(dir, burnInStage+".xml"))
				return err
			},
			Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) {
				running, err := install.running(ctx, work.Key())
				if err != nil || running {
					return running, err
				}
				_, err = os.Stat(filepath.Join(dir, burnInStage+".log"))
				return err == nil, nil
			},
		}); err != nil {
			return "", fail(err)
		}
		if _, err := k.waitBurnIn(ctx, work, boot); err != nil {
			return "", fail(err)
		}
		if k.PXE {
			if err := install.verifyNetworkBoot(ctx, work); err != nil {
				return "", fail(err)
			}
			boot.Open, boot.Closed = false, true
			if err := install.save(work, boot); err != nil {
				return "", err
			}
		}
		if err := work.Step(ctx, site.Step{Name: burnInStage + "-verified", Send: func(context.Context) error { return nil }, Idempotent: true}); err != nil {
			return "", err
		}
	}
	result, err := k.burnInResult(work, boot)
	if err != nil {
		return "", err
	}
	if k.PXE {
		if err := work.Step(ctx, site.Step{Name: burnInStage + "-network-off", Send: k.stopNetwork, Idempotent: true}); err != nil {
			return "", err
		}
	}
	// The burn-in only reads its scratch disk; removing it leaves the Device
	// with no disk the commissioning made.
	if err := work.Step(ctx, site.Step{
		Name: burnInStage + "-disk-removed",
		Send: func(context.Context) error {
			if err := os.Remove(disk); err != nil && !os.IsNotExist(err) {
				return err
			}
			return syncDir(dir)
		},
		Idempotent: true,
	}); err != nil {
		return "", err
	}
	return result, nil
}

// waitBurnIn waits for the guest to stop, then reads its result.
func (k Commissioning) waitBurnIn(ctx context.Context, work *site.Work, boot Boot) (ops.BurnInResult, error) {
	for {
		running, err := Installation(k).running(ctx, work.Key())
		if err != nil {
			return "", err
		}
		if !running {
			return k.burnInResult(work, boot)
		}
		select {
		case <-ctx.Done():
			return "", context.Cause(ctx)
		case <-time.After(time.Second):
		}
	}
}

// burnInResult reads the one result line with this boot's receipt. Any other
// count proves nothing.
func (k Commissioning) burnInResult(work *site.Work, boot Boot) (ops.BurnInResult, error) {
	data, err := os.ReadFile(filepath.Join(Installation(k).directory(work.Device, work.Key()), burnInStage+".log"))
	if err != nil {
		return "", err
	}
	prefix := burnInMarker + boot.Receipt + ":"
	var results []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, prefix) {
			results = append(results, strings.TrimPrefix(line, prefix))
		}
	}
	if len(results) != 1 {
		return "", errors.New("the guest stopped without exactly one burn-in result")
	}
	switch results[0] {
	case "passed":
		return ops.BurnInPassed, nil
	case "failed":
		return ops.BurnInFailed, nil
	}
	return "", errors.New("the burn-in result is neither passed nor failed")
}
