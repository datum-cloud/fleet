package redfishwork

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
)

// fakeCustody is the central media service's view of custody: the one
// operation it names active, at its fence.
type fakeCustody struct {
	mu    sync.Mutex
	uid   string
	fence int64
}

func (f *fakeCustody) Active(context.Context, string) (string, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uid, f.fence, f.uid != "", nil
}

func (f *fakeCustody) set(uid string, fence int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uid, f.fence = uid, fence
}

// mediaBMC simulates one server behind a TLS Redfish BMC with two virtual
// CD devices. An insert fetches the medium from the central media service,
// as a BMC would. A one-time Cd boot runs the installer, which reads the
// config medium and powers off without reporting; the next disk boot runs
// the installed OS, which posts its report to the media service.
type mediaBMC struct {
	mu      sync.Mutex
	t       *testing.T
	power   string
	enabled string
	target  string
	media   map[string]string
	fetched map[string][]byte
	// installed is what the installer wrote for the installed OS.
	installed map[string]string
	server    *httptest.Server
	central   *MediaServer
	requests  []string
	// onInsert runs as each insert arrives, before its effect.
	onInsert func(device string)
	// ejectDelay holds the next eject's response past the client timeout
	// after applying it, as a lost response.
	ejectDelay time.Duration
	// ignoreEject keeps media inserted after an eject.
	ignoreEject bool
	// report replaces fields of the installed OS's report; reports is how
	// many times it posts it.
	reportToken, reportImage string
	reports                  int
	// downloads is what the installer reports it downloaded; extraDownloads
	// adds downloads the operation never pinned.
	downloads, extraDownloads []ReportedDownload
	// lag keeps the server reporting Off, with the override still armed,
	// for a while after it accepted a power-on, as some BMCs do.
	lag time.Duration
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newMediaBMC(t *testing.T, central *MediaServer) *mediaBMC {
	t.Helper()
	b := &mediaBMC{t: t, power: "Off", enabled: "Disabled", target: "None", media: map[string]string{}, fetched: map[string][]byte{}, installed: map[string]string{}, central: central, reports: 1}
	b.server = httptest.NewUnstartedServer(b)
	b.server.TLS = &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}
	b.server.StartTLS()
	t.Cleanup(b.server.Close)
	return b
}

func (b *mediaBMC) pin() string {
	certificate, _ := x509.ParseCertificate(b.server.TLS.Certificates[0].Certificate[0])
	sum := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// call sends one request to the central media service from the BMC
// network.
func (b *mediaBMC) call(method, url, body string) ([]byte, int) {
	parsed, err := neturl.Parse(url)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	request := httptest.NewRequest(method, parsed.Path, strings.NewReader(body))
	request.RemoteAddr = "192.0.2.50:40000"
	recorder := httptest.NewRecorder()
	b.central.Handler().ServeHTTP(recorder, request)
	return recorder.Body.Bytes(), recorder.Code
}

func (b *mediaBMC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	system := "/redfish/v1/Systems/A"
	collection := system + "/VirtualMedia"
	b.mu.Lock()
	switch {
	case r.Method == http.MethodPatch && r.URL.Path == system:
		var body struct{ Boot map[string]string }
		json.NewDecoder(r.Body).Decode(&body)
		b.enabled, b.target = body.Boot["BootSourceOverrideEnabled"], body.Boot["BootSourceOverrideTarget"]
		b.requests = append(b.requests, "arm "+b.target)
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodPost && r.URL.Path == system+"/Actions/ComputerSystem.Reset":
		var body struct{ ResetType string }
		json.NewDecoder(r.Body).Decode(&body)
		b.requests = append(b.requests, "reset "+body.ResetType)
		switch body.ResetType {
		case "ForceOff":
			b.power = "Off"
		case "On":
			target := b.target
			if b.lag > 0 {
				// The boot starts later, from whatever override is armed then.
				go func(lag time.Duration) {
					time.Sleep(lag)
					b.mu.Lock()
					target := b.target
					b.power, b.enabled, b.target = "On", "Disabled", "None"
					b.mu.Unlock()
					b.boot(target)
				}(b.lag)
			} else {
				b.power = "On"
				b.enabled, b.target = "Disabled", "None"
				go b.boot(target)
			}
		}
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Actions/VirtualMedia.InsertMedia"):
		device := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, collection+"/"), "/Actions/VirtualMedia.InsertMedia")
		var body struct{ Image string }
		json.NewDecoder(r.Body).Decode(&body)
		b.requests = append(b.requests, "insert "+device)
		hook := b.onInsert
		b.mu.Unlock()
		if hook != nil {
			hook(device)
		}
		data, code := b.call(http.MethodGet, body.Image, "")
		if code != http.StatusOK {
			http.Error(w, "fetch failed", http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		b.media[device], b.fetched[device] = body.Image, data
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Actions/VirtualMedia.EjectMedia"):
		device := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, collection+"/"), "/Actions/VirtualMedia.EjectMedia")
		b.requests = append(b.requests, "eject "+device)
		if !b.ignoreEject {
			delete(b.media, device)
			delete(b.fetched, device)
		}
		delay := b.ejectDelay
		b.ejectDelay = 0
		b.mu.Unlock()
		time.Sleep(delay)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	docs := map[string]any{
		"/redfish/v1":         map[string]any{"@odata.id": "/redfish/v1", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}},
		"/redfish/v1/Systems": map[string]any{"Members": []any{map[string]string{"@odata.id": system}}},
		system: map[string]any{"@odata.id": system, "Id": "A", "UUID": uuid, "PowerState": b.power, "VirtualMedia": map[string]string{"@odata.id": collection},
			"Boot":    map[string]any{"BootSourceOverrideEnabled": b.enabled, "BootSourceOverrideTarget": b.target, "BootSourceOverrideTarget@Redfish.AllowableValues": []string{"Pxe", "Hdd", "Cd"}},
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": system + "/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On", "ForceOff"}}}},
		collection: map[string]any{"Members": []any{map[string]string{"@odata.id": collection + "/CD1"}, map[string]string{"@odata.id": collection + "/CD2"}}},
	}
	for _, id := range []string{"CD1", "CD2"} {
		path := collection + "/" + id
		var image any
		if url, ok := b.media[id]; ok {
			image = url
		}
		docs[path] = map[string]any{"@odata.id": path, "Id": id, "MediaTypes": []string{"CD", "DVD"}, "Inserted": image != nil, "Image": image,
			"Actions": map[string]any{"#VirtualMedia.InsertMedia": map[string]string{"target": path + "/Actions/VirtualMedia.InsertMedia"},
				"#VirtualMedia.EjectMedia": map[string]string{"target": path + "/Actions/VirtualMedia.EjectMedia"}}}
	}
	b.mu.Unlock()
	doc, ok := docs[strings.TrimSuffix(r.URL.Path, "/")]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(doc)
}

