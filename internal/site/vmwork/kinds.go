// Package vmwork is the local VM site's install and wipe workers. A libvirt
// domain stands in for a server: its disk, a one-time network boot from the
// site's boot server, and console receipts that prove which phase ran. Every
// effect goes through the site framework's fenced journal, so a newer fence
// stops a worker and a resumed worker continues from the journal. The site
// reaches its Devices through fixture.Access. It is a disposable acceptance
// executor, never a production one.
package vmwork

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
)

// DefaultBootWindow bounds one-time network boot admission.
const DefaultBootWindow = 10 * time.Minute

// guestMAC is the provisioning NIC of the one local guest.
const guestMAC = "52:54:00:77:00:02"

// Config is one local VM site.
type Config struct {
	// Artifacts holds the installer kernel, initrd and their SHA256SUMS.
	Artifacts string
	// Image is the only image the site installs: the artifact manifest's
	// identity, "fleet-local-linux@sha256:<digest>".
	Image string
	// Disks holds each installation's disk and console logs.
	Disks string
	// BootRoot holds the boot server's delivery evidence.
	BootRoot string
	// BootOrigin is the boot server's URL in the DHCP boot filename. Empty
	// means the boot server shares the bridge.
	BootOrigin string
	// PXE boots the installer from the network; otherwise directly.
	PXE bool
	// Accelerator is kvm or tcg.
	Accelerator string
	URI         string
	BootWindow  time.Duration
	// Command runs libvirt's virsh. Tests replace it.
	Command func(ctx context.Context, name string, args ...string) (string, error)
	Now     func() time.Time
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c Config) window() time.Duration {
	if c.BootWindow > 0 {
		return c.BootWindow
	}
	return DefaultBootWindow
}

func (c Config) virsh(ctx context.Context, args ...string) (string, error) {
	run := c.Command
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("%s %s failed", name, args[0])
			}
			return string(out), nil
		}
	}
	if c.URI != "" {
		args = append([]string{"-c", c.URI}, args...)
	}
	return run(ctx, "virsh", args...)
}

// Key is an operation's 16-character key in boot URLs and directories.
func Key(operationUID string) string {
	sum := sha256.Sum256([]byte(operationUID))
	return hex.EncodeToString(sum[:])[:16]
}

func deviceKey(device string) string {
	sum := sha256.Sum256([]byte(device))
	return hex.EncodeToString(sum[:])[:16]
}

// directory is one installation's disk and console directory, under its
// Device so a wipe finds every disk of that Device.
func (c Config) directory(device, operationUID string) string {
	return filepath.Join(c.Disks, deviceKey(device), Key(operationUID))
}

func domain(operationUID string) string { return "fleet-local-" + Key(operationUID) }

// Installation is the local VM's DeviceInstallation worker.
type Installation struct{ Config }

// Wipe is the local VM's DeviceWipe worker.
type Wipe struct{ Config }

var (
	_ site.Kind = Installation{}
	_ site.Kind = Wipe{}
)

func enrolled(work *site.Work, disks []string) *custody.Refusal {
	connection, _ := work.Connection.(*fixture.Connection)
	if connection == nil {
		return &custody.Refusal{Reason: custody.ReasonTargetUnsupported, Message: "this site does not run local VMs"}
	}
	if len(disks) != 1 || len(connection.Target.Disks) != 1 || disks[0] != connection.Target.Disks[0] {
		return &custody.Refusal{Reason: custody.ReasonTargetDisks, Message: "the site enrolls exactly one disk per Device"}
	}
	return nil
}

func (k Installation) spec(work *site.Work) (ops.DeviceInstallationSpec, *custody.Refusal) {
	var spec ops.DeviceInstallationSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil {
		return spec, &custody.Refusal{Reason: custody.ReasonInputsUnreadable, Message: "the admitted inputs are not a DeviceInstallation spec"}
	}
	return spec, nil
}

func (k Installation) Check(_ context.Context, work *site.Work) *custody.Refusal {
	spec, refusal := k.spec(work)
	if refusal != nil {
		return refusal
	}
	if spec.Bootstrap != nil {
		return &custody.Refusal{Reason: custody.ReasonBootstrapUnsupported, Message: "bootstrap payloads need the sealed store, which is not implemented"}
	}
	if spec.Image.Name+"@"+spec.Image.Digest != k.Image {
		return &custody.Refusal{Reason: custody.ReasonTargetUnsupported, Message: "the site installs only its verified local image"}
	}
	return enrolled(work, spec.TargetDisks)
}

// Boot is one installation's network boot admission. The boot server serves
// it only while it is open for the installer phase, unexpired, and at the
// Device's stored fence.
type Boot struct {
	Operation string    `json:"operation"`
	Fence     int64     `json:"fence"`
	Receipt   string    `json:"receipt"`
	Token     string    `json:"token"`
	Expires   time.Time `json:"expires"`
	Open      bool      `json:"open,omitempty"`
	Closed    bool      `json:"closed,omitempty"`
}

const bootRecords = "vmboot"

