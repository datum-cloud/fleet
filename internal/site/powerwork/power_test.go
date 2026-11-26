package powerwork

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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
)

const (
	uuid      = "00000000-0000-4000-8000-0000000000a1"
	otherUUID = "00000000-0000-4000-8000-0000000000b2"
	device    = "default/server-example-001"
	systemID  = "A"
	path      = "/redfish/v1/Systems/A"
)

// bmc simulates one server behind a TLS Redfish BMC, with the lab's power
// faults. It counts every reset request it receives, applied or not.
type bmc struct {
	mu     sync.Mutex
	power  string
	reset  time.Time
	resets int
	// offReads is how many reads of the System a restart reports Off for
	// before On. Counting reads, not time, keeps a slow worker from missing
	// the Off.
	offReads, offLeft int
	// hideReset hides LastResetTime, as some BMCs do.
	hideReset bool
	// offered are the advertised reset types.
	offered []string
	// uuid is what the System reports; a swap changes it.
	uuid string
	// lose applies the reset and withholds the response; drop withholds it
	// without applying it; refuse answers 400 without applying it.
	lose, drop, refuse bool
	hold               time.Duration
	// onReset runs after a reset request is counted.
	onReset func()
	server  *httptest.Server
}

func newBMC(t *testing.T, power string) *bmc {
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
	b := &bmc{power: power, reset: time.Now().Add(-time.Hour).UTC().Truncate(time.Second), offered: []string{"On", "ForceOff", "ForceRestart", "PowerCycle"}, uuid: uuid, hold: time.Second}
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

func (b *bmc) set(change func(*bmc)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	change(b)
}

func (b *bmc) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resets
}

// apply runs the reset's effect. Restarts and power cycles move the reset
// time and, when offReads is set, report Off for that many reads first.
func (b *bmc) apply(kind string) {
	switch kind {
	case "On":
		if b.power == "Off" {
			b.power, b.reset = "On", time.Now().UTC().Truncate(time.Second)
		}
	case "ForceOff":
		b.power = "Off"
	case "ForceRestart", "PowerCycle":
		b.reset = time.Now().UTC().Add(time.Second).Truncate(time.Second)
		if b.offReads <= 0 {
			b.power = "On"
			return
		}
		b.power, b.offLeft = "Off", b.offReads
	}
}

func (b *bmc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == path+"/Actions/ComputerSystem.Reset" {
		var body struct{ ResetType string }
		json.NewDecoder(r.Body).Decode(&body)
		b.mu.Lock()
		b.resets++
		lose, drop, refuse, hold, hook := b.lose, b.drop, b.refuse, b.hold, b.onReset
		if !drop && !refuse {
			b.apply(body.ResetType)
		}
		b.mu.Unlock()
		if hook != nil {
			hook()
		}
		switch {
		case refuse:
			w.WriteHeader(http.StatusBadRequest)
		case lose || drop:
			time.Sleep(hold)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	b.mu.Lock()
	system := map[string]any{"@odata.id": path, "Id": systemID, "UUID": b.uuid, "PowerState": b.power,
		"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": path + "/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": b.offered}}}
	if !b.hideReset {
		system["LastResetTime"] = b.reset.Format(time.RFC3339)
	}
	if b.offLeft > 0 && strings.TrimSuffix(r.URL.Path, "/") == path {
		if b.offLeft--; b.offLeft == 0 {
			b.power = "On"
		}
	}
	b.mu.Unlock()
	docs := map[string]any{
		"/redfish/v1":         map[string]any{"@odata.id": "/redfish/v1", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}},
		"/redfish/v1/Systems": map[string]any{"Members": []any{map[string]string{"@odata.id": path}}},
		path:                  system,
	}
	doc, ok := docs[strings.TrimSuffix(r.URL.Path, "/")]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(doc)
}

type credentials struct{ pin string }

func (c credentials) Lookup(context.Context, string) (bmcdiscovery.Credential, bool, error) {
	return bmcdiscovery.Credential{Username: "operator", Password: "synthetic-secret", Pin: c.pin}, true, nil
}

// runner is a site whose requests time out after 300ms, so a withheld
// response is a lost one.
func runner(t *testing.T, b *bmc) *site.Runner {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &site.Runner{
		Site: "site-example-a", Store: store, Results: site.NewResults(store),
		Access: site.RedfishAccess{Credentials: credentials{pin: b.pin()},
			Targets: site.StaticTargets{device: {Device: device, Endpoint: b.server.URL, SystemID: systemID, SystemUUID: uuid}},
			Dial: func(endpoint, username, password, pin string) (*redfish.Client, error) {
				return redfish.NewPinnedClient(endpoint, username, password, pin, 300*time.Millisecond)
			}},
		Kinds: map[string]site.Kind{"DevicePowerOperation": Power{Poll: 10 * time.Millisecond, ObserveTimeout: 2 * time.Second, Outage: time.Second}},
		Poll:  10 * time.Millisecond,
	}
}

