package redfishwork

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
)

// Virtual media delivery: the BMC mounts a boot medium and a config medium
// from the central media service and boots from them once. The installed OS
// posts its install report to the media service at first boot. The worker
// opens the operation's window under the Device's fence, which the media
// service checks against custody, and proves before the operation ends that
// no medium, override, window or transfer remains. The site store keeps
// only digests, except the rendered config medium and its URL capability,
// which it deletes when the window closes.

const (
	// mediaRecords holds each operation's window digests at the site.
	mediaRecords = "media"
	// mediaConfigs holds each operation's rendered config medium and URL
	// capability until its window closes.
	mediaConfigs = "mediacfg"
	// configMediumSize is the config medium's size: four CD sectors.
	configMediumSize = 8192
	// mediaBootTarget is the Redfish boot override target for virtual
	// media.
	mediaBootTarget = "Cd"
)

// mediaWindow is the site's record of one operation's window: what the
// installer must report and how the BMC's readback must look, as digests.
type mediaWindow struct {
	Operation  string `json:"operation"`
	Fence      int64  `json:"fence"`
	BootDigest string `json:"bootDigest"`
	// TokenSHA256 is the digest of the report token on the config medium.
	TokenSHA256 string `json:"tokenSHA256"`
	// Image is the content digest the boot medium must report: its
	// manifest of the kernel and initrd. Bootstrap is the salted digest the
	// installer must report for the payload it read back from disk.
	Image     string `json:"image"`
	Bootstrap string `json:"bootstrap"`
	// BootURLSHA256 and ConfigURLSHA256 compare the BMC's readback of each
	// device's URL without recording the URLs.
	BootURLSHA256   string    `json:"bootURLSHA256"`
	ConfigURLSHA256 string    `json:"configURLSHA256"`
	FetchExpires    time.Time `json:"fetchExpires"`
	ReportExpires   time.Time `json:"reportExpires"`
}

// configMedium is a rendered config medium and its URL capability.
type configMedium struct {
	Data       []byte `json:"data"`
	Capability string `json:"capability"`
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// mediaPlan is what the worker derives from the admitted inputs and the
// pinned artifacts before it sends anything.
type mediaPlan struct {
	origin     string
	bootDigest string
	// image is the digest of the boot medium's content manifest.
	image     string
	fetch     time.Duration
	report    time.Duration
	downloads []ops.DownloadInput
	addresses ops.AddressMode
	// reportAddress is the Machine's IPAM address, the only source the
	// media service takes this operation's report from.
	reportAddress string
}

// planMedia checks the admitted inputs against the site's pinned artifacts
// and reads the boot medium's digest from their manifest. Nothing after
// admission can change which medium boots or where it comes from.
func (c Config) planMedia(spec ops.DeviceInstallationSpec) (mediaPlan, error) {
	delivery := spec.Delivery
	origin, err := redfish.ParseEndpoint(delivery.MediaService)
	if err != nil || !strings.HasPrefix(origin, "https://") || delivery.FetchWindow == nil || delivery.ReportWindow == nil || delivery.FetchWindow.Duration <= 0 || delivery.ReportWindow.Duration <= 0 {
		return mediaPlan{}, errors.New("the virtual media inputs name no https media service or phase windows")
	}
	identity, err := redfish.VerifyArtifacts(c.Artifacts)
	if err != nil || identity != spec.Image.Name+"@"+spec.Image.Digest {
		return mediaPlan{}, errors.New("the site's artifacts are not the admitted image")
	}
	manifest, err := os.ReadFile(filepath.Join(c.Artifacts, "SHA256SUMS"))
	if err != nil {
		return mediaPlan{}, err
	}
	plan := mediaPlan{origin: origin, fetch: delivery.FetchWindow.Duration, report: delivery.ReportWindow.Duration, downloads: spec.Image.Downloads, addresses: delivery.Addresses}
	if delivery.ReportAddress == nil {
		return mediaPlan{}, errors.New("the installation records no address for its report")
	}
	address, err := netip.ParseAddr(delivery.ReportAddress.Address)
	if err != nil || address.Zone() != "" || !slices.ContainsFunc(c.ReportFrom, func(prefix netip.Prefix) bool { return prefix.Contains(address) }) {
		return mediaPlan{}, errors.New("the installation's report address is not inside this site's report ranges")
	}
	plan.reportAddress = address.String()
	var content strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if fields[1] == "boot.iso" {
			plan.bootDigest = fields[0]
		} else {
			content.WriteString(line + "\n")
		}
	}
	plan.image = "sha256:" + digest(content.String())
	return plan, nil
}