// boot runs the armed boot: a Cd boot is the installer, a disk boot the
// installed OS posting its first-boot report.
func (b *mediaBMC) boot(target string) {
	time.Sleep(30 * time.Millisecond)
	b.mu.Lock()
	switch target {
	case "Cd":
		values := map[string]string{}
		b.downloads = nil
		for _, line := range strings.Split(strings.TrimRight(string(b.fetched["CD2"]), "\x00"), "\n") {
			if key, value, ok := strings.Cut(line, "="); ok {
				values[key] = value
				// The installer reports each download by the digest it pins.
				if name, reference, ok := strings.Cut(value, " "); key == "download" && ok {
					_, pinned, _ := strings.Cut(reference, "@")
					b.downloads = append(b.downloads, ReportedDownload{Name: name, Digest: pinned})
				}
			}
		}
		b.downloads = append(b.downloads, b.extraDownloads...)
		b.installed = map[string]string{"token": values["token"], "report": values["report"], "bootstrap": SaltedDigest(values["salt"], nil)}
	case "Hdd":
		token, image := b.installed["token"], b.central.contentImage(b.t)
		if b.reportToken != "" {
			token = b.reportToken
		}
		if b.reportImage != "" {
			image = b.reportImage
		}
		report, _ := json.Marshal(InstallReport{Token: token, Image: image, Bootstrap: b.installed["bootstrap"], Downloads: b.downloads})
		url, n := b.installed["report"], b.reports
		b.mu.Unlock()
		for i := range n {
			b.call(http.MethodPost, url, string(report))
			// A retry of the same report changes nothing; a later report
			// with the same token is a second install.
			b.call(http.MethodPost, url, string(report))
			if i == 0 {
				report, _ = json.Marshal(InstallReport{Token: token, Image: image, Bootstrap: "sha256:" + strings.Repeat("0", 64)})
			}
		}
		b.mu.Lock()
	}
	b.power = "Off"
	b.mu.Unlock()
}

// contentImage is the digest the boot medium's manifest reports.
func (m *MediaServer) contentImage(t *testing.T) string {
	data, err := os.ReadFile(filepath.Join(m.Artifacts, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	return "sha256:" + digest(lines[0]+lines[1])
}

func (b *mediaBMC) log() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.requests...)
}

