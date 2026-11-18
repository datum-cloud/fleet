// Package localvm implements a deliberately bounded local VM executor.
// It is not a remotely accessible provider or a production authorization boundary.
package localvm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	"golang.org/x/sys/unix"
)

const Root = "/var/lib/fleet-local"
const artifacts = "/opt/fleet-local/artifacts"

// bootWindow bounds one-time network boot admission after an install is retained.
const bootWindow = 10 * time.Minute

const AssetID = "fleet-provider/fleet.datumapis.com/Device/synthetic-device-central-01"

type Provider struct {
	bootMode    string
	image       string
	accelerator string
	root        string
	// bootRoot holds boot-service delivery evidence. In the Kubernetes site it
	// is a separate volume path so the boot service never writes the journal.
	bootRoot string
	// bootOrigin is the boot Service URL written into the DHCP boot filename.
	bootOrigin string
	// uri selects the libvirt daemon; the site shares only its socket.
	uri string
	// readOnly providers (boot service, observer) never take the journal lock,
	// create inventory, or execute effects.
	readOnly      bool
	command       func(context.Context, string, ...string) (string, error)
	syncDirectory func(string) error
}

func parseAccelerator(value string) (string, error) {
	switch value {
	case "", "kvm":
		return "kvm", nil
	case "tcg":
		return "tcg", nil
	default:
		return "", errors.New("FLEET_LOCAL_VM_ACCEL must be kvm or tcg")
	}
}
func (p *Provider) mode() string {
	if p.accelerator == "" {
		return "kvm"
	}
	return p.accelerator
}