// boot reads the installation's boot admission, creating it once. The
// receipt is fixed before any boot, so a replay waits for the same receipt.
func (k Installation) boot(work *site.Work) (Boot, error) {
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
	boot = Boot{Operation: work.Key(), Fence: work.Fence, Receipt: hex.EncodeToString(receipt), Token: hex.EncodeToString(token), Expires: k.now().Add(k.window())}
	return boot, k.save(work, boot)
}

func (k Installation) save(work *site.Work, boot Boot) error {
	return work.Store().PutRecord(work.Device, work.Fence, bootRecords, Key(work.Key()), boot)
}

func (k Installation) Run(ctx context.Context, work *site.Work) (any, error) {
	if _, refusal := k.spec(work); refusal != nil {
		return nil, refusal
	}
	boot, err := k.boot(work)
	if err != nil {
		return nil, err
	}
	dir := k.directory(work.Device, work.Key())
	var evidence ops.InstallationEvidence
	record := func(step string) {
		at, _ := work.JournaledAt(step)
		evidence.Steps = append(evidence.Steps, ops.StepEvidence{Name: step, CompletedAt: metav1.NewTime(at.UTC())})
	}
	disk := filepath.Join(dir, "disk.raw")
	if err := work.Step(ctx, site.Step{
		Name: "prepare-disk",
		Send: func(context.Context) error { return createDisk(dir, disk) },
		Observe: func(context.Context, json.RawMessage) (bool, error) {
			_, err := os.Stat(disk)
			return err == nil, nil
		},
	}); err != nil {
		return nil, err
	}
	record("prepare-disk")
	for _, stage := range []string{"installer", "disk"} {
		if work.Journaled(stage + "-verified") {
			record(stage)
			continue
		}
		started := work.Journaled(stage + "-start")
		if stage == "installer" && k.PXE && !started {
			if boot.Closed || !k.now().Before(boot.Expires) {
				return nil, errors.New("the boot admission expired or closed before the network boot")
			}
			if err := work.Step(ctx, site.Step{Name: "installer-network", Send: func(ctx context.Context) error { return k.prepareNetwork(ctx, work, boot) }, Idempotent: true}); err != nil {
				return nil, err
			}
			boot.Open = true
			if err := k.save(work, boot); err != nil {
				return nil, err
			}
		}
		if stage == "disk" && k.PXE {
			if err := work.Step(ctx, site.Step{Name: "disk-network-off", Send: k.stopNetwork, Idempotent: true}); err != nil {
				return nil, err
			}
		}
		xml := k.domainXML(work, boot, stage)
		if err := os.WriteFile(filepath.Join(dir, stage+".xml"), []byte(xml), 0o600); err != nil {
			return nil, err
		}
		// A failed network boot closes its admission at once, so the boot
		// server stops serving the installer. A worker stopped from outside
		// leaves it open for its replacement to resume.
		fail := func(err error) error {
			if stage == "installer" && k.PXE && boot.Open && ctx.Err() == nil {
				boot.Open, boot.Closed = false, true
				_ = k.save(work, boot)
			}
			return err
		}
		// A boot is started once. After a crash it is observed: a running
		// domain or this phase's console counts as started, and no sign of
		// either leaves the outcome unknown rather than booting again.
		if err := work.Step(ctx, site.Step{
			Name: stage + "-start",
			Send: func(ctx context.Context) error {
				_, err := k.virsh(ctx, "create", filepath.Join(dir, stage+".xml"))
				return err
			},
			Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) {
				running, err := k.running(ctx, work.Key())
				if err != nil || running {
					return running, err
				}
				_, err = os.Stat(filepath.Join(dir, stage+".log"))
				return err == nil, nil
			},
		}); err != nil {
			return nil, fail(err)
		}
		if err := k.waitReceipt(ctx, work, boot, stage); err != nil {
			return nil, fail(err)
		}
		if stage == "installer" && k.PXE {
			if err := k.verifyNetworkBoot(ctx, work); err != nil {
				return nil, fail(err)
			}
			boot.Open, boot.Closed = false, true
			if err := k.save(work, boot); err != nil {
				return nil, err
			}
		}
		if err := work.Step(ctx, site.Step{Name: stage + "-verified", Send: func(context.Context) error { return nil }, Idempotent: true}); err != nil {
			return nil, err
		}
		record(stage)
	}
	return evidence, nil
}

