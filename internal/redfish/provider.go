package redfish

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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	"golang.org/x/sys/unix"
)

const (
	// Root holds the journal; the site mounts its journal subpath here.
	Root      = "/var/lib/fleet-local"
	artifacts = "/opt/fleet-local/artifacts"
	// LibvirtSocketDir is the hypervisor socket path of the libvirt mode. Its
	// presence in a Redfish worker is a configuration error, never a fallback.
	LibvirtSocketDir = "/run/fleet-libvirt"
	bootWindow       = 20 * time.Minute
	maxPowerOn       = 3
)

// Target is one enrolled physical asset: its BMC endpoint, its exact Redfish
// System, its boot NIC and, when the site's DHCP is synchronized, the
// provisioning attachment the network owner assigned to it. Console names the
// serial-console capture file for the target (a lab seam; see the operations
// guide). Enrollment is retained before effects and never inferred.
type Target struct {
	AssetID      string       `json:"assetID"`
	Endpoint     string       `json:"endpoint"`
	SystemID     string       `json:"systemID"`
	SystemUUID   string       `json:"systemUUID"`
	MAC          string       `json:"mac"`
	Provisioning Provisioning `json:"provisioning,omitzero"`
	DiskBytes    int64        `json:"diskBytes"`
	Console      string       `json:"console"`
}

// Provisioning is the asset's address on the provisioning network, in prefix
// form, and that network's gateway. Networking/IPAM allocates it; Fleet only
// publishes it while the asset's network boot is admitted.
type Provisioning struct {
	Address string `json:"address"`
	Gateway string `json:"gateway"`
}

// DHCP modes. Static: the site's DHCP is configured independently of Fleet.
// Synced: Fleet publishes boot reservations and a network boot requires
// evidence that DHCP applied the reservation and later withdrew it.
const (
	DHCPStatic = "static"
	DHCPSynced = "synced"
)

// Enrollment is the site's explicit asset to BMC mapping.
type Enrollment struct {
	DHCP    string   `json:"dhcp"`
	Targets []Target `json:"targets"`
}

func (e Enrollment) target(asset string) (Target, bool) {
	for _, t := range e.Targets {
		if t.AssetID == asset {
			return t, true
		}
	}
	return Target{}, false
}

func normalizeMAC(v string) (string, bool) {
	return bootnet.NormalizeMAC(strings.ReplaceAll(v, "-", ":"))
}

func ParseEnrollment(b []byte) (Enrollment, error) {
	var e Enrollment
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil || len(e.Targets) == 0 || (e.DHCP != DHCPStatic && e.DHCP != DHCPSynced) {
		return Enrollment{}, errors.New("invalid Redfish enrollment")
	}
	seen := map[string]bool{}
	for i, t := range e.Targets {
		mac, ok := normalizeMAC(t.MAC)
		endpoint, err := ParseEndpoint(t.Endpoint)
		// A Redfish System ID is unique only within its BMC.
		system := "s" + endpoint + " " + t.SystemID
		if !ok || err != nil || t.AssetID == "" || t.SystemID == "" || len(t.SystemUUID) != 36 || t.DiskBytes <= 0 || t.Console == "" || filepath.Base(t.Console) != t.Console ||
			seen["a"+t.AssetID] || seen[system] || seen["u"+strings.ToLower(t.SystemUUID)] || seen["m"+mac] {
			return Enrollment{}, errors.New("invalid or duplicate Redfish enrollment target")
		}
		seen["a"+t.AssetID], seen[system], seen["u"+strings.ToLower(t.SystemUUID)], seen["m"+mac] = true, true, true, true
		e.Targets[i].MAC, e.Targets[i].Endpoint = mac, endpoint
		if e.DHCP == DHCPStatic {
			if t.Provisioning != (Provisioning{}) {
				return Enrollment{}, errors.New("a static-DHCP enrollment carries no provisioning attachment")
			}
			continue
		}
		feed, err := bootnet.NewFeed([]bootnet.Reservation{{MAC: mac, Address: t.Provisioning.Address, Gateway: t.Provisioning.Gateway, Expires: time.Unix(1, 0)}})
		if err != nil {
			return Enrollment{}, errors.New("invalid provisioning attachment for enrolled target " + t.AssetID)
		}
		r := feed.Reservations[0]
		address := "p" + strings.SplitN(r.Address, "/", 2)[0]
		if seen[address] {
			return Enrollment{}, errors.New("duplicate provisioning address")
		}
		seen[address] = true
		e.Targets[i].Provisioning = Provisioning{Address: r.Address, Gateway: r.Gateway}
	}
	return e, nil
}