// localKVM reports whether this process starts KVM guests through a local
// libvirtd and so needs /dev/kvm itself. Through a shared socket the runtime
// that owns the daemon holds and checks the device; read-only providers never
// start a guest.
func (p *Provider) localKVM() bool {
	return p.mode() == "kvm" && p.uri == "qemu:///system"
}
func (p *Provider) checkMode(j journal) error {
	mode, err := parseAccelerator(j.Accelerator)
	if err != nil {
		return err
	}
	if j.Image != "" && (mode != p.mode() || j.BootMode != p.bootMode) {
		return executor.ErrConflict
	}
	return nil
}
func (p *Provider) stateRoot() string {
	if p.root != "" {
		return p.root
	}
	return Root
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (p *Provider) syncDir(path string) error {
	if p.syncDirectory != nil {
		return p.syncDirectory(path)
	}
	return syncDir(path)
}

type record struct {
	Request     simulator.Request         `json:"request"`
	Result      simulator.OperationResult `json:"result"`
	Receipt     string                    `json:"receipt"`
	Stage       string                    `json:"stage"`
	BootToken   string                    `json:"bootToken,omitempty"`
	BootExpires time.Time                 `json:"bootExpires"`
	BootClosed  bool                      `json:"bootClosed"`
}
type journal struct {
	BootMode    string             `json:"bootMode"`
	Accelerator string             `json:"accelerator"`
	Image       string             `json:"image"`
	Epoch       int64              `json:"epoch"`
	Binding     string             `json:"binding"`
	Fence       string             `json:"fence"`
	Closed      bool               `json:"closed"`
	Operations  map[string]*record `json:"operations"`
}

// Open returns the effectful provider used only by per-operation workers.
func Open() (*Provider, error) {
	p, err := open()
	if err != nil {
		return nil, err
	}
	if p.localKVM() {
		if _, err := os.Stat("/dev/kvm"); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(Root, 0700); err != nil {
		return nil, err
	}
	inventory := []byte(`{"kind":"Device","name":"synthetic-device-central-01","assetID":"` + AssetID + `","management":"LocalOwned","architecture":"amd64","vcpus":2,"memoryMiB":2048,"diskBytes":2147483648,"networkInterfaces":1}`)
	guard, e := p.lock()
	if e != nil {
		return nil, e
	}
	defer guard.Close()
	current, e := os.ReadFile(Root + "/device.json")
	if os.IsNotExist(e) {
		f, e := os.OpenFile(Root+"/device.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(inventory)
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	} else if string(current) != string(inventory) {
		return nil, errors.New("local device inventory differs; adoption is not authorized")
	}
	return p, nil
}

// OpenReadOnly verifies the same artifacts and configuration but cannot
// execute, lock, or create state. The boot service and observer use it with
// a read-only journal mount.
func OpenReadOnly() (*Provider, error) {
	p, err := open()
	if err != nil {
		return nil, err
	}
	p.readOnly = true
	return p, nil
}

func open() (*Provider, error) {
	if os.Getenv("FLEET_LOCAL_VM") != "disposable-container" {
		return nil, errors.New("local VM provider requires its disposable container")
	}
	accelerator, err := parseAccelerator(os.Getenv("FLEET_LOCAL_VM_ACCEL"))
	if err != nil {
		return nil, err
	}
	origin, err := parseBootOrigin(os.Getenv("FLEET_BOOT_ORIGIN"))
	if err != nil {
		return nil, err
	}
	uri, err := parseLibvirtURI(os.Getenv("FLEET_LIBVIRT_URI"))
	if err != nil {
		return nil, err
	}
	bootRoot := os.Getenv("FLEET_BOOT_EVIDENCE")
	if bootRoot != "" && (!filepath.IsAbs(bootRoot) || filepath.Clean(bootRoot) != bootRoot) {
		return nil, errors.New("FLEET_BOOT_EVIDENCE must be a clean absolute path")
	}
	b, err := os.ReadFile(artifacts + "/SHA256SUMS")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		return nil, errors.New("artifact manifest requires exactly two artifacts")
	}
	seen := map[string]bool{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[1] != "kernel" && fields[1] != "installer.gz") {
			return nil, errors.New("invalid artifact manifest")
		}
		if seen[fields[1]] {
			return nil, errors.New("duplicate artifact")
		}
		seen[fields[1]] = true
		f, e := os.Open(artifacts + "/" + fields[1])
		if e != nil {
			return nil, e
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		f.Close()
		if e != nil {
			return nil, e
		}
		if hex.EncodeToString(h.Sum(nil)) != fields[0] {
			return nil, errors.New("artifact digest mismatch")
		}
	}
	sum := sha256.Sum256(b)
	p := &Provider{accelerator: accelerator, bootMode: "pxe", bootOrigin: origin, bootRoot: bootRoot, uri: uri}
	p.image = "fleet-local-linux@sha256:" + hex.EncodeToString(sum[:])
	return p, nil
}

// parseBootOrigin accepts only a literal http://IPv4:port origin. The site
// derives it from the boot Service ClusterIP; no hostname is resolved.
func parseBootOrigin(value string) (string, error) {
	if value == "" {
		return defaultBootOrigin, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("FLEET_BOOT_ORIGIN must be http://IPv4:port")
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if ip == nil || ip.To4() == nil || err != nil || port < 1 || port > 65535 {
		return "", errors.New("FLEET_BOOT_ORIGIN must be http://IPv4:port")
	}
	return "http://" + ip.To4().String() + ":" + strconv.Itoa(port), nil
}

// parseLibvirtURI allows only the system daemon, optionally through an
// explicit absolute socket path shared from the runtime Pod.
func parseLibvirtURI(value string) (string, error) {
	if value == "" || value == "qemu:///system" {
		return "qemu:///system", nil
	}
	const prefix = "qemu:///system?socket="
	socket := strings.TrimPrefix(value, prefix)
	if !strings.HasPrefix(value, prefix) || !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || strings.ContainsAny(socket, "&?#") {
		return "", errors.New("FLEET_LIBVIRT_URI must be qemu:///system or qemu:///system?socket=/absolute/path")
	}
	return value, nil
}

func (p *Provider) lock() (*os.File, error) {
	f, e := os.OpenFile(p.stateRoot()+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
func (p *Provider) read() (journal, error) {
	var j journal
	b, e := os.ReadFile(p.stateRoot() + "/journal.json")
	if os.IsNotExist(e) {
		j.Operations = map[string]*record{}
		return j, nil
	}
	if e != nil {
		return j, e
	}
	e = json.Unmarshal(b, &j)
	if e == nil {
		for id, r := range j.Operations {
			if r == nil {
				return j, errors.New("invalid retained operation")
			}
			if _, err := hex.DecodeString(r.Receipt); err != nil {
				return j, errors.New("invalid receipt")
			}
			switch r.Stage {
			case "admitted", "prepared", "installer-started", "installer-verified", "disk-started", "disk-verified":
				if r.Result.Phase != "Unknown" || r.Result.CompletedAt != nil {
					return j, errors.New("invalid pending result")
				}
			case "done":
				if r.Result.Phase != "Succeeded" || r.Result.CompletedAt == nil || (r.Request.Kind == "install" && r.Result.Result != "installed") || (r.Request.Kind == "cleanup" && r.Result.Result != "clean") {
					return j, errors.New("invalid terminal result")
				}
			default:
				return j, errors.New("unknown journal stage")
			}
			d, de := simulator.RequestDigest(r.Request)
			if de != nil || r.Request.ID != id || r.Result.ID != id || r.Result.Kind != r.Request.Kind || d != r.Result.InputDigest || r.Request.AssetID != AssetID || len(r.Receipt) != 64 {
				return j, errors.New("invalid retained request")
			}
		}
	}
	if e == nil && j.Operations == nil {
		e = errors.New("invalid journal")
	}
	return j, e
}
func (p *Provider) save(j journal) error {
	b, e := json.MarshalIndent(j, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(p.stateRoot()+"/journal.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(p.stateRoot()+"/journal.tmp", p.stateRoot()+"/journal.json"); e != nil {
		return e
	}
	d, e := os.Open(p.stateRoot())
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func key(id string) string { s := sha256.Sum256([]byte(id)); return hex.EncodeToString(s[:]) }
func run(ctx context.Context, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	b, e := c.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("%s failed: %w: %s", name, e, b)
	}
	return strings.TrimSpace(string(b)), nil
}
func (p *Provider) virsh(ctx context.Context, args ...string) (string, error) {
	command := p.command
	if command == nil {
		command = run
	}
	uri := p.uri
	if uri == "" {
		uri = "qemu:///system"
	}
	return command(ctx, "virsh", append([]string{"--connect", uri}, args...)...)
}

// Lookup reports only terminal retained results. A read-only provider reads
// without the lock: atomic journal replacement makes each read consistent.
func (p *Provider) Lookup(ctx context.Context, id string) (simulator.OperationResult, bool, error) {
	if !p.readOnly {
		f, e := p.lock()
		if e != nil {
			return simulator.OperationResult{}, false, e
		}
		defer f.Close()
	}
	j, e := p.read()
	if e != nil {
		return simulator.OperationResult{}, false, e
	}
	if e = p.checkMode(j); e != nil {
		return simulator.OperationResult{}, false, e
	}
	r := j.Operations[id]
	if r == nil {
		return simulator.OperationResult{}, false, nil
	}
	if r.Stage != "done" {
		return simulator.OperationResult{}, false, executor.ErrOutcomeUnknown
	}
	return r.Result, true, nil
}
func (p *Provider) Execute(ctx context.Context, q simulator.Request) (simulator.OperationResult, error) {
	if p.readOnly {
		return simulator.OperationResult{}, executor.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	f, e := p.lock()
	if e != nil {
		return simulator.OperationResult{}, e
	}
	defer f.Close()
	if e = p.validate(q); e != nil {
		return simulator.OperationResult{}, e
	}
	digest, e := simulator.RequestDigest(q)
	if e != nil {
		return simulator.OperationResult{}, e
	}
	j, e := p.read()
	if e != nil {
		return simulator.OperationResult{}, e
	}
	if e = p.checkMode(j); e != nil {
		return simulator.OperationResult{}, e
	}
	j.Accelerator = p.mode()
	j.BootMode = p.bootMode
	if j.Image != "" && j.Image != p.image {
		return simulator.OperationResult{}, executor.ErrConflict
	}
	j.Image = p.image
	r := j.Operations[q.ID]
	if r != nil {
		if r.Result.InputDigest != digest {
			return simulator.OperationResult{}, executor.ErrConflict
		}
		if r.Stage == "done" {
			return r.Result, nil
		}
	} else {
		for _, old := range j.Operations {
			if old.Stage != "done" {
				return simulator.OperationResult{}, executor.ErrOutcomeUnknown
			}
		}
		if q.Epoch < j.Epoch || (q.Epoch == j.Epoch && (j.Closed || q.BindingID != j.Binding || q.FenceToken != j.Fence)) || (q.Epoch > j.Epoch && j.Epoch != 0 && !j.Closed) {
			return simulator.OperationResult{}, executor.ErrFenced
		}
		j.Epoch = q.Epoch
		j.Binding = q.BindingID
		j.Fence = q.FenceToken
		j.Closed = false
		nonce := make([]byte, 32)
		if _, e = rand.Read(nonce); e != nil {
			return simulator.OperationResult{}, e
		}
		r = &record{Request: q, Receipt: hex.EncodeToString(nonce), Stage: "admitted", Result: simulator.OperationResult{ID: q.ID, Kind: q.Kind, InputDigest: digest, Phase: "Unknown", CreatedAt: time.Now().UTC()}}
		if p.bootMode == "pxe" && q.Kind == "install" {
			token := make([]byte, 32)
			if _, e = rand.Read(token); e != nil {
				return simulator.OperationResult{}, e
			}
			r.BootToken = hex.EncodeToString(token)
			r.BootExpires = time.Now().UTC().Add(bootWindow)
		}
		j.Operations[q.ID] = r
		if e = p.save(j); e != nil {
			return simulator.OperationResult{}, e
		}
	}
	if q.Kind == "install" {
		e = p.install(ctx, &j, r)
	} else {
		e = p.cleanup(ctx, &j, r)
	}
	if e != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: %v", executor.ErrOutcomeUnknown, e)
	}
	now := time.Now().UTC()
	r.Stage = "done"
	r.Result.Phase = "Succeeded"
	r.Result.CompletedAt = &now
	r.Result.Result = "installed"
	if q.Kind == "cleanup" {
		r.Result.Result = "clean"
		j.Closed = true
	}
	if e = p.save(j); e != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: %v", executor.ErrOutcomeUnknown, e)
	}
	return r.Result, nil
}
func (p *Provider) directory(r *record) string {
	return filepath.Join(p.stateRoot(), key(r.Request.ID))
}

// bootDirectory holds boot-service delivery evidence for one operation.
func (p *Provider) bootDirectory(r *record) string {
	if p.bootRoot == "" {
		return p.directory(r)
	}
	return filepath.Join(p.bootRoot, key(r.Request.ID))
}
func domain(r *record) string { return "fleet-local-" + key(r.Request.ID)[:24] }
func (p *Provider) install(ctx context.Context, j *journal, r *record) error {
	dir := p.directory(r)
	if r.Stage == "admitted" {
		if e := os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
			return e
		}
		// Exclusive creation prevents an interrupted preparation from overwriting a disk.
		f, e := os.OpenFile(dir+"/disk.raw", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		e = f.Truncate(2 << 30)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e = p.syncDir(dir); e != nil {
			return e
		}
		r.Stage = "prepared"
		if e = p.save(*j); e != nil {
			return e
		}
	}
	for _, stage := range []string{"installer", "disk"} {
		before := "prepared"
		if stage == "disk" {
			before = "installer-verified"
		}
		if r.Stage == before {
			if stage == "disk" && p.bootMode == "pxe" {
				if !r.BootClosed {
					return errors.New("boot admission still open")
				}
				if e := p.stopNetwork(ctx); e != nil {
					return e
				}
			}
			if stage == "installer" && p.bootMode == "pxe" {
				if !time.Now().Before(r.BootExpires) || r.BootClosed {
					return errors.New("boot admission expired or closed")
				}
				if e := p.prepareNetwork(ctx, r); e != nil {
					return e
				}
			}
			xml := p.domainXML(r, stage)
			if e := os.WriteFile(dir+"/"+stage+".xml", []byte(xml), 0600); e != nil {
				return e
			}
			// Retain start uncertainty before invoking libvirt. Recovery observes it;
			// it never repeats a possibly executed installer boot.
			r.Stage = stage + "-started"
			if e := p.save(*j); e != nil {
				return e
			}
			if _, e := p.virsh(ctx, "create", dir+"/"+stage+".xml"); e != nil {
				return e
			}
		}
		if r.Stage == stage+"-started" {
			if e := p.waitReceipt(ctx, r, stage); e != nil {
				return e
			}
			if stage == "installer" && p.bootMode == "pxe" {
				if e := p.verifyNetworkBoot(ctx, r); e != nil {
					return e
				}
				r.BootClosed = true
			}
			r.Stage = stage + "-verified"
			if e := p.save(*j); e != nil {
				return e
			}
		}
	}
	if r.Stage != "disk-verified" {
		return errors.New("unexpected installation stage")
	}
	return nil
}
func (p *Provider) domainXML(r *record, stage string) string {
	dir := p.directory(r)
	boot := "<boot dev='hd'/>"
	nic := ""
	if stage == "installer" {
		boot = fmt.Sprintf("<kernel>%s/kernel</kernel><initrd>%s/installer.gz</initrd><cmdline>console=ttyS0 rdinit=/init fleet.receipt=%s</cmdline>", artifacts, artifacts, r.Receipt)
	}
	if stage == "installer" && p.bootMode == "pxe" {
		boot = "<boot dev='network'/>"
		nic = "<interface type='network'><source network='fleet-pxe'/><mac address='52:54:00:77:00:02'/><model type='e1000'/></interface>"
	}
	domainType := "kvm"
	if p.mode() == "tcg" {
		domainType = "qemu"
	}
	return fmt.Sprintf(`<domain type='%s'><name>%s</name><memory unit='MiB'>2048</memory><vcpu>2</vcpu><cpu><topology sockets='1' cores='2' threads='1'/></cpu><features><acpi/><apic/></features><os><type arch='x86_64' machine='pc'>hvm</type>%s</os><on_poweroff>destroy</on_poweroff><on_reboot>destroy</on_reboot><on_crash>destroy</on_crash><devices>%s<emulator>/usr/bin/qemu-system-x86_64</emulator><disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='%s/disk.raw'/><target dev='vda' bus='virtio'/></disk><serial type='file'><source path='%s/%s.log'/><target port='0'/></serial></devices></domain>`, domainType, domain(r), boot, nic, dir, dir, stage)
}
func (p *Provider) waitReceipt(ctx context.Context, r *record, stage string) error {
	marker := "FLEET-INSTALLED:"
	if stage == "disk" {
		marker = "FLEET-DISK-BOOT:"
	}
	for {
		names, e := p.virsh(ctx, "list", "--all", "--name")
		if e != nil {
			return e
		}
		present := false
		for _, name := range strings.Fields(names) {
			if name == domain(r) {
				present = true
			}
		}
		if !present {
			b, e := os.ReadFile(p.directory(r) + "/" + stage + ".log")
			if e != nil {
				return e
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(line) == marker+r.Receipt {
					evidence, err := os.OpenFile(p.directory(r)+"/"+stage+".log", os.O_RDWR, 0)
					if err != nil {
						return err
					}
					err = evidence.Sync()
					closeErr := evidence.Close()
					if err != nil {
						return err
					}
					if closeErr != nil {
						return closeErr
					}
					return p.syncDir(p.directory(r))
				}
			}
			return errors.New("guest stopped without exact boot receipt")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (p *Provider) cleanup(ctx context.Context, j *journal, r *record) error {
	// Only retained operations in this allocation identify cleanup targets.
	names, e := p.virsh(ctx, "list", "--all", "--name")
	if e != nil {
		return e
	}
	for _, old := range j.Operations {
		if old.Request.Kind != "install" || old.Request.BindingID != r.Request.BindingID {
			continue
		}
		for _, n := range strings.Fields(names) {
			if n == domain(old) {
				return errors.New("domain still exists; cleanup remains unknown")
			}
		}
		path := p.directory(old) + "/disk.raw"
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
		dir, err := os.Open(p.directory(old))
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
		if _, e = os.Lstat(path); !os.IsNotExist(e) {
			return errors.New("disk deletion unverified")
		}
	}
	d, e := os.Open(p.stateRoot())
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

func (p *Provider) validate(q simulator.Request) error {
	if q.ID == "" || len(q.ID) > 256 || q.BindingID == "" || len(q.BindingID) > 256 || q.FenceToken == "" || len(q.FenceToken) > 1024 || q.Epoch < 1 || len(q.Inputs) > 4096 {
		return executor.ErrInvalid
	}
	if q.AssetID != AssetID || (q.Kind != "install" && q.Kind != "cleanup") {
		return executor.ErrInvalid
	}
	if q.Kind == "install" {
		var in struct {
			Image string `json:"image"`
		}
		d := json.NewDecoder(strings.NewReader(string(q.Inputs)))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF || in.Image != p.image {
			return executor.ErrInvalid
		}
	}
	if q.Kind == "cleanup" && len(q.Inputs) != 0 && strings.TrimSpace(string(q.Inputs)) != "{}" {
		return executor.ErrInvalid
	}
	return nil
}

// ImageReference is the locally verified artifact manifest identity.
func (p *Provider) ImageReference() string { return p.image }