// checkMedia refuses, before anything is sent, a virtual media installation
// this site cannot deliver.
func (c Config) checkMedia(ctx context.Context, work *site.Work, spec ops.DeviceInstallationSpec) *custody.Refusal {
	switch {
	case len(c.MediaCA) == 0:
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "this site has no trust for a media service"}
	case len(c.ReportFrom) == 0:
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "this site declares no address its installed servers report from"}
	case spec.BootNetwork != nil || spec.Delivery.Addresses != ops.AddressesDataNetwork:
		return &custody.Refusal{Reason: custody.ReasonDeliveryInputs, Message: "a virtual media installation carries no boot network and the DataNetwork address mode"}
	}
	if _, err := c.planMedia(spec); err != nil {
		return &custody.Refusal{Reason: ReasonInputs, Message: err.Error()}
	}
	// Only virtual media reads the site worker's media service identity.
	if _, err := conn(work).MediaClient(ctx); err != nil {
		return &custody.Refusal{Reason: site.ReasonCredentialMissing, Message: "the site worker's media service client certificate is unreadable"}
	}
	if refusal := checkTarget(work, spec.TargetDisks, mediaPhases); refusal != nil {
		return refusal
	}
	devices, err := conn(work).VirtualMedia(ctx)
	if err != nil {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the BMC's virtual media could not be read"}
	}
	if _, _, ok := mediaDevices(devices); !ok {
		return &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: "the BMC offers no two CD devices with insert and eject"}
	}
	return nil
}

// mediaPhases are the boots of a virtual media installation, for the
// capability checks: the installer from media, then the installed disk.
var mediaPhases = []phase{{"media-installer", mediaBootTarget, "", "install"}, {"disk", "Hdd", "", ""}}

// mediaDevices picks the boot and config devices: the first two, by Id,
// that accept a CD and offer both actions.
func mediaDevices(devices []redfish.Media) (boot, config redfish.Media, ok bool) {
	var usable []redfish.Media
	for _, device := range devices {
		if slices.Contains(device.MediaTypes, "CD") && device.CanInsert() && device.CanEject() {
			usable = append(usable, device)
		}
	}
	slices.SortFunc(usable, func(a, b redfish.Media) int { return strings.Compare(a.ID, b.ID) })
	if len(usable) < 2 {
		return redfish.Media{}, redfish.Media{}, false
	}
	return usable[0], usable[1], true
}

// renderConfig renders the config medium in the format the lab image reads:
// a text block padded with zeros. It carries the report token and salt, how
// the OS gets its addresses, the pinned downloads, and where the installed
// OS posts its report with the certificate it must verify there.
func renderConfig(token, salt string, bootstrap []byte, plan mediaPlan, reportURL string, reportCA []byte) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "FLEET-CONFIG-V1\ntoken=%s\nsalt=%s\naddresses=%s\nreportaddress=%s\nbootstrap=%s\nreport=%s\nreportca=%s\n", token, salt, plan.addresses,
		plan.reportAddress, base64.StdEncoding.EncodeToString(bootstrap), reportURL, base64.StdEncoding.EncodeToString(reportCA))
	for _, download := range plan.downloads {
		fmt.Fprintf(&b, "download=%s %s\n", download.Name, download.Reference)
	}
	b.WriteString("end\n")
	if b.Len() > configMediumSize {
		return nil, errors.New("the config medium does not fit")
	}
	data := make([]byte, configMediumSize)
	copy(data, b.String())
	return data, nil
}