// phase is one boot of the target: a one-time boot override, power on, and an
// exact console receipt followed by power off.
type phase struct{ name, boot, marker, mode string }

var phases = map[string][]phase{
	"install": {{"installer", "Pxe", "FLEET-INSTALLED:", "install"}, {"disk", "Hdd", "FLEET-DISK-BOOT:", ""}},
	"cleanup": {{"wipe", "Pxe", "FLEET-WIPED:", "wipe"}},
}

type record struct {
	Request     simulator.Request         `json:"request"`
	Result      simulator.OperationResult `json:"result"`
	Receipt     string                    `json:"receipt"`
	Stage       string                    `json:"stage"`
	BootToken   string                    `json:"bootToken,omitempty"`
	BootExpires time.Time                 `json:"bootExpires"`
	BootClosed  bool                      `json:"bootClosed"`
	Target      Target                    `json:"target"`
	System      *System                   `json:"system,omitempty"`
	PowerOn     map[string]int            `json:"powerOnRequests,omitempty"`
	RequestedAt map[string]time.Time      `json:"requestedAt,omitempty"`
}

type journal struct {
	Provider   string             `json:"provider"`
	Image      string             `json:"image"`
	Epoch      int64              `json:"epoch"`
	Binding    string             `json:"binding"`
	Fence      string             `json:"fence"`
	Closed     bool               `json:"closed"`
	Operations map[string]*record `json:"operations"`
}

// Provider is the Redfish executor. The effectful provider runs only in a
// per-operation worker; read-only providers serve the boot and observer roles.
type Provider struct {
	root, bootRoot, consoleRoot, artifacts string
	origin, image                          string
	enrollment                             Enrollment
	clients                                map[string]*Client
	readOnly                               bool
	now                                    func() time.Time
	poll, settle, offGrace, outage         time.Duration
}

func (p *Provider) clock() time.Time {
	if p.now != nil {
		return p.now().UTC()
	}
	return time.Now().UTC()
}

// Open builds the effectful worker provider from its environment. It refuses
// any libvirt configuration: Redfish workers have no hypervisor fallback.
func Open() (*Provider, error) {
	if os.Getenv("FLEET_LIBVIRT_URI") != "" {
		return nil, errors.New("redfish provider refuses a libvirt URI")
	}
	if _, err := os.Stat(LibvirtSocketDir); err == nil {
		return nil, errors.New("redfish provider refuses a mounted libvirt socket")
	}
	p, err := open()
	if err != nil {
		return nil, err
	}
	enrollment, err := ParseEnrollment([]byte(os.Getenv("FLEET_REDFISH_ENROLLMENT")))
	if err != nil {
		return nil, err
	}
	credentials := os.Getenv("FLEET_REDFISH_CREDENTIALS")
	user, err1 := os.ReadFile(filepath.Join(credentials, "username"))
	password, err2 := os.ReadFile(filepath.Join(credentials, "password"))
	if credentials == "" || err1 != nil || err2 != nil {
		return nil, errors.New("redfish credentials are unavailable")
	}
	p.clients = map[string]*Client{}
	for _, t := range enrollment.Targets {
		if p.clients[t.Endpoint] != nil {
			continue
		}
		if p.clients[t.Endpoint], err = NewClient(t.Endpoint, strings.TrimSpace(string(user)), strings.TrimSpace(string(password)), 20*time.Second); err != nil {
			return nil, err
		}
	}
	p.consoleRoot = os.Getenv("FLEET_REDFISH_CONSOLE")
	if !filepath.IsAbs(p.consoleRoot) {
		return nil, errors.New("FLEET_REDFISH_CONSOLE must be an absolute path")
	}
	p.enrollment = enrollment
	return p, p.retainEnrollment()
}