// artifacts writes a synthetic image: its kernel, initrd and boot medium,
// and their manifest.
func artifacts(t *testing.T) (string, ops.ImageInput) {
	t.Helper()
	dir := t.TempDir()
	var manifest strings.Builder
	for _, name := range redfish.Artifacts {
		data := []byte("synthetic " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		manifest.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(manifest.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := redfish.VerifyArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	name, value, _ := strings.Cut(identity, "@")
	return dir, ops.ImageInput{Name: name, Digest: value}
}

type mediaEnv struct {
	bmc     *mediaBMC
	central *MediaServer
	custody *fakeCustody
	runner  *site.Runner
	store   *site.Store
	image   ops.ImageInput
	origin  string
	fetch   time.Duration
	report  time.Duration
	workers *workerCA
	// downloads are the installer downloads the operation pins.
	downloads []ops.DownloadInput
}

func newMediaEnv(t *testing.T) *mediaEnv {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir, image := artifacts(t)
	custodyView := &fakeCustody{}
	central := &MediaServer{Root: t.TempDir(), Artifacts: dir, Custody: custodyView, Clients: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("127.0.0.0/8")},
		Workers: func(identity, d string) bool { return identity == workerIdentity && d == device }}
	// The worker reaches the central service over TLS it verifies, and
	// presents its client certificate from the workers' CA.
	certificate := selfSigned(t)
	workers := newWorkerCA(t)
	api := httptest.NewUnstartedServer(central.Handler())
	api.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: workers.pool}
	api.StartTLS()
	t.Cleanup(api.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	b := newMediaBMC(t, central)
	// Installed servers report from the BMC network address the fake uses.
	config := Config{ConsoleRoot: t.TempDir(), BootRoot: t.TempDir(), Poll: 10 * time.Millisecond, OffGrace: time.Second, MediaCA: ca, Artifacts: dir,
		ReportFrom: []netip.Prefix{netip.MustParsePrefix("192.0.2.50/32")}}
	runner := &site.Runner{
		Site: "site-example-a", Store: store, Results: site.NewResults(store),
		Access: site.RedfishAccess{Credentials: credentials{pin: b.pin()}, MediaIdentity: &mediaIdentity{identity: workers.issue(t, workerIdentity)},
			Targets: site.StaticTargets{device: {Device: "server-example-001", Endpoint: b.server.URL, SystemID: "A", SystemUUID: uuid, DiskBytes: diskBytes, Console: "console.log"}},
			// A short client timeout makes a withheld response a lost one.
			Dial: func(endpoint, username, password, pin string) (*redfish.Client, error) {
				return redfish.NewPinnedClient(endpoint, username, password, pin, time.Second)
			}},
		Kinds: map[string]site.Kind{"DeviceInstallation": Installation{config}},
		Poll:  10 * time.Millisecond,
	}
	return &mediaEnv{bmc: b, central: central, custody: custodyView, runner: runner, store: store, image: image, origin: api.URL, fetch: time.Minute, report: time.Minute, workers: workers}
}

const workerIdentity = "fleet-site-worker:site-example-a"

// mediaIdentity is the site worker's own media service client certificate.
type mediaIdentity struct {
	identity *tls.Certificate
}

func (c *mediaIdentity) MediaClient(context.Context, string) (*tls.Certificate, error) {
	return c.identity, nil
}

// workerCA issues site worker client certificates.
type workerCA struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pool        *x509.CertPool
}

func newWorkerCA(t *testing.T) *workerCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "synthetic-site-workers"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	return &workerCA{certificate: certificate, key: key, pool: pool}
}

func (w *workerCA) issue(t *testing.T, identity string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: identity}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, w.certificate, &key.PublicKey, w.key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// asWorker marks a direct handler request as presented with a verified
// client certificate of the identity.
func (w *workerCA) asWorker(t *testing.T, request *http.Request, identity string) *http.Request {
	certificate, _ := x509.ParseCertificate(w.issue(t, identity).Certificate[0])
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{certificate, w.certificate}}}
	return request
}

func (e *mediaEnv) spec() ops.DeviceInstallationSpec {
	image := e.image
	image.Downloads = e.downloads
	return ops.DeviceInstallationSpec{Image: image, BootInterface: "primary", TargetDisks: []string{"disk-0"}, RequestUID: "request-1",
		Delivery: ops.DeliveryInput{Method: ops.DeliveryVirtualMedia, Addresses: ops.AddressesDataNetwork, MediaService: e.origin,
			FetchWindow: &metav1.Duration{Duration: e.fetch}, ReportWindow: &metav1.Duration{Duration: e.report}}}
}

// assignment admits the operation: custody names it active at its fence.
func (e *mediaEnv) assignment(uid string, fence int64) site.Assignment {
	e.custody.set(uid, fence)
	inputs, _ := json.Marshal(e.spec())
	sum := sha256.Sum256(inputs)
	return site.Assignment{
		Device: device, Operation: ops.OperationReference{Kind: "DeviceInstallation", Name: "alloc-example-" + uid, UID: uid}, Fence: fence, AdmittedAt: time.Now(), Deadline: time.Now().Add(time.Minute),
		Snapshot:    ops.DeviceSnapshot{BMCEndpoint: e.bmc.server.URL, SystemUUID: uuid, BMCPublicKeySHA256: e.bmc.pin()},
		InputDigest: "sha256:" + hex.EncodeToString(sum[:]), Inputs: inputs,
	}
}

func (e *mediaEnv) window(t *testing.T, uid string) Window {
	t.Helper()
	window, found, err := e.central.load(Key(uid))
	if err != nil || !found {
		t.Fatalf("window: %v, %v", found, err)
	}
	return window
}

func (e *mediaEnv) journaled(uid string) map[string]site.StepState {
	journal, _ := e.store.Journal(device)
	out := map[string]site.StepState{}
	for _, entry := range journal {
		if entry.Operation == uid {
			out[entry.Step] = entry.State
		}
	}
	return out
}

