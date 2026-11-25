// Package redfishwork runs DeviceInstallation and DeviceWipe over Redfish
// behind the site worker contract. It ports the phases of the Redfish
// executor: power off, arm a one-time boot, power on once, then observe the
// console receipt and, for a network boot, the boot server's delivery
// evidence. Each effect is a journaled step; the framework owns the fence,
// journal, credential, pin, identity and deadline.
//
// Increment B is inert: the old executor in internal/redfish keeps running
// the existing lifecycle until the cutover in Increment C deletes it.
package redfishwork

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Kind-specific refusal reasons. Each ends the operation Failed with nothing
// sent.
// They are declared on the DeviceInstallation and DeviceWipe kinds as raised
// by the worker.
const (
	ReasonUnsupportedTarget = custody.ReasonTargetUnsupported
	ReasonTargetDisks       = custody.ReasonTargetDisks
	ReasonInputs            = custody.ReasonInputsUnreadable
)

// phase is one boot of the server.
type phase struct{ name, boot, marker, mode string }

var (
	installPhases = []phase{{"installer", "Pxe", "FLEET-INSTALLED:", "install"}, {"disk", "Hdd", "FLEET-DISK-BOOT:", ""}}
	wipePhases    = []phase{{"wipe", "Pxe", "FLEET-WIPED:", "wipe"}}
)

// Config is what both kinds read at the site.
type Config struct {
	// ConsoleRoot holds each target's serial console capture.
	ConsoleRoot string
	// BootRoot holds the boot server's per-operation delivery evidence.
	BootRoot string
	// Poll is how often the kinds observe the server. It defaults to two
	// seconds.
	Poll time.Duration
	// OffGrace bounds how long the server may stay off without its receipt,
	// and Outage how long the BMC may be unreadable while a phase runs.
	OffGrace, Outage time.Duration
	// BootWindow bounds how long a network boot admission stays open.
	BootWindow time.Duration
	// PowerOffBound bounds how long the server may take to power off. It
	// defaults to five minutes.
	PowerOffBound time.Duration
	// Synced means the site's DHCP is synchronized: a network boot waits
	// until DHCP applied the boot NIC's reservation, and the operation ends
	// only once every DHCP instance that served it withdrew it.
	Synced bool
	Now    func() time.Time
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Config) durations() (poll, offGrace, outage, window time.Duration) {
	poll, offGrace, outage, window = c.Poll, c.OffGrace, c.Outage, c.BootWindow
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if offGrace <= 0 {
		offGrace = 30 * time.Second
	}
	if outage <= 0 {
		outage = 5 * time.Minute
	}
	if window <= 0 {
		window = 20 * time.Minute
	}
	return
}

// Installation is the DeviceInstallation worker.
type Installation struct{ Config }

// Wipe is the DeviceWipe worker.
type Wipe struct{ Config }

var (
	_ site.Kind = Installation{}
	_ site.Kind = Wipe{}
)

// conn is the Redfish connection the site prepared. Check refuses any other.
func conn(work *site.Work) *site.RedfishConnection {
	connection, _ := work.Connection.(*site.RedfishConnection)
	return connection
}

func (k Installation) Check(_ context.Context, work *site.Work) *custody.Refusal {
	if conn(work) == nil {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "this site does not reach the server over Redfish"}
	}
	var spec ops.DeviceInstallationSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil {
		return &custody.Refusal{Reason: ReasonInputs, Message: "the admitted inputs are not a DeviceInstallation spec"}
	}
	if spec.Bootstrap != nil {
		return &custody.Refusal{Reason: custody.ReasonBootstrapUnsupported, Message: "bootstrap payloads need the sealed store, which is not implemented"}
	}
	return checkTarget(work, spec.TargetDisks, installPhases)
}

func (k Wipe) Check(_ context.Context, work *site.Work) *custody.Refusal {
	if conn(work) == nil {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "this site does not reach the server over Redfish"}
	}
	var spec ops.DeviceWipeSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil {
		return &custody.Refusal{Reason: ReasonInputs, Message: "the admitted inputs are not a DeviceWipe spec"}
	}
	if conn(work).Target.DiskBytes <= 0 {
		return &custody.Refusal{Reason: ReasonTargetDisks, Message: "the site's enrollment records no disk size to verify the wipe against"}
	}
	return checkTarget(work, spec.TargetDisks, wipePhases)
}