// createDisk creates the installation's disk exclusively, so an
// interrupted preparation never overwrites one.
func createDisk(dir, disk string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(disk, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	err = file.Truncate(2 << 30)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (k Installation) running(ctx context.Context, operationUID string) (bool, error) {
	names, err := k.virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return false, err
	}
	for _, name := range strings.Fields(names) {
		if name == domain(operationUID) {
			return true, nil
		}
	}
	return false, nil
}

func (k Installation) domainXML(work *site.Work, boot Boot, stage string) string {
	dir := k.directory(work.Device, work.Key())
	bootXML := "<boot dev='hd'/>"
	nic := ""
	if stage == "installer" {
		bootXML = fmt.Sprintf("<kernel>%s/kernel</kernel><initrd>%s/installer.gz</initrd><cmdline>console=ttyS0 rdinit=/init fleet.receipt=%s</cmdline>", k.Artifacts, k.Artifacts, boot.Receipt)
		if k.PXE {
			bootXML = "<boot dev='network'/>"
			nic = "<interface type='network'><source network='fleet-pxe'/><mac address='" + guestMAC + "'/><model type='e1000'/></interface>"
		}
	}
	domainType := "kvm"
	if k.Accelerator == "tcg" {
		domainType = "qemu"
	}
	return fmt.Sprintf(`<domain type='%s'><name>%s</name><memory unit='MiB'>2048</memory><vcpu>2</vcpu><cpu><topology sockets='1' cores='2' threads='1'/></cpu><features><acpi/><apic/></features><os><type arch='x86_64' machine='pc'>hvm</type>%s</os><on_poweroff>destroy</on_poweroff><on_reboot>destroy</on_reboot><on_crash>destroy</on_crash><devices>%s<emulator>/usr/bin/qemu-system-x86_64</emulator><disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='%s/disk.raw'/><target dev='vda' bus='virtio'/></disk><serial type='file'><source path='%s/%s.log'/><target port='0'/></serial></devices></domain>`, domainType, domain(work.Key()), bootXML, nic, dir, dir, stage)
}

// waitReceipt waits for the guest to stop with exactly this phase's receipt.
func (k Installation) waitReceipt(ctx context.Context, work *site.Work, boot Boot, stage string) error {
	marker := "FLEET-INSTALLED:"
	if stage == "disk" {
		marker = "FLEET-DISK-BOOT:"
	}
	log := filepath.Join(k.directory(work.Device, work.Key()), stage+".log")
	for {
		running, err := k.running(ctx, work.Key())
		if err != nil {
			return err
		}
		if !running {
			data, err := os.ReadFile(log)
			if err != nil {
				return err
			}
			count := 0
			for _, line := range strings.Split(string(data), "\n") {
				if strings.TrimSpace(line) == marker+boot.Receipt {
					count++
				}
			}
			if count != 1 {
				return errors.New("the guest stopped without exactly one boot receipt")
			}
			file, err := os.OpenFile(log, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			err = file.Sync()
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
			return syncDir(filepath.Dir(log))
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(time.Second):
		}
	}
}

func (k Installation) wipeSpec(work *site.Work) (ops.DeviceWipeSpec, *custody.Refusal) {
	var spec ops.DeviceWipeSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil || spec.CleaningPolicyDigest == "" {
		return spec, &custody.Refusal{Reason: custody.ReasonInputsUnreadable, Message: "the admitted inputs are not a DeviceWipe spec"}
	}
	return spec, nil
}

func (k Wipe) Check(_ context.Context, work *site.Work) *custody.Refusal {
	spec, refusal := Installation(k).wipeSpec(work)
	if refusal != nil {
		return refusal
	}
	return enrolled(work, spec.TargetDisks)
}

// Run deletes every installation disk of the Device. A disk whose domain
// still runs stays, and the wipe's outcome is unknown.
func (k Wipe) Run(ctx context.Context, work *site.Work) (any, error) {
	spec, refusal := Installation(k).wipeSpec(work)
	if refusal != nil {
		return nil, refusal
	}
	root := filepath.Join(k.Disks, deviceKey(work.Device))
	remaining := func() ([]string, error) {
		paths, err := filepath.Glob(filepath.Join(root, "*", "disk.raw"))
		return paths, err
	}
	step := site.Step{
		Name: "wipe",
		Send: func(ctx context.Context) error {
			names, err := k.virsh(ctx, "list", "--all", "--name")
			if err != nil {
				return err
			}
			for _, name := range strings.Fields(names) {
				if strings.HasPrefix(name, "fleet-local-") {
					return errors.New("a local domain still runs; the wipe cannot prove the disks are unused")
				}
			}
			paths, err := remaining()
			if err != nil {
				return err
			}
			for _, path := range paths {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				if err := syncDir(filepath.Dir(path)); err != nil {
					return err
				}
			}
			return nil
		},
		Observe: func(context.Context, json.RawMessage) (bool, error) {
			paths, err := remaining()
			return err == nil && len(paths) == 0, err
		},
		// Deleting a deleted disk changes nothing.
		Idempotent: true,
	}
	if err := work.Step(ctx, step); err != nil {
		return nil, err
	}
	if paths, err := remaining(); err != nil || len(paths) != 0 {
		return nil, &custody.Refusal{Reason: site.ReasonStep, Message: "a disk remained after the wipe"}
	}
	at, _ := work.JournaledAt(step.Name)
	return ops.WipeEvidence{Steps: []ops.StepEvidence{{Name: step.Name, CompletedAt: metav1.NewTime(at.UTC())}}, WipedDisks: spec.TargetDisks, PolicyDigest: spec.CleaningPolicyDigest}, nil
}