// SaltedDigest is the digest the installer reports for the bootstrap
// payload it read back: SHA-256 over the salt, a newline, and the payload.
func SaltedDigest(salt string, payload []byte) string {
	sum := sha256.Sum256(append([]byte(salt+"\n"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// BootMediumURL is where the media service serves the boot medium through
// one operation's window.
func BootMediumURL(origin, key, bootDigest string) string {
	return origin + "/media/" + key + "/boot/" + bootDigest + "/boot.iso"
}

// ConfigMediumURL is where the media service serves one operation's config
// medium. The capability is the only secret in it.
func ConfigMediumURL(origin, key, capability string) string {
	return origin + "/media/" + key + "/config/" + capability + "/config.img"
}

// ReportURL is where the installed OS posts its install report.
func ReportURL(origin, key string) string { return origin + "/report/" + key }

// mediaClient is the worker's client of the central media service. It
// verifies the service against the site's trust, never follows redirects
// and uses no environment proxy.
type mediaClient struct {
	origin string
	http   *http.Client
}

func (c Config) mediaClient(origin string, identity *tls.Certificate) (*mediaClient, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.MediaCA) {
		return nil, errors.New("the site's media service trust is not a PEM certificate")
	}
	config := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	// The site worker's client certificate is its identity at the media
	// service. Without one, the service refuses every window.
	if identity != nil {
		config.Certificates = []tls.Certificate{*identity}
	}
	transport := &http.Transport{TLSClientConfig: config}
	return &mediaClient{origin: origin, http: &http.Client{Timeout: 30 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (m *mediaClient) do(ctx context.Context, method, path string, body, into any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, m.origin+path, reader)
	if err != nil {
		return 0, err
	}
	response, err := m.http.Do(request)
	if err != nil {
		return 0, errors.New("the media service could not be reached")
	}
	defer response.Body.Close()
	if into != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(into); err != nil {
			return response.StatusCode, errors.New("the media service's answer is unreadable")
		}
	}
	return response.StatusCode, nil
}

func (m *mediaClient) open(ctx context.Context, key string, request OpenRequest) error {
	code, err := m.do(ctx, http.MethodPut, "/v1/windows/"+key, request, nil)
	switch {
	case err != nil:
		return err
	case code == http.StatusConflict:
		return ErrMediaStale
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return ErrMediaUnauthorized
	case code != http.StatusOK && code != http.StatusCreated:
		return fmt.Errorf("the media service refused the window: HTTP %d", code)
	}
	return nil
}

func (m *mediaClient) close(ctx context.Context, key string) error {
	code, err := m.do(ctx, http.MethodPost, "/v1/windows/"+key+"/close", nil, nil)
	if err == nil && code != http.StatusOK && code != http.StatusNotFound {
		err = fmt.Errorf("the media service did not close the window: HTTP %d", code)
	}
	return err
}

func (m *mediaClient) window(ctx context.Context, key string) (WindowStatus, error) {
	var status WindowStatus
	code, err := m.do(ctx, http.MethodGet, "/v1/windows/"+key, nil, &status)
	if err == nil && code != http.StatusOK {
		err = fmt.Errorf("the media service does not report the window: HTTP %d", code)
	}
	return status, err
}

func (m *mediaClient) device(ctx context.Context, device string) (DeviceStatus, error) {
	var status DeviceStatus
	code, err := m.do(ctx, http.MethodGet, "/v1/devices/"+device, nil, &status)
	if err == nil && code != http.StatusOK {
		err = fmt.Errorf("the media service does not report the Device: HTTP %d", code)
	}
	return status, err
}

// window opens the operation's media window once. It renders the config
// medium with a fresh report token and salt and stores it, with the
// window's digests, under the stored fence before anything is sent. The
// open itself is a journaled step; the media service opens it only while
// custody names the operation active at this fence, and answers a replay of
// the same window again.
func (c Config) window(ctx context.Context, work *site.Work, media *mediaClient, plan mediaPlan) (mediaWindow, error) {
	store := work.Store()
	key := Key(work.Operation.UID)
	var window mediaWindow
	var medium configMedium
	found, err := store.GetRecord(work.Device, mediaRecords, work.Operation.UID, &window)
	if err != nil {
		return window, err
	}
	if !found {
		capability, err := random()
		if err != nil {
			return window, err
		}
		token, err := random()
		if err != nil {
			return window, err
		}
		salt, err := random()
		if err != nil {
			return window, err
		}
		// The sealed store is not on this branch, so admission refuses
		// bootstrap data and the payload is always empty.
		var payload []byte
		data, err := renderConfig(token, salt, payload, plan, ReportURL(c.reportOrigin(plan), key), c.MediaCA)
		if err != nil {
			return window, err
		}
		now := c.now()
		window = mediaWindow{Operation: work.Operation.UID, Fence: work.Fence, BootDigest: plan.bootDigest, TokenSHA256: digest(token), Image: plan.image, Bootstrap: SaltedDigest(salt, payload),
			BootURLSHA256: digest(BootMediumURL(plan.origin, key, plan.bootDigest)), ConfigURLSHA256: digest(ConfigMediumURL(plan.origin, key, capability)),
			FetchExpires: minTime(now.Add(plan.fetch), work.Deadline), ReportExpires: minTime(now.Add(plan.fetch+plan.report), work.Deadline)}
		medium = configMedium{Data: data, Capability: capability}
		if err := store.PutRecord(work.Device, work.Fence, mediaConfigs, work.Operation.UID, medium); err != nil {
			return window, err
		}
		if err := store.PutRecord(work.Device, work.Fence, mediaRecords, work.Operation.UID, window); err != nil {
			return window, err
		}
	} else if found, err := store.GetRecord(work.Device, mediaConfigs, work.Operation.UID, &medium); err != nil {
		return window, err
	} else if !found && !work.Journaled("media-window") {
		return window, errors.New("the config medium is gone before its window opened")
	}
	open := func(ctx context.Context) error {
		return media.open(ctx, key, OpenRequest{Device: work.Device, Operation: work.Operation.UID, Fence: work.Fence, BootDigest: window.BootDigest,
			CapabilitySHA256: digest(medium.Capability), TokenSHA256: window.TokenSHA256, ConfigMedium: medium.Data,
			FetchExpires: window.FetchExpires, ReportExpires: window.ReportExpires, ReportFrom: c.reportFrom(), ReportAddress: plan.reportAddress})
	}
	if err := work.Step(ctx, site.Step{Name: "media-window", Send: open, Idempotent: true}); err != nil {
		return mediaWindow{}, err
	}
	return window, nil
}

func (c Config) runMedia(ctx context.Context, work *site.Work, spec ops.DeviceInstallationSpec) (steps []ops.StepEvidence, err error) {
	plan, err := c.planMedia(spec)
	if err != nil {
		return nil, &custody.Refusal{Reason: ReasonInputs, Message: err.Error()}
	}
	identity, err := conn(work).MediaClient(ctx)
	if err != nil {
		return nil, &custody.Refusal{Reason: site.ReasonCredentialMissing, Message: "the site worker's media service client certificate is unreadable"}
	}
	media, err := c.mediaClient(plan.origin, identity)
	if err != nil {
		return nil, &custody.Refusal{Reason: ReasonUnsupportedTarget, Message: err.Error()}
	}
	window, err := c.window(ctx, work, media, plan)
	if errors.Is(err, ErrMediaStale) || errors.Is(err, ErrMediaUnauthorized) {
		// The media service refused the window outright, so it holds no
		// window. If the journal shows no BMC step either, nothing reached
		// the server and the operation fails; any BMC step leaves it
		// unknown.
		if !bmcStepJournaled(work) {
			return nil, &site.Unsent{Refusal: &custody.Refusal{Reason: custody.ReasonMediaWindowRefused, Message: err.Error()}}
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		// Every path into an unknown outcome tries to undo delivery: close
		// the windows, delete the config medium and eject what is
		// inserted. That attempt is never release proof. A worker stopped
		// from outside leaves delivery for its replacement, and one a newer
		// fence stopped cannot send anything.
		if err != nil && (ctx.Err() == nil || site.DeadlinePassed(ctx)) {
			undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			c.undoMedia(undo, work, media)
		}
	}()
	if at, done := work.JournaledAt("media-installer-done"); done {
		steps = append(steps, ops.StepEvidence{Name: "media-installer", CompletedAt: metaNow(at)})
	} else {
		if err := c.prepareMedia(ctx, work, media, window, plan.origin); err != nil {
			return nil, err
		}
		// The installer reports nothing itself: it powers the server off.
		// What it installed is proven only by the installed OS's report.
		if err := c.awaitOff(ctx, work, mediaBootTarget, window.ReportExpires); err != nil {
			return nil, err
		}
		if err := work.Step(ctx, site.Step{Name: "media-installer-done", Send: func(context.Context) error { return nil }, Idempotent: true}); err != nil {
			return nil, err
		}
		steps = append(steps, ops.StepEvidence{Name: "media-installer", CompletedAt: metaNow(c.now())})
	}
	if at, done := work.JournaledAt("media-report-verified"); done {
		steps = append(steps, ops.StepEvidence{Name: "first-boot-report", CompletedAt: metaNow(at)})
	} else {
		if err := c.bootReport(ctx, work, media, window, plan.downloads); err != nil {
			return nil, err
		}
		steps = append(steps, ops.StepEvidence{Name: "first-boot-report", CompletedAt: metaNow(c.now())})
	}
	if err := c.releaseMedia(ctx, work, media); err != nil {
		return nil, err
	}
	return append(steps, ops.StepEvidence{Name: "media-release", CompletedAt: metaNow(c.now())}), nil
}

// insertPayload is the journaled payload of an insert or eject: which
// device, and the digest of the URL, never the URL.
type insertPayload struct {
	Device    string `json:"device"`
	URLSHA256 string `json:"urlSHA256"`
}

// prepareMedia ejects anything already inserted and reads every device back
// empty, inserts both media and reads both URLs back, then arms a one-time
// boot from media and powers the server on once.
func (c Config) prepareMedia(ctx context.Context, work *site.Work, media *mediaClient, window mediaWindow, origin string) error {
	if !work.Journaled("media-insert-boot") {
		if err := c.ejectAll(ctx, work, "media-clear-"); err != nil {
			return err
		}
	}
	devices, err := conn(work).VirtualMedia(ctx)
	if err != nil {
		return err
	}
	boot, config, ok := mediaDevices(devices)
	if !ok {
		return errors.New("the BMC no longer offers two CD devices")
	}
	key := Key(work.Operation.UID)
	inserts := []struct {
		name   string
		device redfish.Media
		url    func() (string, error)
		sha    string
	}{
		{"media-insert-boot", boot, func() (string, error) { return BootMediumURL(origin, key, window.BootDigest), nil }, window.BootURLSHA256},
		{"media-insert-config", config, func() (string, error) {
			var medium configMedium
			if found, err := work.Store().GetRecord(work.Device, mediaConfigs, work.Operation.UID, &medium); err != nil || !found {
				return "", errors.New("the config medium is gone: its window closed")
			}
			return ConfigMediumURL(origin, key, medium.Capability), nil
		}, window.ConfigURLSHA256},
	}
	for _, insert := range inserts {
		device, sha := insert.device, insert.sha
		readback := func(ctx context.Context) (bool, error) {
			now, err := conn(work).VirtualMedia(ctx)
			if err != nil {
				return false, err
			}
			for _, m := range now {
				if m.ID == device.ID {
					return m.Inserted && digest(m.Image) == sha, nil
				}
			}
			return false, nil
		}
		if err := work.Step(ctx, site.Step{
			Name:    insert.name,
			Payload: insertPayload{Device: device.ID, URLSHA256: sha},
			Send: func(ctx context.Context) error {
				url, err := insert.url()
				if err != nil {
					return err
				}
				return conn(work).InsertMedia(ctx, device, url)
			},
			Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) { return readback(ctx) },
		}); err != nil {
			return err
		}
		// HTTP success is not the effect: the BMC must report the URL.
		if inserted, err := readback(ctx); err != nil {
			return err
		} else if !inserted {
			return fmt.Errorf("the BMC does not report the %s medium inserted", strings.TrimPrefix(insert.name, "media-insert-"))
		}
	}
	requested := work.Journaled("media-installer-power-on")
	if err := work.Step(ctx, site.Step{Name: "media-installer-power-off", Send: func(ctx context.Context) error { return c.powerOff(ctx, work) }, Idempotent: true}); err != nil {
		return err
	}
	if !requested && !c.now().Before(window.FetchExpires) {
		// The BMC must still be able to fetch when it boots.
		return errors.New("the media fetch window ended before the boot")
	}
	if err := work.Step(ctx, site.Step{Name: "media-installer-arm", Send: func(ctx context.Context) error { return conn(work).SetBootOnce(ctx, mediaBootTarget) }, Idempotent: true}); err != nil {
		return err
	}
	// Power on is sent once. After a lost response any sign that the boot
	// began counts, and no sign leaves the outcome unknown.
	return work.Step(ctx, site.Step{
		Name:    "media-installer-power-on",
		Payload: powerOn{RequestedAt: c.now().UTC()},
		Send:    func(ctx context.Context) error { return conn(work).Reset(ctx, "On") },
		Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) {
			return c.bootStarted(ctx, work, mediaBootTarget, media, key)
		},
	})
}

// bootStarted reports whether a requested boot may have begun: power not
// off, the override consumed, or a report taken.
func (c Config) bootStarted(ctx context.Context, work *site.Work, target string, media *mediaClient, key string) (bool, error) {
	power, err := conn(work).Observe(ctx)
	if err != nil {
		return false, err
	}
	status, err := media.window(ctx, key)
	if err != nil {
		return false, err
	}
	return power.State != "Off" || power.OverrideEnabled != "Once" || power.OverrideTarget != target || status.Report != nil, nil
}

// awaitOff waits, before the deadline, until the boot requested from target
// began and the server is off again. A BMC may report Off, or the armed
// override, for a while after it accepted the power-on, so Off counts only
// once the server was seen on or the override consumed. The BMC may be
// unreadable for a bounded time.
func (c Config) awaitOff(ctx context.Context, work *site.Work, target string, until time.Time) error {
	poll, _, outage, _ := c.durations()
	var failingSince time.Time
	began := false
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
				return fmt.Errorf("the BMC was unreadable while the installer ran: %w", err)
			}
		default:
			failingSince = time.Time{}
			began = began || power.State != "Off" || power.OverrideEnabled != "Once" || power.OverrideTarget != target
			if began && power.State == "Off" {
				return nil
			}
		}
		if !c.now().Before(until) {
			return errors.New("the installer did not finish before the report deadline")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// bootReport boots the installed disk once and waits for its first-boot
// report at the media service: exactly one report with this operation's
// token, carrying its image and salted bootstrap digests, before the report
// deadline. Another token, no token, a second report or other digests
// never succeed.
func (c Config) bootReport(ctx context.Context, work *site.Work, media *mediaClient, window mediaWindow, downloads []ops.DownloadInput) error {
	key := Key(work.Operation.UID)
	if err := work.Step(ctx, site.Step{Name: "disk-power-off", Send: func(ctx context.Context) error { return c.powerOff(ctx, work) }, Idempotent: true}); err != nil {
		return err
	}
	if err := work.Step(ctx, site.Step{Name: "disk-arm", Send: func(ctx context.Context) error { return conn(work).SetBootOnce(ctx, "Hdd") }, Idempotent: true}); err != nil {
		return err
	}
	if err := work.Step(ctx, site.Step{
		Name:    "disk-power-on",
		Payload: powerOn{RequestedAt: c.now().UTC()},
		Send:    func(ctx context.Context) error { return conn(work).Reset(ctx, "On") },
		Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) {
			return c.bootStarted(ctx, work, "Hdd", media, key)
		},
	}); err != nil {
		return err
	}
	poll, _, _, _ := c.durations()
	for {
		status, err := media.window(ctx, key)
		switch {
		case err != nil:
			// An unreadable media service is retried until the deadline.
		case status.Duplicates > 0:
			return errors.New("the report token was used more than once")
		case status.Report != nil:
			if status.Report.Image != window.Image || status.Report.Bootstrap != window.Bootstrap {
				return errors.New("the first-boot report does not carry the operation's image and bootstrap digests")
			}
			if !sameDownloads(status.Report.Downloads, downloads) {
				return errors.New("the first-boot report names downloads that are not the operation's pinned inputs")
			}
			return work.Step(ctx, site.Step{Name: "media-report-verified", Send: func(context.Context) error { return nil }, Idempotent: true})
		}
		if !c.now().Before(window.ReportExpires) {
			return errors.New("no first-boot report with the operation's token arrived before the report deadline")
		}
		if err := sleep(ctx, poll); err != nil {
			return err
		}
	}
}

// ejectAll ejects every device that reports a medium, each as its own
// journaled step, then reads every device back empty.
func (c Config) ejectAll(ctx context.Context, work *site.Work, prefix string) error {
	devices, err := conn(work).VirtualMedia(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if device.Empty() {
			continue
		}
		if err := work.Step(ctx, site.Step{
			Name:    prefix + stepID(device.ID),
			Payload: insertPayload{Device: device.ID, URLSHA256: digest(device.Image)},
			Send:    func(ctx context.Context) error { return conn(work).EjectMedia(ctx, device) },
			Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) { return deviceEmpty(ctx, work, device.ID) },
		}); err != nil {
			return err
		}
	}
	return allEmpty(ctx, work)
}

func deviceEmpty(ctx context.Context, work *site.Work, id string) (bool, error) {
	devices, err := conn(work).VirtualMedia(ctx)
	if err != nil {
		return false, err
	}
	for _, device := range devices {
		if device.ID == id {
			return device.Empty(), nil
		}
	}
	return true, nil
}

// allEmpty reads every virtual media device back and requires each empty.
func allEmpty(ctx context.Context, work *site.Work) error {
	devices, err := conn(work).VirtualMedia(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if !device.Empty() {
			return fmt.Errorf("virtual media device %s still reports a medium", device.ID)
		}
	}
	return nil
}

// stepID keeps a BMC device Id usable in a step name.
func stepID(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// closeWindows closes, at the media service, every window the site ever
// opened for the Device, not only this operation's, and deletes their
// config media.
func (c Config) closeWindows(ctx context.Context, work *site.Work, media *mediaClient) error {
	store := work.Store()
	records, err := store.Records(mediaRecords)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Device != work.Device {
			continue
		}
		if err := media.close(ctx, Key(record.Name)); err != nil {
			return err
		}
		if err := store.DeleteRecord(work.Device, mediaConfigs, record.Name); err != nil {
			return err
		}
	}
	return nil
}

// releaseMedia closes every window of the Device and ejects every device,
// then proves release from two independent sources: the BMC reports every
// device empty and no boot override, and the media service reports no open
// window, no transfer in progress and no reused report token for the
// Device. If either cannot be read, release is unproven.
func (c Config) releaseMedia(ctx context.Context, work *site.Work, media *mediaClient) error {
	if err := work.Step(ctx, site.Step{Name: "media-close", Send: func(ctx context.Context) error { return c.closeWindows(ctx, work, media) }, Idempotent: true}); err != nil {
		return err
	}
	if err := c.ejectAll(ctx, work, "media-eject-"); err != nil {
		return err
	}
	power, err := conn(work).Observe(ctx)
	if err != nil {
		return err
	}
	if power.OverrideEnabled != "Disabled" && power.OverrideTarget != "None" {
		return fmt.Errorf("a boot override is still set: %s %s", power.OverrideEnabled, power.OverrideTarget)
	}
	status, err := media.device(ctx, work.Device)
	switch {
	case err != nil:
		return err
	case status.OpenWindows > 0:
		return errors.New("a media window of the Device is still open")
	case status.OpenTransfers > 0:
		return fmt.Errorf("the media service reports %d transfers in progress for the Device", status.OpenTransfers)
	case status.Duplicates > 0:
		return errors.New("a report token of the Device was used more than once")
	}
	return work.Step(ctx, site.Step{Name: "media-released", Send: func(context.Context) error { return nil }, Idempotent: true})
}

// undoMedia closes what it opened and ejects what is inserted. Its sends
// are journaled steps, so a newer fence stops them.
func (c Config) undoMedia(ctx context.Context, work *site.Work, media *mediaClient) {
	_ = work.Step(ctx, site.Step{Name: "media-undo-close", Send: func(ctx context.Context) error { return c.closeWindows(ctx, work, media) }, Idempotent: true})
	devices, err := conn(work).VirtualMedia(ctx)
	if err != nil {
		return
	}
	for _, device := range devices {
		if device.Empty() {
			continue
		}
		_ = work.Step(ctx, site.Step{Name: "media-undo-eject-" + stepID(device.ID), Send: func(ctx context.Context) error { return conn(work).EjectMedia(ctx, device) }, Idempotent: true})
	}
}

// minTime is the earlier of a phase's end and the operation's deadline.
func minTime(a, deadline time.Time) time.Time {
	if !deadline.IsZero() && deadline.Before(a) {
		return deadline.UTC()
	}
	return a.UTC()
}

// bmcStepJournaled reports whether the journal holds any step of this
// operation other than opening its media window, the only step that goes to
// the media service rather than the BMC.
func bmcStepJournaled(work *site.Work) bool {
	journal, err := work.Store().Journal(work.Device)
	if err != nil {
		return true
	}
	for _, entry := range journal {
		if entry.Operation == work.Operation.UID && entry.Step != "media-window" {
			return true
		}
	}
	return false
}

// reportOrigin is where the installed OS posts its report: the site's
// report path to the media service, or the recorded media service.
func (c Config) reportOrigin(plan mediaPlan) string {
	if c.ReportOrigin != "" {
		return c.ReportOrigin
	}
	return plan.origin
}

func (c Config) reportFrom() []string {
	out := make([]string, 0, len(c.ReportFrom))
	for _, prefix := range c.ReportFrom {
		out = append(out, prefix.String())
	}
	return out
}

// sameDownloads reports whether the installer reported exactly the recorded
// downloads, each by the digest its reference pins.
func sameDownloads(reported []ReportedDownload, recorded []ops.DownloadInput) bool {
	if len(reported) != len(recorded) {
		return false
	}
	want := map[string]string{}
	for _, download := range recorded {
		_, digest, _ := strings.Cut(download.Reference, "@")
		want[download.Name] = digest
	}
	for _, download := range reported {
		if digest, ok := want[download.Name]; !ok || digest != download.Digest {
			return false
		}
		delete(want, download.Name)
	}
	return len(want) == 0
}