// checkTarget requires the one enrolled disk and every capability the phases
// use. Target disks stay a site-local enrollment fact: the site enrolls one
// disk per server, named disk-0.
func checkTarget(work *site.Work, disks []string, phases []phase) *custody.Refusal {
	if len(disks) != 1 || disks[0] != "disk-0" {
		return &custody.Refusal{Reason: ReasonTargetDisks, Message: "the site enrolls exactly one disk, disk-0"}
	}
	if conn(work).Target.Console == "" {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the site's enrollment names no console for receipts"}
	}
	for _, ph := range phases {
		if !contains(conn(work).System.BootTargets, ph.boot) {
			return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the system does not offer boot override " + ph.boot}
		}
	}
	if !conn(work).System.OffersOnce() {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the system does not offer a one-time boot override"}
	}
	for _, need := range []string{"On", "ForceOff"} {
		if !contains(conn(work).System.ResetTypes, need) {
			return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the system does not offer reset " + need}
		}
	}
	return nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func (k Installation) Run(ctx context.Context, work *site.Work) (any, error) {
	steps, err := k.run(ctx, work, installPhases)
	if err != nil {
		return nil, err
	}
	return ops.InstallationEvidence{Steps: steps}, nil
}

func (k Wipe) Run(ctx context.Context, work *site.Work) (any, error) {
	steps, err := k.run(ctx, work, wipePhases)
	if err != nil {
		return nil, err
	}
	return ops.WipeEvidence{Steps: steps, WipedDisks: []string{"disk-0"}}, nil
}

// powerOn is the journaled payload of a power-on step.
type powerOn struct {
	RequestedAt time.Time `json:"requestedAt"`
}

// Boot is one operation's boot admission. The boot server serves a network
// boot only while an admission is open and its fence is still the Device's
// stored fence.
type Boot struct {
	Operation string `json:"operation"`
	Fence     int64  `json:"fence"`
	Mode      string `json:"mode"`
	MAC       string `json:"mac"`
	Receipt   string `json:"receipt"`
	Token     string `json:"token"`
	// Created is when the admission was created; DHCP reports before it
	// are not this operation's.
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	// DurationSeconds is how long a burn-in image runs. Other modes omit it.
	DurationSeconds int64 `json:"durationSeconds,omitempty"`
	// Phase is the network-booting phase, set while it may boot.
	Phase  string `json:"phase,omitempty"`
	Closed bool   `json:"closed,omitempty"`
}

const bootRecords = "boot"

// boot reads the operation's boot admission, creating it once. The receipt is
// fixed before any boot, so a replay waits for the same receipt.
func (c Config) boot(ctx context.Context, work *site.Work, mode string) (Boot, error) {
	store := work.Store()
	var boot Boot
	found, err := store.GetRecord(work.Device, bootRecords, work.Key(), &boot)
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
	_, _, _, window := c.durations()
	boot = Boot{Operation: work.Key(), Fence: work.Fence, Mode: mode, MAC: strings.ToLower(conn(work).Target.MAC), Receipt: hex.EncodeToString(receipt), Token: hex.EncodeToString(token), Created: c.now().UTC(), Expires: c.now().Add(window).UTC()}
	return boot, store.PutRecord(work.Device, work.Fence, bootRecords, work.Key(), boot)
}

func (c Config) saveBoot(work *site.Work, boot Boot) error {
	return work.Store().PutRecord(work.Device, work.Fence, bootRecords, work.Key(), boot)
}

func (c Config) run(ctx context.Context, work *site.Work, phases []phase) ([]ops.StepEvidence, error) {
	mode := phases[0].mode
	boot, err := c.boot(ctx, work, mode)
	if err != nil {
		return nil, err
	}
	// A worker stopped at its deadline closes its admission: nothing can
	// resume the operation, so nothing may boot the server from it. A worker
	// stopped from outside leaves it open for its replacement.
	defer func() {
		if mode != "" && site.DeadlinePassed(ctx) && !boot.Closed {
			boot.Phase, boot.Closed = "", true
			_ = c.saveBoot(work, boot)
		}
	}()
	var evidence []ops.StepEvidence
	for _, ph := range phases {
		// A phase verified by an earlier worker is done; resume after it.
		if at, done := work.JournaledAt(ph.name + "-verified"); done {
			evidence = append(evidence, ops.StepEvidence{Name: ph.name, CompletedAt: metaNow(at)})
			continue
		}
		// The boot admission is checked only before this phase's boot is
		// requested; a resumed worker whose boot already began observes it.
		requested := work.Journaled(ph.name + "-power-on")
		// Power off and arm the one-time override. Both are safe to send
		// again: no boot of this phase has been requested yet.
		if err := work.Step(ctx, site.Step{Name: ph.name + "-power-off", Send: func(ctx context.Context) error { return c.powerOff(ctx, work) }, Idempotent: true}); err != nil {
			return nil, err
		}
		if ph.mode != "" && !requested {
			if boot.Closed || !c.now().Before(boot.Expires) {
				return nil, errors.New("boot admission expired or closed before the network boot")
			}
		}
		if err := work.Step(ctx, site.Step{Name: ph.name + "-arm", Send: func(ctx context.Context) error { return conn(work).SetBootOnce(ctx, ph.boot) }, Idempotent: true}); err != nil {
			return nil, err
		}
		if ph.mode != "" {
			boot.Phase = ph.name
			if err := c.saveBoot(work, boot); err != nil {
				return nil, err
			}
		}
		if ph.mode != "" && c.Synced && !requested {
			if err := c.awaitReservation(ctx, work, boot); err != nil {
				return nil, err
			}
		}
		// Power on is sent once. After a crash or lost response it is
		// observed: any sign that the boot began counts, and no sign leaves
		// the outcome unknown rather than sending it again.
		if err := work.Step(ctx, site.Step{
			Name:    ph.name + "-power-on",
			Payload: powerOn{RequestedAt: c.now().UTC()},
			Send:    func(ctx context.Context) error { return conn(work).Reset(ctx, "On") },
			Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) { return c.started(ctx, work, boot, ph) },
		}); err != nil {
			return nil, err
		}
		if err := c.complete(ctx, work, boot, ph); err != nil {
			return nil, err
		}
		if ph.mode != "" {
			boot.Phase, boot.Closed = "", true
			if err := c.saveBoot(work, boot); err != nil {
				return nil, err
			}
		}
		// Journal the verified phase, so a resumed worker skips it.
		if err := work.Step(ctx, site.Step{Name: ph.name + "-verified", Send: func(context.Context) error { return nil }, Idempotent: true}); err != nil {
			return nil, err
		}
		evidence = append(evidence, ops.StepEvidence{Name: ph.name, CompletedAt: metaNow(c.now())})
	}
	if c.Synced {
		if err := c.awaitWithdrawal(ctx, work, boot); err != nil {
			return nil, err
		}
	}
	return evidence, nil
}

