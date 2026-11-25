package site

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
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
)

const (
	testUUID   = "00000000-0000-4000-8000-000000000001"
	otherUUID  = "00000000-0000-4000-8000-000000000002"
	testDevice = "default/server-example-001"
	testMAC    = "02:00:00:00:00:01"
)

// bmc is a TLS Redfish stub that records every request it receives.
type bmc struct {
	mu   sync.Mutex
	uuid string
	// bootTargets defaults to Pxe and Hdd.
	bootTargets []string
	requests    []string
	server      *httptest.Server
}

// newBMC serves a Redfish stub over TLS with its own freshly generated key.
// httptest's default certificate is shared by every test server, so each BMC
// gets its own.
func newBMC(t *testing.T, uuid string) *bmc {
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
	b := &bmc{uuid: uuid}
	b.server = httptest.NewUnstartedServer(b)
	b.server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	b.server.StartTLS()
	t.Cleanup(b.server.Close)
	return b
}

func (b *bmc) pin() string {
	certificate, _ := x509.ParseCertificate(b.server.TLS.Certificates[0].Certificate[0])
	sum := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

func (b *bmc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.requests = append(b.requests, r.Method+" "+r.URL.Path)
	uuid := b.uuid
	targets := b.bootTargets
	if targets == nil {
		targets = []string{"Pxe", "Hdd"}
	}
	b.mu.Unlock()
	path := "/redfish/v1/Systems/A"
	docs := map[string]any{
		"/redfish/v1/":        map[string]any{"@odata.id": "/redfish/v1", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}},
		"/redfish/v1/Systems": map[string]any{"Members": []any{map[string]string{"@odata.id": path}}},
		path: map[string]any{"@odata.id": path, "Id": "A", "UUID": uuid, "PowerState": "Off",
			"Boot":    map[string]any{"BootSourceOverrideEnabled": "Disabled", "BootSourceOverrideTarget": "None", "BootSourceOverrideTarget@Redfish.AllowableValues": targets},
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": path + "/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On", "ForceOff"}}}},
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	doc, ok := docs[strings.TrimSuffix(r.URL.Path, "/")]
	if r.URL.Path == "/redfish/v1/" {
		doc, ok = docs["/redfish/v1/"]
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(doc)
}

func (b *bmc) count(filter func(string) bool) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, request := range b.requests {
		if filter(request) {
			n++
		}
	}
	return n
}

func notGet(request string) bool { return !strings.HasPrefix(request, "GET ") }
func any_(string) bool           { return true }

type fixedCredentials struct {
	credential bmcdiscovery.Credential
	found      bool
}

func (f fixedCredentials) Lookup(context.Context, string) (bmcdiscovery.Credential, bool, error) {
	return f.credential, f.found, nil
}

// fakeKind journals one step per entry of steps and records each send.
type fakeKind struct {
	steps   []string
	sends   atomic.Int32
	block   chan struct{}
	observe func(context.Context, json.RawMessage) (bool, error)
	check   func(*Work)
}

func (k *fakeKind) Check(_ context.Context, work *Work) *custody.Refusal {
	if k.check != nil {
		k.check(work)
	}
	return nil
}

func (k *fakeKind) Run(ctx context.Context, work *Work) (any, error) {
	for _, step := range k.steps {
		err := work.Step(ctx, Step{Name: step, Payload: map[string]string{"step": step}, Send: func(ctx context.Context) error {
			k.sends.Add(1)
			if k.block != nil {
				select {
				case <-k.block:
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			}
			return nil
		}, Observe: k.observe})
		if err != nil {
			return nil, err
		}
	}
	return ops.InstallationEvidence{Steps: []ops.StepEvidence{{Name: "done"}}}, nil
}

type fixture struct {
	runner *Runner
	bmc    *bmc
	kind   *fakeKind
}

func newFixture(t *testing.T, uuid string) *fixture {
	t.Helper()
	store := newStore(t)
	b := newBMC(t, uuid)
	kind := &fakeKind{steps: []string{"power-off", "boot"}}
	runner := &Runner{
		Site: "site-example-a", Store: store, Results: NewResults(store),
		Access: &RedfishAccess{
			Credentials: fixedCredentials{credential: bmcdiscovery.Credential{Username: "operator", Password: "synthetic-secret", Pin: b.pin()}, found: true},
			Targets:     StaticTargets{testDevice: {AssetID: "server-example-001", Endpoint: b.server.URL, SystemID: "A", SystemUUID: testUUID, MAC: testMAC}},
		},
		Kinds: map[string]Kind{"DeviceInstallation": kind},
		Poll:  10 * time.Millisecond,
	}
	return &fixture{runner: runner, bmc: b, kind: kind}
}

func (f *fixture) access() *RedfishAccess { return f.runner.Access.(*RedfishAccess) }

func (f *fixture) assignment(fence int64, uid string) Assignment {
	inputs := json.RawMessage(`{"image":"debian-12"}`)
	sum := sha256.Sum256(inputs)
	return Assignment{
		Device:      testDevice,
		Operation:   ops.OperationReference{Kind: "DeviceInstallation", Name: "alloc-example-install-" + uid, UID: uid},
		Fence:       fence,
		AdmittedAt:  time.Now(),
		Deadline:    time.Now().Add(time.Minute),
		Snapshot:    ops.DeviceSnapshot{BMCEndpoint: f.bmc.server.URL, SystemUUID: testUUID, BootMACAddress: testMAC, BMCPublicKeySHA256: f.bmc.pin()},
		InputDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Inputs:      inputs,
	}
}

func TestRunSucceedsWithFencedTypedEvidence(t *testing.T) {
	f := newFixture(t, testUUID)
	result, err := f.runner.Run(context.Background(), f.assignment(3, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || result.Fence != 3 || result.Site != "site-example-a" || result.Device != "server-example-001" || f.kind.sends.Load() != 2 {
		t.Fatalf("result = %+v, sends %d", result, f.kind.sends.Load())
	}
	var evidence ops.InstallationEvidence
	if err := json.Unmarshal(result.Evidence, &evidence); err != nil || len(evidence.Steps) != 1 {
		t.Fatalf("evidence = %s", result.Evidence)
	}
	if _, stopped, _ := f.runner.Store.Current(testDevice); stopped != 3 {
		t.Fatalf("stop confirmation = %d", stopped)
	}
	journal, _ := f.runner.Store.Journal(testDevice)
	if len(journal) != 2 || journal[0].State != StepDone || journal[1].State != StepDone {
		t.Fatalf("journal = %+v", journal)
	}
}

// TestPinMismatchSendsNothing proves that a BMC presenting another key gets
// no request at all: the handshake is aborted before any request is written,
// so no credential reaches it.
func TestPinMismatchSendsNothing(t *testing.T) {
	f := newFixture(t, testUUID)
	impostor := newBMC(t, testUUID)
	// Enrollment and the snapshot name the impostor's endpoint, while the
	// approved pin and the snapshot's key are the real BMC's.
	f.access().Targets = StaticTargets{testDevice: {Endpoint: impostor.server.URL, SystemID: "A", SystemUUID: testUUID, MAC: testMAC}}
	assignment := f.assignment(1, "op-pin")
	assignment.Snapshot.BMCEndpoint = impostor.server.URL
	result, err := f.runner.Run(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonPublicKeyMismatch {
		t.Fatalf("result = %+v", result)
	}
	if n := impostor.count(any_); n != 0 {
		t.Fatalf("the impostor BMC received %d requests", n)
	}
	if f.kind.sends.Load() != 0 {
		t.Fatal("the kind sent a step")
	}
	// An approved pin that differs from the key discovery recorded is
	// refused before any connection.
	g := newFixture(t, testUUID)
	stale := g.assignment(1, "op-stale-key")
	stale.Snapshot.BMCPublicKeySHA256 = strings.Repeat("0", 64)
	result, _ = g.runner.Run(context.Background(), stale)
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonPublicKeyMismatch || g.bmc.count(any_) != 0 {
		t.Fatalf("result = %+v, requests %d", result, g.bmc.count(any_))
	}
}

// TestIdentityMismatchSendsNothing proves a BMC reporting another system is
// read but never changed.
func TestIdentityMismatchSendsNothing(t *testing.T) {
	f := newFixture(t, otherUUID)
	result, err := f.runner.Run(context.Background(), f.assignment(1, "op-identity"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseFailed || result.Reason != custody.ReasonDeviceIdentityMismatch {
		t.Fatalf("result = %+v", result)
	}
	if n := f.bmc.count(notGet); n != 0 || f.kind.sends.Load() != 0 {
		t.Fatalf("%d changing requests and %d steps reached the BMC", n, f.kind.sends.Load())
	}
	// Enrollment that disagrees with the snapshot is refused before the BMC
	// is contacted.
	g := newFixture(t, testUUID)
	moved := g.assignment(1, "op-moved")
	moved.Snapshot.BootMACAddress = "02:00:00:00:00:99"
	result, _ = g.runner.Run(context.Background(), moved)
	if result.Phase != ops.PhaseFailed || result.Reason != custody.ReasonDeviceIdentityMismatch || g.bmc.count(any_) != 0 {
		t.Fatalf("result = %+v, requests %d", result, g.bmc.count(any_))
	}
}

func TestPreEffectRefusalsSendNothing(t *testing.T) {
	cases := map[string]func(*fixture, *Assignment){
		ReasonCredentialMissing: func(f *fixture, _ *Assignment) { f.access().Credentials = fixedCredentials{} },
		ReasonPinNotApproved: func(f *fixture, _ *Assignment) {
			f.access().Credentials = fixedCredentials{credential: bmcdiscovery.Credential{Username: "operator", Password: "synthetic-secret"}, found: true}
		},
		ReasonInputMismatch:   func(_ *fixture, a *Assignment) { a.Inputs = json.RawMessage(`{"image":"other"}`) },
		ReasonNotEnrolled:     func(f *fixture, _ *Assignment) { f.access().Targets = StaticTargets{} },
		ReasonUnsupportedKind: func(_ *fixture, a *Assignment) { a.Operation.Kind = "DeviceTestUnknown" },
	}
	for reason, mutate := range cases {
		t.Run(reason, func(t *testing.T) {
			f := newFixture(t, testUUID)
			assignment := f.assignment(1, "op-"+reason)
			mutate(f, &assignment)
			result, err := f.runner.Run(context.Background(), assignment)
			if err != nil {
				t.Fatal(err)
			}
			if result.Phase != ops.PhaseFailed || result.Reason != reason || f.bmc.count(notGet) != 0 || f.kind.sends.Load() != 0 {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestAStaleAssignmentIsRefusedAndStoresNothing(t *testing.T) {
	f := newFixture(t, testUUID)
	if err := f.runner.Store.Raise(testDevice, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Run(context.Background(), f.assignment(8, "op-old")); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale assignment: %v", err)
	}
	if pending, _ := f.runner.Results.Pending(); len(pending) != 0 || f.bmc.count(any_) != 0 {
		t.Fatal("a stale assignment stored a result or reached the BMC")
	}
}

// TestANewerFenceStopsTheOlderWorker runs an older worker that blocks inside
// a step, then delivers a newer assignment. The newer worker raises the fence,
// waits for the older one to stop, and confirms the stop.
func TestANewerFenceStopsTheOlderWorker(t *testing.T) {
	f := newFixture(t, testUUID)
	f.kind.block = make(chan struct{})
	older := make(chan custody.SiteResult, 1)
	go func() {
		result, err := f.runner.Run(context.Background(), f.assignment(1, "op-older"))
		if err != nil {
			t.Error(err)
		}
		older <- result
	}()
	waitFor(t, func() bool { return f.kind.sends.Load() == 1 })
	// The newer admission is a DeviceRecovery: it only fences.
	newer := f.assignment(2, "op-recovery")
	newer.Operation.Kind = "DeviceRecovery"
	if err := f.runner.Fence(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	result := <-older
	if result.Phase != ops.PhaseOutcomeUnknown || result.Reason != ReasonSuperseded || result.Fence != 1 {
		t.Fatalf("older result = %+v", result)
	}
	if fence, stopped, _ := f.runner.Store.Current(testDevice); fence != 2 || stopped != 2 {
		t.Fatalf("fence %d stopped %d", fence, stopped)
	}
	// The older worker journaled nothing after the newer fence.
	journal, _ := f.runner.Store.Journal(testDevice)
	if len(journal) != 1 || journal[0].Step != "power-off" || journal[0].State != StepIntended {
		t.Fatalf("journal = %+v", journal)
	}
	if f.kind.sends.Load() != 1 {
		t.Fatalf("%d sends", f.kind.sends.Load())
	}
}

func TestAWorkerStopsAtItsDeadline(t *testing.T) {
	f := newFixture(t, testUUID)
	f.kind.block = make(chan struct{})
	assignment := f.assignment(1, "op-deadline")
	assignment.Deadline = time.Now().Add(300 * time.Millisecond)
	result, err := f.runner.Run(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseOutcomeUnknown || result.Reason != ReasonDeadline {
		t.Fatalf("result = %+v", result)
	}
	if time.Now().After(assignment.Deadline.Add(time.Second)) {
		t.Fatal("the worker ran past its deadline")
	}
}

func TestAReplayedStepIsObservedNotSentAgain(t *testing.T) {
	f := newFixture(t, testUUID)
	assignment := f.assignment(1, "op-replay")
	// A previous worker journaled the first step and crashed.
	if err := f.runner.Store.Raise(testDevice, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Store.Intend(testDevice, 1, "op-replay", "power-off"); err != nil {
		t.Fatal(err)
	}
	var observed atomic.Int32
	f.kind.observe = func(_ context.Context, payload json.RawMessage) (bool, error) {
		observed.Add(1)
		// A step journaled by an earlier worker carries no payload here.
		return payload == nil, nil
	}
	result, err := f.runner.Run(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || observed.Load() != 1 || f.kind.sends.Load() != 1 {
		t.Fatalf("result %+v, observed %d, sent %d", result, observed.Load(), f.kind.sends.Load())
	}
	// An observation that cannot prove the step leaves the outcome unknown.
	g := newFixture(t, testUUID)
	if err := g.runner.Store.Raise(testDevice, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := g.runner.Store.Intend(testDevice, 1, "op-replay", "power-off"); err != nil {
		t.Fatal(err)
	}
	g.kind.observe = func(context.Context, json.RawMessage) (bool, error) { return false, nil }
	result, _ = g.runner.Run(context.Background(), g.assignment(1, "op-replay"))
	if result.Phase != ops.PhaseOutcomeUnknown || result.Reason != ReasonStep || g.kind.sends.Load() != 0 {
		t.Fatalf("result %+v, sent %d", result, g.kind.sends.Load())
	}
}

func TestResultsAreDeletedOnceTheCoordinatorAnswers(t *testing.T) {
	f := newFixture(t, testUUID)
	for index, uid := range []string{"op-a", "op-b", "op-c"} {
		if _, err := f.runner.Run(context.Background(), f.assignment(int64(index+1), uid)); err != nil {
			t.Fatal(err)
		}
	}
	answers := map[string]error{"op-a": nil, "op-b": custody.ErrStaleFence, "op-c": errors.New("coordinator unreachable")}
	err := f.runner.Results.Deliver(context.Background(), func(_ context.Context, result custody.SiteResult) error {
		return answers[result.Operation.UID]
	})
	if err == nil {
		t.Fatal("the failed delivery was not reported")
	}
	pending, _ := f.runner.Results.Pending()
	if len(pending) != 1 || pending[0].Result.Operation.UID != "op-c" {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestAssignmentValidation(t *testing.T) {
	f := newFixture(t, testUUID)
	valid := f.assignment(1, "op")
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Assignment){
		"device":   func(a *Assignment) { a.Device = "server-example-001" },
		"nested":   func(a *Assignment) { a.Device = "default/a/b" },
		"uid":      func(a *Assignment) { a.Operation.UID = "" },
		"fence":    func(a *Assignment) { a.Fence = 0 },
		"deadline": func(a *Assignment) { a.Deadline = time.Time{} },
		"inputs":   func(a *Assignment) { a.Inputs = make(json.RawMessage, maxInputs+1) },
		"snapshot": func(a *Assignment) { a.Snapshot.BMCPublicKeySHA256 = "" },
	} {
		assignment := valid
		mutate(&assignment)
		if assignment.Validate() == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAResumedWorkerThatCannotReachTheBMCReportsUnknown proves that a
// refusal after an earlier worker journaled steps is not reported as "nothing
// sent".
func TestAResumedWorkerThatCannotReachTheBMCReportsUnknown(t *testing.T) {
	f := newFixture(t, testUUID)
	if err := f.runner.Store.Raise(testDevice, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Store.Intend(testDevice, 1, "op-resumed", "power-off"); err != nil {
		t.Fatal(err)
	}
	f.access().Credentials = fixedCredentials{}
	result, err := f.runner.Run(context.Background(), f.assignment(1, "op-resumed"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseOutcomeUnknown || result.Reason != ReasonCredentialMissing {
		t.Fatalf("result = %+v", result)
	}
	// A worker started after its deadline sends nothing.
	g := newFixture(t, testUUID)
	late := g.assignment(1, "op-late")
	late.AdmittedAt, late.Deadline = time.Now().Add(-time.Minute), time.Now().Add(-time.Second)
	result, _ = g.runner.Run(context.Background(), late)
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonDeadline || g.bmc.count(any_) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// startingKind must start within one second of admission.
type startingKind struct{ *fakeKind }

func (startingKind) StartBy() time.Duration { return time.Second }

func TestALateStartIsStaleAndSendsNothing(t *testing.T) {
	f := newFixture(t, testUUID)
	f.runner.Kinds["DeviceInstallation"] = startingKind{f.kind}
	late := f.assignment(1, "op-stale")
	late.AdmittedAt = time.Now().Add(-time.Minute)
	result, err := f.runner.Run(context.Background(), late)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonStale || f.bmc.count(any_) != 0 || f.kind.sends.Load() != 0 {
		t.Fatalf("result = %+v", result)
	}
	// A worker resuming steps an earlier worker sent is not stale.
	g := newFixture(t, testUUID)
	g.runner.Kinds["DeviceInstallation"] = startingKind{g.kind}
	if err := g.runner.Store.Raise(testDevice, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := g.runner.Store.Intend(testDevice, 1, "op-resume-late", "power-off"); err != nil {
		t.Fatal(err)
	}
	g.kind.observe = func(context.Context, json.RawMessage) (bool, error) { return true, nil }
	resumed := g.assignment(1, "op-resume-late")
	resumed.AdmittedAt = time.Now().Add(-time.Minute)
	if result, _ := g.runner.Run(context.Background(), resumed); result.Phase != ops.PhaseSucceeded {
		t.Fatalf("resumed result = %+v", result)
	}
}

func TestAStepPayloadIsJournaledAndReadBack(t *testing.T) {
	store := newStore(t)
	if err := store.Raise(testDevice, 1); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"powerBefore":"Off"}`)
	if _, _, err := store.IntendWithPayload(testDevice, 1, "op", "reset", payload); err != nil {
		t.Fatal(err)
	}
	_, recorded, err := store.IntendWithPayload(testDevice, 1, "op", "reset", json.RawMessage(`{"powerBefore":"changed"}`))
	if !errors.Is(err, ErrStepReplayed) || string(recorded) != string(payload) {
		t.Fatalf("replay = %s %v", recorded, err)
	}
	if _, _, err := store.IntendWithPayload(testDevice, 1, "op", "too-big", make(json.RawMessage, maxPayload+1)); err == nil {
		t.Fatal("an oversized payload was journaled")
	}
}

// TestIdentityIsSeparateFromInstallCapabilities proves the framework checks
// only the pin and the System, so a kind that needs no network boot, such as
// power, runs on a System that offers no boot override.
func TestIdentityIsSeparateFromInstallCapabilities(t *testing.T) {
	f := newFixture(t, testUUID)
	f.bmc.mu.Lock()
	f.bmc.bootTargets = []string{}
	f.bmc.mu.Unlock()
	var system redfish.System
	f.kind.check = func(work *Work) {
		if connection, ok := work.Connection.(*RedfishConnection); ok {
			system = connection.System
		}
	}
	result, err := f.runner.Run(context.Background(), f.assignment(1, "op-power-like"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || system.UUID != testUUID || len(system.BootTargets) != 0 {
		t.Fatalf("result = %+v, system = %+v", result, system)
	}
}
