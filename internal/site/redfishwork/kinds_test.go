package redfishwork

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
)

const (
	uuid      = "00000000-0000-4000-8000-000000000001"
	device    = "default/server-example-001"
	mac       = "02:00:00:00:00:01"
	diskBytes = int64(64 << 30)
)

// lab simulates one server behind a TLS Redfish BMC. Powering it on with a
// one-time override runs that boot: a network boot is delivered and prints
// its receipt; a disk boot prints the disk receipt. Then it powers off.
type lab struct {
	mu        sync.Mutex
	t         *testing.T
	power     string
	enabled   string
	target    string
	ons       int
	onHook    func()
	ignoreOff bool
	server    *httptest.Server
	console   string
	bootRoot  string
	store     *site.Store
	operation string
	// silent keeps the server running with no console receipt.
	silent bool
}

func newLab(t *testing.T) *lab {
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
	l := &lab{t: t, power: "Off", enabled: "Disabled", target: "None", console: filepath.Join(t.TempDir(), "console.log"), bootRoot: t.TempDir()}
	l.server = httptest.NewUnstartedServer(l)
	l.server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	l.server.StartTLS()
	t.Cleanup(l.server.Close)
	return l
}

func (l *lab) pin() string {
	certificate, _ := x509.ParseCertificate(l.server.TLS.Certificates[0].Certificate[0])
	sum := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

func (l *lab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := "/redfish/v1/Systems/A"
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case r.Method == http.MethodPatch && r.URL.Path == path:
		var body struct{ Boot map[string]string }
		json.NewDecoder(r.Body).Decode(&body)
		l.enabled, l.target = body.Boot["BootSourceOverrideEnabled"], body.Boot["BootSourceOverrideTarget"]
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodPost && r.URL.Path == path+"/Actions/ComputerSystem.Reset":
		var body struct{ ResetType string }
		json.NewDecoder(r.Body).Decode(&body)
		switch body.ResetType {
		case "ForceOff":
			if !l.ignoreOff {
				l.power = "Off"
			}
		case "On":
			l.ons++
			l.power = "On"
			target := l.target
			l.enabled, l.target = "Disabled", "None"
			go l.boot(target)
			if l.onHook != nil {
				go l.onHook()
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	docs := map[string]any{
		"/redfish/v1":         map[string]any{"@odata.id": "/redfish/v1", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}},
		"/redfish/v1/Systems": map[string]any{"Members": []any{map[string]string{"@odata.id": path}}},
		path: map[string]any{"@odata.id": path, "Id": "A", "UUID": uuid, "PowerState": l.power,
			"Boot":    map[string]any{"BootSourceOverrideEnabled": l.enabled, "BootSourceOverrideTarget": l.target, "BootSourceOverrideTarget@Redfish.AllowableValues": []string{"Pxe", "Hdd"}},
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": path + "/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On", "ForceOff"}}}},
	}
	doc, ok := docs[strings.TrimSuffix(r.URL.Path, "/")]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(doc)
}

// boot runs the armed boot, as the server and the boot server would.
func (l *lab) boot(target string) {
	time.Sleep(30 * time.Millisecond)
	if l.silent {
		return
	}
	var boot Boot
	if _, err := l.store.GetRecord(device, "boot", l.operation, &boot); err != nil {
		l.t.Error(err)
		return
	}
	var line string
	switch {
	case target == "Pxe" && boot.Mode == "wipe":
		l.deliver()
		line = fmt.Sprintf("FLEET-WIPED:%s:%d", boot.Receipt, diskBytes)
	case target == "Pxe":
		l.deliver()
		line = "FLEET-INSTALLED:" + boot.Receipt
	case target == "Hdd":
		line = "FLEET-DISK-BOOT:" + boot.Receipt
	}
	file, err := os.OpenFile(l.console, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintln(file, line)
		file.Close()
	}
	l.mu.Lock()
	l.power = "Off"
	l.mu.Unlock()
}

func (l *lab) deliver() {
	dir := BootDirectory(l.bootRoot, l.operation)
	os.MkdirAll(dir, 0o700)
	log := "t admitted boot.ipxe bytes=1\n"
	for _, name := range []string{"boot.ipxe", "kernel", "installer.gz"} {
		log += "t write-complete " + name + " bytes=1\n"
	}
	os.WriteFile(filepath.Join(dir, "boot-delivery.log"), []byte(log), 0o600)
}

func (l *lab) onCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ons
}

type credentials struct{ pin string }

func (c credentials) Lookup(context.Context, string) (bmcdiscovery.Credential, bool, error) {
	return bmcdiscovery.Credential{Username: "operator", Password: "synthetic-secret", Pin: c.pin}, true, nil
}

func runner(t *testing.T, l *lab) *site.Runner {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.store = store
	config := Config{ConsoleRoot: filepath.Dir(l.console), BootRoot: l.bootRoot, Poll: 10 * time.Millisecond, OffGrace: 2 * time.Second}
	return &site.Runner{
		Site: "site-example-a", Store: store, Results: site.NewResults(store),
		Access: site.RedfishAccess{Credentials: credentials{pin: l.pin()},
			Targets: site.StaticTargets{device: {Device: "server-example-001", Endpoint: l.server.URL, SystemID: "A", SystemUUID: uuid, MAC: mac, DiskBytes: diskBytes, Console: filepath.Base(l.console)}}},
		Kinds: map[string]site.Kind{"DeviceInstallation": Installation{config}, "DeviceWipe": Wipe{config}},
		Poll:  10 * time.Millisecond,
	}
}

func assignment(l *lab, kind, uid string, fence int64, spec any) site.Assignment {
	inputs, _ := json.Marshal(spec)
	sum := sha256.Sum256(inputs)
	a := site.Assignment{
		Device: device, Operation: ops.OperationReference{Kind: kind, Name: "alloc-example-" + uid}, Fence: fence, AdmittedAt: time.Now(), Deadline: time.Now().Add(time.Minute),
		Snapshot:    ops.DeviceSnapshot{BMCEndpoint: l.server.URL, SystemUUID: uuid, BootMACAddress: mac, BMCPublicKeySHA256: l.pin()},
		InputDigest: "sha256:" + hex.EncodeToString(sum[:]), Inputs: inputs,
	}
	// The lab finds the boot record by the admission's site key.
	l.operation = a.Key()
	return a
}

func installSpec() ops.DeviceInstallationSpec {
	return ops.DeviceInstallationSpec{Image: ops.ImageInput{Name: "debian-12", Digest: "sha256:" + strings.Repeat("a", 64)}, BootInterface: "primary", TargetDisks: []string{"disk-0"}}
}

func TestInstallationRunsBothPhasesWithOneOnEach(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	result, err := r.Run(context.Background(), assignment(l, "DeviceInstallation", "op-install", 1, installSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || l.onCount() != 2 {
		t.Fatalf("result = %+v, %d power-ons", result, l.onCount())
	}
	var evidence ops.InstallationEvidence
	if err := json.Unmarshal(result.Evidence, &evidence); err != nil || len(evidence.Steps) != 2 || evidence.Steps[0].Name != "installer" || evidence.Steps[1].Name != "disk" {
		t.Fatalf("evidence = %s", result.Evidence)
	}
	if _, open, _ := OpenAdmission(r.Store, mac, time.Now()); open {
		t.Fatal("the boot admission stayed open after the install")
	}
}

func TestWipeChecksTheEnrolledDisk(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	spec := ops.DeviceWipeSpec{Step: 1, CleaningPolicyDigest: "sha256:" + strings.Repeat("b", 64), TargetDisks: []string{"disk-0"}}
	result, err := r.Run(context.Background(), assignment(l, "DeviceWipe", "op-wipe", 1, spec))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || l.onCount() != 1 {
		t.Fatalf("result = %+v, %d power-ons", result, l.onCount())
	}
	// A disk the site does not enroll is refused before anything is sent.
	m := newLab(t)
	s := runner(t, m)
	spec.TargetDisks = []string{"disk-1"}
	result, _ = s.Run(context.Background(), assignment(m, "DeviceWipe", "op-wipe-other", 1, spec))
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonTargetDisks || m.onCount() != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestBootstrapIsRefusedAtTheSiteToo(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	spec := installSpec()
	spec.Bootstrap = &ops.SealedInput{Ref: "sealed-example", Digest: "sha256:" + strings.Repeat("c", 64)}
	result, _ := r.Run(context.Background(), assignment(l, "DeviceInstallation", "op-bootstrap", 1, spec))
	if result.Phase != ops.PhaseFailed || l.onCount() != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestAnInterruptedWorkerResumesWithoutASecondOn kills the worker right
// after its first power-on. A new worker for the same assignment observes
// that the boot began instead of powering on again.
func TestAnInterruptedWorkerResumesWithoutASecondOn(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	work := assignment(l, "DeviceInstallation", "op-resume", 1, installSpec())
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	l.onHook = func() { once.Do(cancel) }
	// The worker is stopped from outside, as a Pod would be, and stores
	// no result.
	if _, err := r.Run(ctx, work); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted: %v", err)
	}
	l.onHook = nil
	second, err := r.Run(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if second.Phase != ops.PhaseSucceeded || l.onCount() != 2 {
		t.Fatalf("resumed result = %+v, %d power-ons, want one per phase", second, l.onCount())
	}
}

func TestAWorkerStoppedAtItsDeadlineClosesItsAdmission(t *testing.T) {
	l := newLab(t)
	l.silent = true
	r := runner(t, l)
	a := assignment(l, "DeviceInstallation", "00000000-0000-4000-8000-000000000301", 2, installSpec())
	a.Deadline = time.Now().Add(2 * time.Second)
	result, err := r.Run(context.Background(), a)
	if err != nil || result.Phase != ops.PhaseOutcomeUnknown || result.Reason != site.ReasonDeadline {
		t.Fatalf("install past its deadline = %+v, %v", result, err)
	}
	var boot Boot
	if found, err := r.Store.GetRecord(device, bootRecords, a.Key(), &boot); err != nil || !found || !boot.Closed || boot.Phase != "" {
		t.Fatalf("admission after the deadline = %+v, %v, %v", boot, found, err)
	}
	if _, open, _ := OpenAdmission(r.Store, mac, time.Now()); open {
		t.Fatal("a worker stopped at its deadline left its admission open")
	}
}

func TestOnlyTheCurrentFenceHasAnOpenBootAdmission(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	store := r.Store
	if err := store.Raise(device, 3); err != nil {
		t.Fatal(err)
	}
	boot := Boot{Operation: "op", Fence: 3, Mode: "install", MAC: mac, Receipt: "r", Token: strings.Repeat("0", 64), Expires: time.Now().Add(time.Minute), Phase: "installer"}
	if err := store.PutRecord(device, 3, "boot", "op", boot); err != nil {
		t.Fatal(err)
	}
	if _, open, err := OpenAdmission(store, strings.ToUpper(mac), time.Now()); err != nil || !open {
		t.Fatalf("open admission not found: %v", err)
	}
	if err := store.Raise(device, 4); err != nil {
		t.Fatal(err)
	}
	if _, open, _ := OpenAdmission(store, mac, time.Now()); open {
		t.Fatal("a superseded operation can still boot the server")
	}
	if _, open, _ := OpenAdmission(store, mac, time.Now().Add(2*time.Minute)); open {
		t.Fatal("an expired admission is open")
	}
}

var _ = redfish.ErrPinMismatch

// TestAWorkerInterruptedInTheDiskPhaseResumes kills the worker after the
// disk phase's power-on. The new worker skips the verified installer phase
// and observes the disk boot, with no further power-on.
func TestAWorkerInterruptedInTheDiskPhaseResumes(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	work := assignment(l, "DeviceInstallation", "op-disk-resume", 1, installSpec())
	ctx, cancel := context.WithCancel(context.Background())
	l.onHook = func() {
		if l.onCount() == 2 {
			cancel()
		}
	}
	if _, err := r.Run(ctx, work); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted: %v", err)
	}
	l.onHook = nil
	second, err := r.Run(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if second.Phase != ops.PhaseSucceeded || l.onCount() != 2 {
		t.Fatalf("resumed result = %+v, %d power-ons", second, l.onCount())
	}
}

func TestAServerThatNeverPowersOffEndsTheStep(t *testing.T) {
	l := newLab(t)
	l.power = "On"
	l.ignoreOff = true
	r := runner(t, l)
	installation := r.Kinds["DeviceInstallation"].(Installation)
	installation.PowerOffBound = 100 * time.Millisecond
	r.Kinds["DeviceInstallation"] = installation
	started := time.Now()
	result, err := r.Run(context.Background(), assignment(l, "DeviceInstallation", "op-stuck-on", 1, installSpec()))
	if err != nil {
		t.Fatal(err)
	}
	// The bound ends the step, long before the one-minute deadline.
	if result.Phase != ops.PhaseOutcomeUnknown || result.Reason != site.ReasonStep || l.onCount() != 0 || time.Since(started) > 10*time.Second {
		t.Fatalf("result = %+v, %d power-ons after %s", result, l.onCount(), time.Since(started))
	}
}