// defaultPowerOffBound is how long the server may take to power off, as the
// old executor allowed.
const defaultPowerOffBound = 5 * time.Minute

func (c Config) powerOff(ctx context.Context, work *site.Work) error {
	poll, _, _, _ := c.durations()
	requested := false
	bound := c.PowerOffBound
	if bound <= 0 {
		bound = defaultPowerOffBound
	}
	deadline := c.now().Add(bound)
	for {
		power, err := conn(work).Observe(ctx)
		if err != nil {
			return err
		}
		if power.State == "Off" {
			return nil
		}
		if !requested {
			// A slow or lost ForceOff is observed, not trusted.
			if err := conn(work).Reset(ctx, "ForceOff"); errors.Is(err, redfish.ErrUnauthorized) || errors.Is(err, redfish.ErrUnsupported) {
				return err
			}
			requested = true
		}
		if c.now().After(deadline) {
			return errors.New("the server did not power off")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// started reports whether a requested boot may have begun: power not off,
// the override consumed, a receipt, or a boot delivery.
func (c Config) started(ctx context.Context, work *site.Work, boot Boot, ph phase) (bool, error) {
	power, err := conn(work).Observe(ctx)
	if err != nil {
		return false, err
	}
	seen, err := c.receipts(work, boot, ph)
	if err != nil {
		return false, err
	}
	delivered := ph.mode != "" && c.deliveries(work)["admitted boot.ipxe"]
	return power.State != "Off" || power.OverrideEnabled != "Once" || power.OverrideTarget != ph.boot || seen > 0 || delivered, nil
}

// complete waits for exactly one receipt for this operation and the server
// off again. The BMC may be unreadable for a bounded time.
func (c Config) complete(ctx context.Context, work *site.Work, boot Boot, ph phase) error {
	poll, offGrace, outage, _ := c.durations()
	var offSince, failingSince time.Time
	for {
		power, err := conn(work).Observe(ctx)
		switch {
		case errors.Is(err, redfish.ErrIdentity):
			return err
		case err != nil:
			if failingSince.IsZero() {
				failingSince = c.now()
			}
			if c.now().Sub(failingSince) > outage {
				return fmt.Errorf("the BMC was unreadable while %s ran: %w", ph.name, err)
			}
		default:
			failingSince = time.Time{}
			seen, err := c.receipts(work, boot, ph)
			if err != nil {
				return err
			}
			if power.State == "Off" {
				if seen == 1 {
					if ph.mode != "" {
						return c.verifyDelivery(work)
					}
					return nil
				}
				if offSince.IsZero() {
					offSince = c.now()
				}
				if c.now().Sub(offSince) > offGrace {
					return fmt.Errorf("the server powered off without the %s receipt", ph.name)
				}
			} else {
				offSince = time.Time{}
			}
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// receipts counts exact receipt lines for this operation on the console.
// More than one means the phase ran twice, which never succeeds.
func (c Config) receipts(work *site.Work, boot Boot, ph phase) (int, error) {
	data, err := os.ReadFile(filepath.Join(c.ConsoleRoot, filepath.Base(conn(work).Target.Console)))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	want := []string{ph.marker + boot.Receipt}
	switch ph.name {
	case "wipe":
		want[0] += ":" + strconv.FormatInt(conn(work).Target.DiskBytes, 10)
	case burnInPhase.name:
		// A burn-in receipt carries the image's result.
		want = []string{want[0] + ":passed", want[0] + ":failed"}
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if slices.Contains(want, strings.TrimSpace(line)) {
			n++
		}
	}
	if ph.name == "disk" {
		installed, err := c.receipts(work, boot, installPhases[0])
		if err != nil {
			return 0, err
		}
		if installed != 1 {
			return 0, errors.New("a disk receipt needs exactly one installer receipt")
		}
	}
	if n > 1 {
		return 0, fmt.Errorf("duplicate %s receipt: the phase ran more than once", ph.name)
	}
	return n, nil
}

// BootDirectory is where the boot server writes an operation's delivery
// evidence.
func BootDirectory(root, operationUID string) string {
	sum := sha256.Sum256([]byte(operationUID))
	return filepath.Join(root, hex.EncodeToString(sum[:])[:16])
}

func (c Config) deliveries(work *site.Work) map[string]bool {
	data, _ := os.ReadFile(filepath.Join(BootDirectory(c.BootRoot, work.Key()), "boot-delivery.log"))
	done := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 4 && (fields[1] == "write-complete" || fields[1] == "admitted") && strings.HasPrefix(fields[3], "bytes=") {
			done[fields[1]+" "+fields[2]] = true
		}
	}
	return done
}

func (c Config) verifyDelivery(work *site.Work) error {
	done := c.deliveries(work)
	for _, name := range []string{"boot.ipxe", "kernel", "installer.gz"} {
		if !done["write-complete "+name] {
			return fmt.Errorf("missing network delivery: %s", name)
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func metaNow(at time.Time) metav1.Time { return metav1.NewTime(at.UTC()) }

// OpenAdmission returns the open network boot admission for a boot NIC, as
// the boot server must check it: open for a network-booting phase, not
// expired, and at the Device's stored fence, so a superseded operation can
// never boot the server.
func OpenAdmission(store *site.Store, mac string, now time.Time) (Boot, bool, error) {
	records, err := store.Records(bootRecords)
	if err != nil {
		return Boot{}, false, err
	}
	for _, record := range records {
		var boot Boot
		if json.Unmarshal(record.Data, &boot) != nil || boot.MAC != strings.ToLower(mac) || boot.Phase == "" || boot.Closed || !now.Before(boot.Expires) {
			continue
		}
		fence, _, err := store.Peek(record.Device)
		if err != nil {
			return Boot{}, false, err
		}
		if fence == boot.Fence {
			return boot, true, nil
		}
	}
	return Boot{}, false, nil
}