// OpenReadOnly serves the boot service and observer. It holds no credential,
// cannot lock the journal and cannot execute.
func OpenReadOnly() (*Provider, error) {
	p, err := open()
	if err != nil {
		return nil, err
	}
	p.readOnly = true
	return p, nil
}

func open() (*Provider, error) {
	if os.Getenv("FLEET_SITE_PROVIDER") != "redfish" {
		return nil, errors.New("redfish provider requires FLEET_SITE_PROVIDER=redfish")
	}
	origin, err := ParseBootOrigin(os.Getenv("FLEET_BOOT_ORIGIN"))
	if err != nil {
		return nil, err
	}
	bootRoot := os.Getenv("FLEET_BOOT_EVIDENCE")
	if !filepath.IsAbs(bootRoot) || filepath.Clean(bootRoot) != bootRoot {
		return nil, errors.New("FLEET_BOOT_EVIDENCE must be a clean absolute path")
	}
	p := &Provider{root: Root, bootRoot: bootRoot, origin: origin, artifacts: artifacts}
	if p.image, err = verifyArtifacts(p.artifacts); err != nil {
		return nil, err
	}
	return p, nil
}

// ParseBootOrigin accepts only a literal http://IPv4:port origin.
func ParseBootOrigin(value string) (string, error) {
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

// verifyArtifacts derives the admitted image from the exact two-artifact manifest.
func verifyArtifacts(dir string) (string, error) {
	b, err := os.ReadFile(dir + "/SHA256SUMS")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	seen := map[string]bool{}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(lines) != 2 || len(f) != 2 || (f[1] != "kernel" && f[1] != "installer.gz") || seen[f[1]] {
			return "", errors.New("invalid artifact manifest")
		}
		seen[f[1]] = true
		if sum, err := fileSHA256(dir + "/" + f[1]); err != nil || sum != f[0] {
			return "", errors.New("artifact digest mismatch")
		}
	}
	sum := sha256.Sum256(b)
	return "fleet-local-linux@sha256:" + hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *Provider) lock() (*os.File, error) {
	f, err := os.OpenFile(p.root+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// retainEnrollment records the enrollment once; a changed enrollment for a
// retained journal is a re-mapping that requires an explicit migration.
func (p *Provider) retainEnrollment() error {
	// No journal lock: a worker that only opens must never contend with an
	// executing worker or a concurrently delivered one. The first writer
	// creates the record atomically (link of a synced file); every other
	// worker compares it.
	if err := os.MkdirAll(p.root, 0700); err != nil {
		return err
	}
	want, _ := json.Marshal(p.enrollment)
	path := p.root + "/enrollment.json"
	got, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		tmp, cerr := os.CreateTemp(p.root, ".enrollment-")
		if cerr != nil {
			return cerr
		}
		defer os.Remove(tmp.Name())
		_, werr := tmp.Write(want)
		if werr == nil {
			werr = tmp.Sync()
		}
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
		if lerr := os.Link(tmp.Name(), path); lerr == nil {
			return syncDir(p.root)
		} else if !os.IsExist(lerr) {
			return lerr
		}
		got, err = os.ReadFile(path)
	}
	if err != nil {
		return err
	}
	if string(got) != string(want) {
		return errors.New("redfish enrollment differs from the retained enrollment; re-mapping is not authorized")
	}
	return nil
}

var stages = func() map[string]bool {
	m := map[string]bool{"admitted": true, "discovered": true}
	for _, list := range phases {
		for _, ph := range list {
			for _, s := range []string{"armed", "requested", "started", "verified"} {
				m[ph.name+"-"+s] = true
			}
		}
	}
	return m
}()

func (p *Provider) read() (journal, error) {
	var j journal
	b, err := os.ReadFile(p.root + "/journal.json")
	if os.IsNotExist(err) {
		return journal{Provider: "redfish", Operations: map[string]*record{}}, nil
	}
	if err != nil {
		return j, err
	}
	if err = json.Unmarshal(b, &j); err != nil || j.Provider != "redfish" || j.Operations == nil {
		return j, errors.New("invalid redfish journal")
	}
	for id, r := range j.Operations {
		if r == nil || len(r.Receipt) != 64 {
			return j, errors.New("invalid retained operation")
		}
		if _, err := hex.DecodeString(r.Receipt); err != nil {
			return j, errors.New("invalid receipt")
		}
		if r.Stage == "done" {
			if r.Result.Phase != "Succeeded" || r.Result.CompletedAt == nil || (r.Request.Kind == "install" && r.Result.Result != "installed") || (r.Request.Kind == "cleanup" && r.Result.Result != "clean") {
				return j, errors.New("invalid terminal result")
			}
		} else if !stages[r.Stage] || r.Result.Phase != "Unknown" || r.Result.CompletedAt != nil {
			return j, errors.New("invalid pending result")
		}
		d, err := simulator.RequestDigest(r.Request)
		if err != nil || r.Request.ID != id || r.Result.ID != id || r.Result.Kind != r.Request.Kind || d != r.Result.InputDigest || r.Target.AssetID != r.Request.AssetID {
			return j, errors.New("invalid retained request")
		}
	}
	return j, nil
}

func (p *Provider) save(j journal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p.root+"/journal.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(p.root+"/journal.tmp", p.root+"/journal.json"); err != nil {
		return err
	}
	return syncDir(p.root)
}