func staff(action ops.PowerAction) ops.DevicePowerOperationSpec {
	return ops.DevicePowerOperationSpec{OperationEnvelope: ops.OperationEnvelope{Authority: ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: "qualification"}}},
		Action: action, ExpectedSystemUUID: uuid}
}

func assignment(b *bmc, uid string, spec ops.DevicePowerOperationSpec) site.Assignment {
	inputs, _ := json.Marshal(spec)
	sum := sha256.Sum256(inputs)
	return site.Assignment{
		Device: device, Operation: ops.OperationReference{Kind: "DevicePowerOperation", Name: "server-example-001-power-" + uid}, Fence: 1,
		AdmittedAt: time.Now(), Deadline: time.Now().Add(time.Minute),
		Snapshot:    ops.DeviceSnapshot{BMCEndpoint: b.server.URL, SystemUUID: uuid, BMCPublicKeySHA256: b.pin()},
		InputDigest: "sha256:" + hex.EncodeToString(sum[:]), Inputs: inputs,
	}
}

// requireEvidence decodes the result's evidence and checks it against the
// coordinator's power evidence hook.
func requireEvidence(t *testing.T, result custody.SiteResult, spec ops.DevicePowerOperationSpec) ops.PowerEvidence {
	t.Helper()
	if _, err := custody.DevicePowerOperationKind().Evidence(&ops.DevicePowerOperation{Spec: spec}, result.Evidence); err != nil {
		t.Fatalf("the coordinator would refuse the evidence %s: %v", result.Evidence, err)
	}
	var evidence ops.PowerEvidence
	if err := json.Unmarshal(result.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestEachActionSendsOneResetAndProvesItsResult(t *testing.T) {
	for _, tc := range []struct {
		name          string
		action        ops.PowerAction
		power, after  string
		offReads      int
		hideReset     bool
		resets        int
		proof         ops.ResetEvidence
		requestSentAt bool
	}{
		{name: "On from off", action: ops.PowerOn, power: "Off", after: "On", resets: 1, requestSentAt: true},
		{name: "ForceOff from on", action: ops.PowerForceOff, power: "On", after: "Off", resets: 1, requestSentAt: true},
		{name: "On when already on sends nothing", action: ops.PowerOn, power: "On", after: "On"},
		{name: "ForceOff when already off sends nothing", action: ops.PowerForceOff, power: "Off", after: "Off"},
		{name: "ForceRestart proven by the reset time", action: ops.PowerForceRestart, power: "On", after: "On", resets: 1, proof: ops.ResetLastResetTime, requestSentAt: true},
		{name: "PowerCycle proven by the reset time", action: ops.PowerCycle, power: "On", after: "On", resets: 1, proof: ops.ResetLastResetTime, requestSentAt: true},
		{name: "ForceRestart proven by seeing the server off", action: ops.PowerForceRestart, power: "On", after: "On", offReads: 2, hideReset: true, resets: 1, proof: ops.ResetObservedOff, requestSentAt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBMC(t, tc.power)
			b.offReads, b.hideReset = tc.offReads, tc.hideReset
			spec := staff(tc.action)
			result, err := runner(t, b).Run(context.Background(), assignment(b, "op-1", spec))
			if err != nil {
				t.Fatal(err)
			}
			if result.Phase != ops.PhaseSucceeded || b.count() != tc.resets {
				t.Fatalf("result = %+v, %d resets, want Succeeded with %d", result, b.count(), tc.resets)
			}
			evidence := requireEvidence(t, result, spec)
			if string(evidence.Power.Before.State) != tc.power || string(evidence.Power.After.State) != tc.after || evidence.ResetEvidence != tc.proof || (evidence.RequestSentAt != nil) != tc.requestSentAt {
				t.Fatalf("evidence = %s", result.Evidence)
			}
		})
	}
}

// A refusal never carries the BMC's own text.
func TestARefusalNeverCopiesTheBMCsPowerState(t *testing.T) {
	hostile := "PoweringOn https://bmc.example.invalid/secret"
	b := newBMC(t, hostile)
	result, err := runner(t, b).Run(context.Background(), assignment(b, "op-1", staff(ops.PowerOn)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonPowerStateNotSettled || strings.Contains(result.Message, "example.invalid") || strings.Contains(result.Message, "PoweringOn") {
		t.Fatalf("result = %+v", result)
	}
}

func TestRefusalsSendNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action ops.PowerAction
		power  string
		setup  func(*bmc, *site.Assignment)
		reason string
	}{
		{name: "a restart of a server that is off", action: ops.PowerForceRestart, power: "Off", reason: ReasonNotPoweredOn},
		{name: "a power cycle of a server that is off", action: ops.PowerCycle, power: "Off", reason: ReasonNotPoweredOn},
		{name: "a server powering on", action: ops.PowerForceOff, power: "PoweringOn", reason: ReasonPowerStateNotSettled},
		{name: "a server powering off", action: ops.PowerOn, power: "PoweringOff", reason: ReasonPowerStateNotSettled},
		{name: "an action the BMC no longer offers", action: ops.PowerCycle, power: "On", reason: ReasonResetTypeNotOffered,
			setup: func(b *bmc, _ *site.Assignment) { b.offered = []string{"On", "ForceOff", "ForceRestart"} }},
		{name: "a worker that starts after the start-by window", action: ops.PowerOn, power: "Off", reason: site.ReasonStale,
			setup: func(_ *bmc, a *site.Assignment) { a.AdmittedAt = time.Now().Add(-DefaultStartBy - time.Second) }},
		{name: "a BMC that reports another system", action: ops.PowerOn, power: "Off", reason: custody.ReasonDeviceIdentityMismatch,
			setup: func(b *bmc, _ *site.Assignment) { b.uuid = otherUUID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBMC(t, tc.power)
			work := assignment(b, "op-1", staff(tc.action))
			if tc.setup != nil {
				tc.setup(b, &work)
			}
			result, err := runner(t, b).Run(context.Background(), work)
			if err != nil {
				t.Fatal(err)
			}
			if result.Phase != ops.PhaseFailed || result.Reason != tc.reason || b.count() != 0 {
				t.Fatalf("result = %+v, %d resets, want Failed %s with none", result, b.count(), tc.reason)
			}
			// The coordinator records the reason only if the worker may raise
			// it.
			registry, err := custody.DefaultRegistry()
			if err != nil {
				t.Fatal(err)
			}
			if !registry.Declared("DevicePowerOperation", tc.reason, custody.RaisedByWorker) {
				t.Fatalf("%s is not declared for the power worker", tc.reason)
			}
		})
	}
}

// The expected system UUID is checked again at the site, against the
// System whose identity the framework verified.
func TestAnExpectedUUIDOtherThanTheVerifiedSystemIsRefused(t *testing.T) {
	b := newBMC(t, "Off")
	spec := staff(ops.PowerOn)
	spec.ExpectedSystemUUID = otherUUID
	result, err := runner(t, b).Run(context.Background(), assignment(b, "op-1", spec))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonSystemUUIDMismatch || b.count() != 0 {
		t.Fatalf("result = %+v, %d resets", result, b.count())
	}
}

func TestALostResponseIsObservedAndNeverResent(t *testing.T) {
	b := newBMC(t, "Off")
	b.lose = true
	spec := staff(ops.PowerOn)
	result, err := runner(t, b).Run(context.Background(), assignment(b, "op-1", spec))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || b.count() != 1 {
		t.Fatalf("result = %+v, %d resets", result, b.count())
	}
	requireEvidence(t, result, spec)
}

func TestAResetThatNeverTookEffectIsUnknown(t *testing.T) {
	for name, fault := range map[string]func(*bmc){
		"a request withheld and never applied": func(b *bmc) { b.drop = true },
		"a restart the BMC cannot prove":       func(b *bmc) { b.hideReset = true },
		"a request the BMC refused":            func(b *bmc) { b.refuse = true },
		// The BMC hid its reset time before the command and shows a stale
		// one after it. Without a baseline, that proves nothing.
		"a reset time with no baseline": func(b *bmc) {
			b.hideReset, b.drop = true, true
			b.onReset = func() { b.set(func(b *bmc) { b.hideReset = false }) }
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBMC(t, "On")
			fault(b)
			result, err := runner(t, b).Run(context.Background(), assignment(b, "op-1", staff(ops.PowerForceRestart)))
			if err != nil {
				t.Fatal(err)
			}
			// Something may have reached the server, and nothing proves what
			// it did, so the outcome is unknown and only one reset was sent.
			if result.Phase != ops.PhaseOutcomeUnknown || b.count() != 1 {
				t.Fatalf("result = %+v, %d resets", result, b.count())
			}
		})
	}
}