func (e *mediaEnv) run(t *testing.T, uid string, fence int64) custody.SiteResult {
	t.Helper()
	result, err := e.runner.Run(context.Background(), e.assignment(uid, fence))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAVirtualMediaInstallIsProvenByItsFirstBootReport(t *testing.T) {
	e := newMediaEnv(t)
	result := e.run(t, "op-media", 1)
	if result.Phase != ops.PhaseSucceeded {
		t.Fatalf("result = %+v", result)
	}
	var evidence ops.InstallationEvidence
	if err := json.Unmarshal(result.Evidence, &evidence); err != nil || len(evidence.Steps) != 3 || evidence.Steps[0].Name != "media-installer" || evidence.Steps[1].Name != "first-boot-report" || evidence.Steps[2].Name != "media-release" {
		t.Fatalf("evidence = %s", result.Evidence)
	}
	// The server is already off before each boot, so no ForceOff is sent.
	want := []string{"insert CD1", "insert CD2", "arm Cd", "reset On", "arm Hdd", "reset On", "eject CD1", "eject CD2"}
	if got := e.bmc.log(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("BMC requests = %v, want %v", got, want)
	}
	// Release: no medium or override at the BMC; at the media service the
	// window is closed, its medium dropped, its transfers ended and its
	// one report taken.
	if len(e.bmc.media) != 0 || e.bmc.enabled != "Disabled" {
		t.Fatalf("BMC after release: media %v, override %s", e.bmc.media, e.bmc.enabled)
	}
	window := e.window(t, "op-media")
	if !window.Closed || window.ConfigMedium != nil || window.Report == nil || window.Duplicates != 0 || window.Rejected != 0 {
		t.Fatalf("window = %+v", window)
	}
	transfers, err := e.central.Transfers(Key("op-media"))
	if err != nil || len(transfers) != 2 || !transfers[0].Complete || !transfers[1].Complete {
		t.Fatalf("transfers = %+v, %v", transfers, err)
	}
	if found, _ := e.store.GetRecord(device, mediaConfigs, "op-media", &configMedium{}); found {
		t.Fatal("the rendered config medium outlived its window")
	}
	if steps := e.journaled("op-media"); steps["media-released"] != site.StepDone || steps["media-report-verified"] != site.StepDone {
		t.Fatalf("journal = %v", steps)
	}
	// Records keep digests only: neither the journal, the site's window
	// record, the media service's window nor the evidence holds the token.
	token := e.bmc.installed["token"]
	journal, _ := json.Marshal(e.journaled("op-media"))
	var site mediaWindow
	e.store.GetRecord(device, mediaRecords, "op-media", &site)
	siteRecord, _ := json.Marshal(site)
	central, _ := os.ReadFile(filepath.Join(e.central.Root, "windows", Key("op-media")+".json"))
	for _, text := range [][]byte{journal, siteRecord, central, result.Evidence} {
		if len(token) != 64 || strings.Contains(string(text), token) {
			t.Fatal("a record carries the report token")
		}
	}
	// The closed window serves nothing, even to its BMC.
	if _, code := e.bmc.call(http.MethodGet, BootMediumURL(e.origin, Key("op-media"), window.BootDigest), ""); code != http.StatusForbidden {
		t.Fatalf("closed window served the boot medium: %d", code)
	}
}

// TestEveryMediaStepIsJournaledBeforeItIsSent checks, as each insert
// reaches the BMC, that the journal already records it as intended.
func TestEveryMediaStepIsJournaledBeforeItIsSent(t *testing.T) {
	e := newMediaEnv(t)
	seen := map[string]site.StepState{}
	e.bmc.onInsert = func(device string) {
		name := map[string]string{"CD1": "media-insert-boot", "CD2": "media-insert-config"}[device]
		seen[name] = e.journaled("op-journal")[name]
		if e.journaled("op-journal")["media-window"] != site.StepDone {
			t.Error("a medium was inserted before its window was journaled open")
		}
	}
	if result := e.run(t, "op-journal", 1); result.Phase != ops.PhaseSucceeded {
		t.Fatalf("result = %+v", result)
	}
	if seen["media-insert-boot"] != site.StepIntended || seen["media-insert-config"] != site.StepIntended {
		t.Fatalf("journal at each send = %v, want Intended", seen)
	}
}

// TestAStaleFenceCannotPrepareMedia raises the Device's site fence, and
// custody's, while the first insert is in flight. The worker sends nothing
// more, the media service stops serving the window at once, and the
// outcome is unknown.
func TestAStaleFenceCannotPrepareMedia(t *testing.T) {
	e := newMediaEnv(t)
	var once sync.Once
	e.bmc.onInsert = func(string) {
		once.Do(func() {
			if err := e.store.Raise(device, 2); err != nil {
				t.Error(err)
			}
			e.custody.set("op-newer", 2)
		})
	}
	if result := e.run(t, "op-stale", 1); result.Phase != ops.PhaseOutcomeUnknown {
		t.Fatalf("result = %+v", result)
	}
	if got := e.bmc.log(); strings.Join(got, ",") != "insert CD1" {
		t.Fatalf("BMC requests after the fence rose = %v, want only the first insert", got)
	}
	if _, code := e.bmc.call(http.MethodGet, BootMediumURL(e.origin, Key("op-stale"), e.window(t, "op-stale").BootDigest), ""); code != http.StatusForbidden {
		t.Fatalf("a window custody no longer names served media: %d", code)
	}
	// A delayed worker at the old fence is refused by the site store.
	if _, err := e.runner.Run(context.Background(), e.assignment("op-delayed", 1)); !errors.Is(err, site.ErrStaleFence) {
		t.Fatalf("a stale assignment ran: %v", err)
	}
	if len(e.bmc.log()) != 1 {
		t.Fatalf("the stale assignment reached the BMC: %v", e.bmc.log())
	}
}

// TestTheMediaServiceOpensAWindowOnlyForCustodysOperation has custody name
// another operation: the media service refuses the window, and the worker
// sends nothing to the BMC.
func TestTheMediaServiceOpensAWindowOnlyForCustodysOperation(t *testing.T) {
	e := newMediaEnv(t)
	assignment := e.assignment("op-unadmitted", 1)
	e.custody.set("op-other", 1)
	result, err := e.runner.Run(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseFailed || result.Reason != custody.ReasonMediaWindowRefused || len(e.bmc.log()) != 0 {
		t.Fatalf("result = %+v, BMC %v", result, e.bmc.log())
	}
	if _, found, _ := e.central.load(Key("op-unadmitted")); found {
		t.Fatal("the media service opened a window custody does not name")
	}
}

// TestALostEjectResponseEndsUnknown withholds the response to the first
// release eject past the client timeout. The eject took effect, but the
// worker cannot prove it, so the outcome is unknown and never Succeeded.
func TestALostEjectResponseEndsUnknown(t *testing.T) {
	e := newMediaEnv(t)
	go func() {
		for range 1000 {
			if e.journaled("op-lost")["media-report-verified"] == site.StepDone {
				e.bmc.mu.Lock()
				e.bmc.ejectDelay = 2 * time.Second
				e.bmc.mu.Unlock()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	result := e.run(t, "op-lost", 1)
	if result.Phase != ops.PhaseOutcomeUnknown || result.Reason != site.ReasonStep {
		t.Fatalf("result = %+v", result)
	}
	if steps := e.journaled("op-lost"); steps["media-released"] != "" || steps["media-eject-cd1"] != site.StepIntended {
		t.Fatalf("journal = %v", steps)
	}
	// The undo closed the window; that is never proof.
	if !e.window(t, "op-lost").Closed {
		t.Fatal("the undo left the window open")
	}
}

// TestMediaStillReportedAfterEjectIsUnproven keeps the media inserted
// after every eject.
func TestMediaStillReportedAfterEjectIsUnproven(t *testing.T) {
	e := newMediaEnv(t)
	e.bmc.ignoreEject = true
	if result := e.run(t, "op-stuck", 1); result.Phase != ops.PhaseOutcomeUnknown || e.journaled("op-stuck")["media-released"] != "" {
		t.Fatalf("result = %+v", result)
	}
}

// TestAFirstBootReportThatDoesNotProveTheInstallNeverSucceeds covers every
// report the media service must not count: another token, none at all,
// another image, and the token used twice.
func TestAFirstBootReportThatDoesNotProveTheInstallNeverSucceeds(t *testing.T) {
	for name, set := range map[string]func(*mediaBMC){
		"another token": func(b *mediaBMC) { b.reportToken = strings.Repeat("e", 64) },
		"no report":     func(b *mediaBMC) { b.reports = 0 },
		"another image": func(b *mediaBMC) { b.reportImage = "sha256:" + strings.Repeat("f", 64) },
		"a second use":  func(b *mediaBMC) { b.reports = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			e := newMediaEnv(t)
			e.fetch, e.report = 500*time.Millisecond, 300*time.Millisecond
			set(e.bmc)
			result := e.run(t, "op-report", 1)
			if result.Phase != ops.PhaseOutcomeUnknown || e.journaled("op-report")["media-released"] != "" {
				t.Fatalf("result = %+v, journal %v", result, e.journaled("op-report"))
			}
			// The failed operation still tried to undo delivery.
			if !e.window(t, "op-report").Closed {
				t.Fatal("the window stayed open after the failure")
			}
		})
	}
}

// TestTheMediaServiceServesAndReportsOnlyThroughAnOpenWindow probes the
// service directly.
func TestTheMediaServiceServesAndReportsOnlyThroughAnOpenWindow(t *testing.T) {
	e := newMediaEnv(t)
	now := time.Now()
	e.central.Now = func() time.Time { return now }
	capability, token := strings.Repeat("c", 64), strings.Repeat("d", 64)
	e.custody.set("op-serve", 1)
	call := func(method, path, body, from string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.RemoteAddr = from
		if strings.HasPrefix(path, "/v1/") {
			e.workers.asWorker(t, request, workerIdentity)
		}
		recorder := httptest.NewRecorder()
		e.central.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	open, _ := json.Marshal(OpenRequest{Device: device, Operation: "op-serve", Fence: 1, BootDigest: strings.Repeat("b", 64), CapabilitySHA256: digest(capability), TokenSHA256: digest(token),
		ConfigMedium: []byte("medium"), FetchExpires: now.Add(time.Minute), ReportExpires: now.Add(2 * time.Minute), ReportFrom: []string{"192.0.2.60/32"}})
	key := Key("op-serve")
	if code := call(http.MethodPut, "/v1/windows/"+key, string(open), "127.0.0.1:1"); code != http.StatusCreated {
		t.Fatalf("open = %d", code)
	}
	if code := call(http.MethodPut, "/v1/windows/"+key, strings.Replace(string(open), digest(token), digest(capability), 1), "127.0.0.1:1"); code != http.StatusConflict {
		t.Fatalf("a second, different open = %d", code)
	}
	config := "/media/" + key + "/config/" + capability + "/config.img"
	if code := call(http.MethodGet, config, "", "192.0.2.50:1"); code != http.StatusOK {
		t.Fatalf("the open window's config medium: %d", code)
	}
	if code := call(http.MethodGet, config, "", "198.51.100.7:1"); code != http.StatusForbidden {
		t.Fatalf("a client outside the BMC network was served: %d", code)
	}
	if code := call(http.MethodGet, "/media/"+key+"/config/"+strings.Repeat("9", 64)+"/config.img", "", "192.0.2.50:1"); code != http.StatusForbidden {
		t.Fatalf("another capability was served: %d", code)
	}
	report := func(body string) int { return call(http.MethodPost, "/report/"+key, body, "192.0.2.60:1") }
	if code := report(`{"image":"x"}`); code != http.StatusForbidden {
		t.Fatalf("a report with no token = %d", code)
	}
	if code := report(`{"token":"` + strings.Repeat("e", 64) + `"}`); code != http.StatusForbidden {
		t.Fatalf("a report with another token = %d", code)
	}
	good := `{"token":"` + token + `","image":"sha256:1","bootstrap":"sha256:2"}`
	// A report with the right token from an address that is not the
	// Machine's is refused for where it came from.
	if code := call(http.MethodPost, "/report/"+key, good, "192.0.2.61:1"); code != http.StatusForbidden {
		t.Fatalf("a report from another address = %d", code)
	}
	if window := e.window(t, "op-serve"); window.Report != nil {
		t.Fatal("a report from another address was taken")
	}
	if code := report(good); code != http.StatusOK {
		t.Fatalf("the report = %d", code)
	}
	if code := report(good); code != http.StatusOK {
		t.Fatalf("a repeat of the same report = %d", code)
	}
	if code := report(strings.Replace(good, "sha256:2", "sha256:3", 1)); code != http.StatusConflict {
		t.Fatalf("a second, different use of the token = %d", code)
	}
	window := e.window(t, "op-serve")
	if window.Rejected != 3 || window.Duplicates != 1 || window.Report == nil || window.Report.Image != "sha256:1" {
		t.Fatalf("window = %+v", window)
	}
	// Past the fetch deadline the media stop; a fence rise stops everything.
	now = now.Add(90 * time.Second)
	if code := call(http.MethodGet, config, "", "192.0.2.50:1"); code != http.StatusForbidden {
		t.Fatalf("a window past its fetch deadline was served: %d", code)
	}
	e.custody.set("op-next", 2)
	if code := report(good); code != http.StatusForbidden {
		t.Fatalf("a report after the fence rose = %d", code)
	}
}

// TestReleaseNeedsTheMediaServiceQuiet checks the media service's half of
// the proof: an open window or an unfinished transfer counts for the
// Device even when the BMC reports empty.
func TestReleaseNeedsTheMediaServiceQuiet(t *testing.T) {
	e := newMediaEnv(t)
	e.custody.set("op-open", 3)
	status := func() DeviceStatus {
		request := e.workers.asWorker(t, httptest.NewRequest(http.MethodGet, "/v1/devices/"+device, nil), workerIdentity)
		request.RemoteAddr = "127.0.0.1:1"
		recorder := httptest.NewRecorder()
		e.central.Handler().ServeHTTP(recorder, request)
		var out DeviceStatus
		json.NewDecoder(recorder.Body).Decode(&out)
		return out
	}
	if err := e.central.save(Key("op-open"), Window{Device: device, Operation: "op-open", Fence: 3, FetchExpires: time.Now().Add(time.Hour), ReportExpires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := status(); got.OpenWindows != 1 {
		t.Fatalf("an open window counted as released: %+v", got)
	}
	if err := appendEvidence(filepath.Join(e.central.Root, "transfers", Key("op-open")), transferLog, "t start 01 boot\n"); err != nil {
		t.Fatal(err)
	}
	e.custody.set("op-next", 4)
	if got := status(); got.OpenWindows != 0 || got.OpenTransfers != 1 {
		t.Fatalf("a transfer in progress counted as released: %+v", got)
	}
	// The worker's release reads the same report and stays unproven.
	if err := appendEvidence(filepath.Join(e.central.Root, "transfers", Key("op-open")), transferLog, "t end 01 boot bytes=1 complete\n"); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != (DeviceStatus{Windows: 1}) {
		t.Fatalf("status = %+v", got)
	}
}

// TestAVirtualMediaSiteRefusesWhatItCannotDeliver refuses, with nothing
// sent, a virtual media installation at a site without media trust, and
// network boot with virtual media inputs or downloads.
func TestAVirtualMediaSiteRefusesWhatItCannotDeliver(t *testing.T) {
	e := newMediaEnv(t)
	installation := e.runner.Kinds["DeviceInstallation"].(Installation)
	installation.MediaCA = nil
	e.runner.Kinds["DeviceInstallation"] = installation
	result := e.run(t, "op-no-media", 1)
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonUnsupportedTarget || len(e.bmc.log()) != 0 {
		t.Fatalf("result = %+v, BMC %v", result, e.bmc.log())
	}
	if refusal := (networkBoot{}).check(context.Background(), &site.Work{}, ops.DeviceInstallationSpec{Delivery: ops.DeliveryInput{Method: ops.DeliveryNetworkBoot, Addresses: ops.AddressesDataNetwork}}); refusal == nil || refusal.Reason != custody.ReasonDeliveryInputs {
		t.Fatalf("network boot with an address mode: %+v", refusal)
	}
	downloads := ops.DeviceInstallationSpec{Image: ops.ImageInput{Downloads: []ops.DownloadInput{{Name: "installer", Reference: "registry.example/installer@sha256:" + strings.Repeat("a", 64)}}},
		Delivery: ops.DeliveryInput{Method: ops.DeliveryNetworkBoot}}
	if refusal := (networkBoot{}).check(context.Background(), &site.Work{}, downloads); refusal == nil || refusal.Reason != ReasonUnsupportedTarget {
		t.Fatalf("network boot with downloads: %+v", refusal)
	}
}

// TestTheConfigMediumCarriesPinnedDownloads renders a medium for an image
// whose installer downloads one pinned artifact.
func TestTheConfigMediumCarriesPinnedDownloads(t *testing.T) {
	reference := "registry.example/installer@sha256:" + strings.Repeat("a", 64)
	data, err := renderConfig(strings.Repeat("1", 64), strings.Repeat("2", 64), nil, mediaPlan{addresses: ops.AddressesDataNetwork, downloads: []ops.DownloadInput{{Name: "installer", Reference: reference}}},
		"https://192.0.2.10:8443/report/0123456789abcdef", []byte("ca"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimRight(string(data), "\x00")
	for _, want := range []string{"FLEET-CONFIG-V1\n", "\ndownload=installer " + reference + "\n", "\nreport=https://192.0.2.10:8443/report/0123456789abcdef\n", "\naddresses=DataNetwork\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config medium lacks %q:\n%s", want, text)
		}
	}
}

// TestABMCThatReportsOffAfterPowerOnIsWaitedFor keeps the server Off, with
// the override armed, for a while after each power-on. The worker must not
// take that Off as the installer's end.
func TestABMCThatReportsOffAfterPowerOnIsWaitedFor(t *testing.T) {
	e := newMediaEnv(t)
	e.bmc.lag = 300 * time.Millisecond
	if result := e.run(t, "op-lag", 1); result.Phase != ops.PhaseSucceeded {
		t.Fatalf("result = %+v", result)
	}
	want := []string{"insert CD1", "insert CD2", "arm Cd", "reset On", "arm Hdd", "reset On", "eject CD1", "eject CD2"}
	if got := e.bmc.log(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("BMC requests = %v, want %v", got, want)
	}
}

// TestTheSweepDropsConfigMediaNothingReadsAgain leaves windows open with
// their config media, as a worker that died would: one whose custody moved
// on and one past its report deadline. The sweep drops both media, and so
// their tokens, without any request; a live window keeps its medium.
func TestTheSweepDropsConfigMediaNothingReadsAgain(t *testing.T) {
	e := newMediaEnv(t)
	e.custody.set("op-live", 3)
	for uid, window := range map[string]Window{
		"op-dead":    {Device: device, Operation: "op-dead", Fence: 1, ConfigMedium: []byte("token=one"), FetchExpires: time.Now().Add(time.Hour), ReportExpires: time.Now().Add(time.Hour)},
		"op-expired": {Device: device, Operation: "op-live", Fence: 3, ConfigMedium: []byte("token=two"), FetchExpires: time.Now().Add(-time.Hour), ReportExpires: time.Now().Add(-time.Minute)},
		"op-live":    {Device: device, Operation: "op-live", Fence: 3, ConfigMedium: []byte("token=three"), FetchExpires: time.Now().Add(time.Hour), ReportExpires: time.Now().Add(time.Hour)},
	} {
		if err := e.central.save(Key(uid), window); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.central.RunSweeps(ctx, 10*time.Millisecond, func(err error) { t.Error(err) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		dead, _, _ := e.central.load(Key("op-dead"))
		expired, _, _ := e.central.load(Key("op-expired"))
		if dead.ConfigMedium == nil && expired.ConfigMedium == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep left a config medium no window can serve")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if live, _, _ := e.central.load(Key("op-live")); live.ConfigMedium == nil {
		t.Fatal("the sweep dropped a live window's config medium")
	}
}

// TestTheWorkerAPIRequiresTheSiteWorkersIdentity calls every worker API
// endpoint with no client certificate and with another site's: each is
// refused, and nothing is opened.
func TestTheWorkerAPIRequiresTheSiteWorkersIdentity(t *testing.T) {
	e := newMediaEnv(t)
	e.custody.set("op-auth", 1)
	key := Key("op-auth")
	open, _ := json.Marshal(OpenRequest{Device: device, Operation: "op-auth", Fence: 1, BootDigest: strings.Repeat("b", 64), CapabilitySHA256: digest("c"), TokenSHA256: digest("t"),
		ConfigMedium: []byte("medium"), FetchExpires: time.Now().Add(time.Minute), ReportExpires: time.Now().Add(2 * time.Minute), ReportFrom: []string{"192.0.2.50/32"}})
	call := func(method, path, body, identity string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:1"
		if identity != "" {
			e.workers.asWorker(t, request, identity)
		}
		recorder := httptest.NewRecorder()
		e.central.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	for _, identity := range []string{"", "fleet-site-worker:another-site"} {
		want := http.StatusForbidden
		if identity == "" {
			want = http.StatusUnauthorized
		}
		if code := call(http.MethodPut, "/v1/windows/"+key, string(open), identity); code != want {
			t.Fatalf("open as %q = %d, want %d", identity, code, want)
		}
		if code := call(http.MethodGet, "/v1/devices/"+device, "", identity); code != want {
			t.Fatalf("device status as %q = %d, want %d", identity, code, want)
		}
	}
	if _, found, _ := e.central.load(key); found {
		t.Fatal("a refused client opened a window")
	}
	if code := call(http.MethodPut, "/v1/windows/"+key, string(open), workerIdentity); code != http.StatusCreated {
		t.Fatalf("open as the site's worker = %d", code)
	}
	for _, identity := range []string{"", "fleet-site-worker:another-site"} {
		if code := call(http.MethodGet, "/v1/windows/"+key, "", identity); code == http.StatusOK {
			t.Fatalf("window status served to %q", identity)
		}
		if code := call(http.MethodPost, "/v1/windows/"+key+"/close", "", identity); code == http.StatusOK {
			t.Fatalf("window close accepted from %q", identity)
		}
	}
	if window := e.window(t, "op-auth"); window.Closed {
		t.Fatal("a refused client closed the window")
	}
}

// TestARefusedWindowOpenFailsOnlyWhenNothingReachedTheBMC runs a worker
// without a client certificate, so the media service refuses its window.
// With nothing journaled for the BMC the operation fails. With a BMC step
// already journaled, as after a crash, it is unknown.
func TestARefusedWindowOpenFailsOnlyWhenNothingReachedTheBMC(t *testing.T) {
	for name, wantPhase := range map[string]ops.Phase{"nothing at the BMC": ops.PhaseFailed, "a BMC step journaled": ops.PhaseOutcomeUnknown} {
		t.Run(name, func(t *testing.T) {
			e := newMediaEnv(t)
			access := e.runner.Access.(site.RedfishAccess)
			access.MediaIdentity.(*mediaIdentity).identity = nil
			assignment := e.assignment("op-refused", 1)
			if wantPhase == ops.PhaseOutcomeUnknown {
				if err := e.store.Raise(device, 1); err != nil {
					t.Fatal(err)
				}
				if _, _, err := e.store.IntendWithPayload(device, 1, "op-refused", "media-clear-cd1", nil); err != nil {
					t.Fatal(err)
				}
			}
			result, err := e.runner.Run(context.Background(), assignment)
			if err != nil {
				t.Fatal(err)
			}
			if result.Phase != wantPhase {
				t.Fatalf("result = %+v, want %s", result, wantPhase)
			}
			if wantPhase == ops.PhaseFailed && result.Reason != custody.ReasonMediaWindowRefused {
				t.Fatalf("reason = %s", result.Reason)
			}
			if len(e.bmc.log()) != 0 {
				t.Fatalf("a refused window reached the BMC: %v", e.bmc.log())
			}
		})
	}
}

// TestAReportMustNameExactlyThePinnedDownloads pins one installer download.
// A report naming it by its digest succeeds; a report that also names a
// download the operation never pinned does not.
func TestAReportMustNameExactlyThePinnedDownloads(t *testing.T) {
	pinned := ops.DownloadInput{Name: "installer", Reference: "registry.example/installer@sha256:" + strings.Repeat("a", 64)}
	e := newMediaEnv(t)
	e.downloads = []ops.DownloadInput{pinned}
	if result := e.run(t, "op-pinned", 1); result.Phase != ops.PhaseSucceeded {
		t.Fatalf("result = %+v", result)
	}
	if report := e.window(t, "op-pinned").Report; report == nil || len(report.Downloads) != 1 || report.Downloads[0].Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("report = %+v", report)
	}
	e = newMediaEnv(t)
	e.downloads = []ops.DownloadInput{pinned}
	e.bmc.extraDownloads = []ReportedDownload{{Name: "extension", Digest: "sha256:" + strings.Repeat("b", 64)}}
	if result := e.run(t, "op-unpinned", 1); result.Phase != ops.PhaseOutcomeUnknown || e.journaled("op-unpinned")["media-report-verified"] != "" {
		t.Fatalf("result = %+v", result)
	}
}

// failingIdentity is a site worker identity Secret that cannot be read.
type failingIdentity struct{}

func (failingIdentity) MediaClient(context.Context, string) (*tls.Certificate, error) {
	return nil, errors.New("the identity Secret is malformed")
}

// TestOnlyVirtualMediaReadsTheWorkerIdentity breaks the site worker's media
// identity. A virtual media install is refused before anything is sent.
func TestOnlyVirtualMediaReadsTheWorkerIdentity(t *testing.T) {
	e := newMediaEnv(t)
	access := e.runner.Access.(site.RedfishAccess)
	access.MediaIdentity = failingIdentity{}
	e.runner.Access = access
	result := e.run(t, "op-broken-identity", 1)
	if result.Phase != ops.PhaseFailed || result.Reason != site.ReasonCredentialMissing || len(e.bmc.log()) != 0 {
		t.Fatalf("result = %+v, BMC %v", result, e.bmc.log())
	}
}

// TestTheSweepSkipsAnUnreadableWindow puts an unreadable window file first
// in the pass. The sweep counts and reports it, and still drops the stale
// window after it.
func TestTheSweepSkipsAnUnreadableWindow(t *testing.T) {
	e := newMediaEnv(t)
	e.custody.set("op-next", 2)
	if err := os.MkdirAll(filepath.Join(e.central.Root, "windows"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Window files sweep in name order; this one sorts first.
	if err := os.WriteFile(filepath.Join(e.central.Root, "windows", "0000000000000000.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := Window{Device: device, Operation: "op-dead", Fence: 1, ConfigMedium: []byte("token=one"), FetchExpires: time.Now().Add(time.Hour), ReportExpires: time.Now().Add(time.Hour)}
	if err := e.central.save(Key("op-dead"), stale); err != nil {
		t.Fatal(err)
	}
	result, err := e.central.Sweep(context.Background())
	if err == nil || !strings.Contains(err.Error(), "skipped 1") {
		t.Fatalf("sweep error = %v", err)
	}
	if result != (SweepResult{Dropped: 1, Skipped: 1}) {
		t.Fatalf("sweep = %+v", result)
	}
	if window := e.window(t, "op-dead"); window.ConfigMedium != nil {
		t.Fatal("an unreadable window stopped the sweep before a stale one")
	}
}