func key(id string) string { s := sha256.Sum256([]byte(id)); return hex.EncodeToString(s[:]) }

// Lookup reports only terminal retained results; a retained unfinished
// operation is not found with Unknown, which authorizes exact replay.
func (p *Provider) Lookup(ctx context.Context, id string) (simulator.OperationResult, bool, error) {
	if !p.readOnly {
		f, err := p.lock()
		if err != nil {
			return simulator.OperationResult{}, false, err
		}
		defer f.Close()
	}
	j, err := p.read()
	if err != nil {
		return simulator.OperationResult{}, false, err
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

func (p *Provider) validate(q simulator.Request) (Target, error) {
	if q.ID == "" || len(q.ID) > 256 || q.BindingID == "" || len(q.BindingID) > 256 || q.FenceToken == "" || len(q.FenceToken) > 1024 || q.Epoch < 1 || len(q.Inputs) > 4096 {
		return Target{}, executor.ErrInvalid
	}
	t, ok := p.enrollment.target(q.AssetID)
	if !ok || (q.Kind != "install" && q.Kind != "cleanup") {
		return Target{}, executor.ErrInvalid
	}
	if q.Kind == "install" {
		var in struct {
			Image string `json:"image"`
		}
		d := json.NewDecoder(strings.NewReader(string(q.Inputs)))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF || in.Image != p.image {
			return Target{}, executor.ErrInvalid
		}
	}
	if q.Kind == "cleanup" && len(q.Inputs) != 0 && strings.TrimSpace(string(q.Inputs)) != "{}" {
		return Target{}, executor.ErrInvalid
	}
	return t, nil
}

// Execute runs or resumes exactly one retained operation. The exact request,
// enrolled target, receipt and boot capability are durable before any
// Redfish request; replay under the same ID never repeats an effect that may
// have happened. HTTP success is never success: only observed power state and
// the exact console receipt complete a phase.
func (p *Provider) Execute(ctx context.Context, q simulator.Request) (simulator.OperationResult, error) {
	if p.readOnly || len(p.clients) == 0 {
		return simulator.OperationResult{}, executor.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	f, err := p.lock()
	if err != nil {
		return simulator.OperationResult{}, err
	}
	defer f.Close()
	target, err := p.validate(q)
	if err != nil {
		return simulator.OperationResult{}, err
	}
	digest, err := simulator.RequestDigest(q)
	if err != nil {
		return simulator.OperationResult{}, err
	}
	j, err := p.read()
	if err != nil {
		return simulator.OperationResult{}, err
	}
	if j.Image != "" && j.Image != p.image {
		return simulator.OperationResult{}, executor.ErrConflict
	}
	j.Image = p.image
	r := j.Operations[q.ID]
	if r != nil {
		if r.Result.InputDigest != digest || r.Target != target {
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
		j.Epoch, j.Binding, j.Fence, j.Closed = q.Epoch, q.BindingID, q.FenceToken, false
		receipt, token := make([]byte, 32), make([]byte, 32)
		if _, err = rand.Read(receipt); err == nil {
			_, err = rand.Read(token)
		}
		if err != nil {
			return simulator.OperationResult{}, err
		}
		r = &record{Request: q, Receipt: hex.EncodeToString(receipt), Stage: "admitted", Target: target,
			BootToken: hex.EncodeToString(token), BootExpires: p.clock().Add(bootWindow), PowerOn: map[string]int{}, RequestedAt: map[string]time.Time{},
			Result: simulator.OperationResult{ID: q.ID, Kind: q.Kind, InputDigest: digest, Phase: "Unknown", CreatedAt: p.clock()}}
		j.Operations[q.ID] = r
		if err = p.save(j); err != nil {
			return simulator.OperationResult{}, err
		}
	}
	if err = p.run(ctx, &j, r); err != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: %v", executor.ErrOutcomeUnknown, err)
	}
	now := p.clock()
	r.Stage, r.Result.Phase, r.Result.CompletedAt, r.Result.Result = "done", "Succeeded", &now, "installed"
	if q.Kind == "cleanup" {
		r.Result.Result = "clean"
		j.Closed = true
	}
	if err = p.save(j); err != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: %v", executor.ErrOutcomeUnknown, err)
	}
	return r.Result, nil
}

func (p *Provider) durations() (poll, settle, offGrace, outage time.Duration) {
	poll, settle, offGrace, outage = p.poll, p.settle, p.offGrace, p.outage
	if poll == 0 {
		poll = 2 * time.Second
	}
	if settle == 0 {
		settle = 45 * time.Second
	}
	if offGrace == 0 {
		offGrace = 30 * time.Second
	}
	if outage == 0 {
		outage = 5 * time.Minute
	}
	return
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (p *Provider) run(ctx context.Context, j *journal, r *record) error {
	poll, _, _, _ := p.durations()
	if r.Stage == "admitted" {
		// GETs only: a rejected credential, a missing capability or a
		// mismatched identity leaves the operation admitted with no effect.
		sys, err := p.bmc(r).Discover(ctx, r.Target)
		if err != nil {
			return fmt.Errorf("discovery: %w", err)
		}
		r.System, r.Stage = &sys, "discovered"
		if err := p.save(*j); err != nil {
			return err
		}
	}
	if r.System == nil {
		return errors.New("retained operation lacks its discovered System")
	}
	before := "discovered"
	for _, ph := range phases[r.Request.Kind] {
		if r.Stage == before {
			// Pre-boot: power off and arm the one-time override. Both are
			// idempotent here because no boot of this phase was requested.
			if err := p.powerOff(ctx, r); err != nil {
				return err
			}
			if ph.mode != "" && (r.BootClosed || !p.clock().Before(r.BootExpires)) {
				return errors.New("boot admission expired or closed before network boot")
			}
			if ph.mode != "" && p.enrollment.DHCP == DHCPSynced {
				if err := p.awaitReservation(ctx, r); err != nil {
					return err
				}
			}
			if err := p.bmc(r).SetBootOnce(ctx, *r.System, ph.boot); err != nil {
				return fmt.Errorf("arm %s boot: %w", ph.boot, err)
			}
			r.Stage = ph.name + "-armed"
			if err := p.save(*j); err != nil {
				return err
			}
		}
		if r.Stage == ph.name+"-armed" {
			if err := p.requestPowerOn(ctx, j, r, ph); err != nil {
				return err
			}
		}
		for r.Stage == ph.name+"-requested" {
			started, err := p.started(ctx, j, r, ph)
			if err != nil {
				return err
			}
			if started {
				r.Stage = ph.name + "-started"
				if err := p.save(*j); err != nil {
					return err
				}
				break
			}
			if err := sleep(ctx, poll); err != nil {
				return err
			}
		}
		if r.Stage == ph.name+"-started" {
			if err := p.complete(ctx, r, ph); err != nil {
				return err
			}
			if ph.mode != "" {
				r.BootClosed = true
			}
			r.Stage = ph.name + "-verified"
			if err := p.save(*j); err != nil {
				return err
			}
		}
		before = ph.name + "-verified"
	}
	if r.Stage != before {
		return errors.New("unexpected operation stage " + r.Stage)
	}
	if p.enrollment.DHCP == DHCPSynced {
		return p.awaitWithdrawal(ctx, r)
	}
	return nil
}

// bmc is the client of the target's own enrolled BMC.
func (p *Provider) bmc(r *record) *Client { return p.clients[r.Target.Endpoint] }

func (p *Provider) powerOff(ctx context.Context, r *record) error {
	sys := *r.System
	poll, _, _, _ := p.durations()
	deadline := p.clock().Add(5 * time.Minute)
	requested := false
	for {
		pw, err := p.bmc(r).Observe(ctx, sys)
		if err != nil {
			return fmt.Errorf("observe before boot: %w", err)
		}
		if pw.State == "Off" {
			return nil
		}
		if !requested {
			// A slow or lost ForceOff response is observed, not trusted:
			// power-off before this phase's boot is idempotent.
			if err := p.bmc(r).Reset(ctx, sys, "ForceOff"); errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrUnsupported) {
				return fmt.Errorf("power off: %w", err)
			}
			requested = true
		}
		if p.clock().After(deadline) {
			return errors.New("target did not power off")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// requestPowerOn retains the request, then posts On. A lost or failed
// response leaves the phase requested; recovery observes before any repeat.
func (p *Provider) requestPowerOn(ctx context.Context, j *journal, r *record, ph phase) error {
	if r.PowerOn == nil {
		r.PowerOn, r.RequestedAt = map[string]int{}, map[string]time.Time{}
	}
	if r.PowerOn[ph.name] >= maxPowerOn {
		return errors.New("power-on request budget exhausted; outcome requires inspection")
	}
	r.PowerOn[ph.name]++
	r.RequestedAt[ph.name] = p.clock()
	r.Stage = ph.name + "-requested"
	if err := p.save(*j); err != nil {
		return err
	}
	if err := p.bmc(r).Reset(ctx, *r.System, "On"); err != nil {
		return fmt.Errorf("power on (%s) response: %w", ph.name, err)
	}
	return nil
}

// started reports whether a requested boot may have begun. Any sign of a boot
// (power not Off, the one-time override consumed, a boot delivery or receipt)
// means it may have run, and it is never requested again. Only a target that
// is still Off, still armed and silent after the settle window gets a repeated
// On, within a small retained budget.
func (p *Provider) started(ctx context.Context, j *journal, r *record, ph phase) (bool, error) {
	_, settle, _, _ := p.durations()
	pw, err := p.bmc(r).Observe(ctx, *r.System)
	if err != nil {
		if errors.Is(err, ErrIdentity) {
			return false, err
		}
		return false, fmt.Errorf("observe requested boot: %w", err)
	}
	seen, err := p.receipts(r, ph)
	if err != nil {
		return false, err
	}
	delivered := ph.mode != "" && p.deliveryStarted(r)
	if pw.State != "Off" || pw.OverrideEnabled != "Once" || pw.OverrideTarget != ph.boot || seen > 0 || delivered {
		return true, nil
	}
	if p.clock().Sub(r.RequestedAt[ph.name]) < settle {
		return false, nil
	}
	return false, p.requestPowerOn(ctx, j, r, ph)
}

// complete waits for exactly one console receipt for this operation and the
// target powered off. BMC outages are tolerated for a bounded period.
func (p *Provider) complete(ctx context.Context, r *record, ph phase) error {
	poll, _, offGrace, outage := p.durations()
	var offSince, failingSince time.Time
	for {
		pw, err := p.bmc(r).Observe(ctx, *r.System)
		switch {
		case errors.Is(err, ErrIdentity):
			return err
		case err != nil:
			if failingSince.IsZero() {
				failingSince = p.clock()
			}
			if p.clock().Sub(failingSince) > outage {
				return fmt.Errorf("BMC unavailable while observing %s: %w", ph.name, err)
			}
		default:
			failingSince = time.Time{}
			seen, err := p.receipts(r, ph)
			if err != nil {
				return err
			}
			if pw.State == "Off" {
				if seen == 1 {
					if ph.mode != "" {
						return p.verifyDelivery(r)
					}
					return nil
				}
				if offSince.IsZero() {
					offSince = p.clock()
				}
				if p.clock().Sub(offSince) > offGrace {
					return fmt.Errorf("target powered off without the exact %s receipt", ph.name)
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

// receipts counts exact receipt lines for this operation on the target's
// console. More than one is a duplicate execution and never succeeds.
func (p *Provider) receipts(r *record, ph phase) (int, error) {
	b, err := os.ReadFile(filepath.Join(p.consoleRoot, r.Target.Console))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	want := ph.marker + r.Receipt
	if ph.name == "wipe" {
		want += ":" + strconv.FormatInt(r.Target.DiskBytes, 10)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == want {
			n++
		}
	}
	if ph.name == "disk" {
		installed, _ := p.receipts(r, phases["install"][0])
		if installed != 1 {
			return 0, errors.New("disk receipt without exactly one installer receipt")
		}
	}
	if n > 1 {
		return 0, fmt.Errorf("duplicate %s receipt: the phase ran more than once", ph.name)
	}
	return n, nil
}

func (p *Provider) bootDirectory(r *record) string {
	return filepath.Join(p.bootRoot, key(r.Request.ID))
}

func (p *Provider) deliveries(r *record) map[string]bool {
	b, _ := os.ReadFile(filepath.Join(p.bootDirectory(r), "boot-delivery.log"))
	done := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && (f[1] == "write-complete" || f[1] == "admitted") && strings.HasPrefix(f[3], "bytes=") {
			done[f[1]+" "+f[2]] = true
		}
	}
	return done
}

func (p *Provider) deliveryStarted(r *record) bool { return p.deliveries(r)["admitted boot.ipxe"] }

func (p *Provider) verifyDelivery(r *record) error {
	d := p.deliveries(r)
	for _, name := range []string{"boot.ipxe", "kernel", "installer.gz"} {
		if !d["write-complete "+name] {
			return fmt.Errorf("missing network delivery: %s", name)
		}
	}
	return nil
}

// ImageReference is the locally verified artifact manifest identity.
func (p *Provider) ImageReference() string { return p.image }