func TestASwapAfterTheResetIsUnknown(t *testing.T) {
	b := newBMC(t, "Off")
	b.onReset = func() { b.set(func(b *bmc) { b.uuid, b.power = otherUUID, "Off" }) }
	result, err := runner(t, b).Run(context.Background(), assignment(b, "op-1", staff(ops.PowerOn)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseOutcomeUnknown || result.Reason != ReasonSystemUUIDMismatch || b.count() != 1 {
		t.Fatalf("result = %+v, %d resets", result, b.count())
	}
}

// A worker stopped right after it sent the reset stores no result. Its
// replacement reads the journaled payload, observes the effect, and sends
// nothing.
func TestAResumedWorkerObservesInsteadOfResending(t *testing.T) {
	for name, fault := range map[string]func(*bmc){
		"the reset took effect":       func(*bmc) {},
		"the reset response was lost": func(b *bmc) { b.lose = true },
	} {
		t.Run(name, func(t *testing.T) {
			b := newBMC(t, "On")
			fault(b)
			r := runner(t, b)
			spec := staff(ops.PowerForceRestart)
			work := assignment(b, "op-1", spec)
			ctx, cancel := context.WithCancel(context.Background())
			b.onReset = cancel
			if _, err := r.Run(ctx, work); !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted: %v", err)
			}
			b.set(func(b *bmc) { b.onReset = nil })
			result, err := r.Run(context.Background(), work)
			if err != nil {
				t.Fatal(err)
			}
			if result.Phase != ops.PhaseSucceeded || b.count() != 1 {
				t.Fatalf("resumed result = %+v, %d resets", result, b.count())
			}
			evidence := requireEvidence(t, result, spec)
			if evidence.ResetEvidence != ops.ResetLastResetTime || evidence.Power.Before.State != ops.PowerStateOn {
				t.Fatalf("evidence = %s", result.Evidence)
			}
		})
	}
}

// A resumed worker whose reset never took effect does not send again: the
// outcome is unknown.
func TestAResumedWorkerNeverSendsASecondReset(t *testing.T) {
	b := newBMC(t, "On")
	b.drop = true
	r := runner(t, b)
	work := assignment(b, "op-1", staff(ops.PowerForceOff))
	ctx, cancel := context.WithCancel(context.Background())
	b.onReset = cancel
	if _, err := r.Run(ctx, work); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted: %v", err)
	}
	b.set(func(b *bmc) { b.onReset, b.drop = nil, false })
	result, err := r.Run(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseOutcomeUnknown || b.count() != 1 {
		t.Fatalf("resumed result = %+v, %d resets", result, b.count())
	}
}

// The journal is keyed by the operation and its admission fence, so a
// reset journaled under an earlier fence is never taken for this admission:
// the worker reads the server again and finds it already off.
func TestAResetJournaledUnderAnotherFenceIsNotReused(t *testing.T) {
	b := newBMC(t, "On")
	r := runner(t, b)
	spec := staff(ops.PowerForceOff)
	earlier := assignment(b, "op-1", spec)
	ctx, cancel := context.WithCancel(context.Background())
	b.onReset = cancel
	if _, err := r.Run(ctx, earlier); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted: %v", err)
	}
	b.set(func(b *bmc) { b.onReset = nil })
	later := assignment(b, "op-1", spec)
	later.Fence = 2
	result, err := r.Run(context.Background(), later)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || b.count() != 1 {
		t.Fatalf("result = %+v, %d resets", result, b.count())
	}
	// The earlier fence's journal would carry its send time and its read of
	// the server on.
	if evidence := requireEvidence(t, result, spec); evidence.RequestSentAt != nil || evidence.Power.Before.State != ops.PowerStateOff {
		t.Fatalf("evidence = %s", result.Evidence)
	}
}

// Two workers for one assignment, such as a cloned Pod, send one reset
// between them.
func TestTwoWorkersForOneAssignmentSendOneReset(t *testing.T) {
	b := newBMC(t, "On")
	b.offReads = 3
	r := runner(t, b)
	work := assignment(b, "op-1", staff(ops.PowerForceRestart))
	var group sync.WaitGroup
	results := make([]custody.SiteResult, 2)
	errs := make([]error, 2)
	for index := range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index], errs[index] = r.Run(context.Background(), work)
		}()
	}
	group.Wait()
	succeeded := 0
	for index := range 2 {
		switch {
		case errs[index] == nil && results[index].Phase == ops.PhaseSucceeded:
			succeeded++
		case errors.Is(errs[index], site.ErrAlreadyRunning):
		default:
			t.Fatalf("worker %d: %+v, %v", index, results[index], errs[index])
		}
	}
	if succeeded == 0 || b.count() != 1 {
		t.Fatalf("%d succeeded, %d resets", succeeded, b.count())
	}
}
