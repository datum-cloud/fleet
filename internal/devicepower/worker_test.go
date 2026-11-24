package devicepower

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/redfish"
)

// fakeServer is one emulated System. Every Reset is counted, whatever its
// response.
type fakeServer struct {
	mu          sync.Mutex
	state       string
	lastReset   time.Time
	resetTypes  []string
	uuid        string
	resets      []string
	findErr     error
	observeErr  error
	resetErr    error // the response the worker sees
	applyEffect bool  // whether the reset takes effect despite resetErr
	noResetTime bool
	onReset     func()
	clock       func() time.Time
}

func newServer(state string) *fakeServer {
	return &fakeServer{state: state, uuid: uuidA, resetTypes: []string{"On", "ForceOff", "ForceRestart"}, applyEffect: true,
		lastReset: t0.Add(-time.Hour), clock: func() time.Time { return t0 }}
}

func (f *fakeServer) FindPowerSystem(_ context.Context, uuid string) (redfish.PowerSystem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.findErr != nil {
		return redfish.PowerSystem{}, f.findErr
	}
	if uuid != f.uuid {
		return redfish.PowerSystem{}, redfish.ErrIdentity
	}
	return redfish.PowerSystem{Path: "/redfish/v1/Systems/1", ID: "1", UUID: uuid, ResetTarget: "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", ResetTypes: f.resetTypes}, nil
}

func (f *fakeServer) ObservePower(_ context.Context, _ redfish.PowerSystem) (redfish.PowerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observeErr != nil {
		return redfish.PowerState{}, f.observeErr
	}
	p := redfish.PowerState{State: f.state, ObservedAt: f.clock()}
	if !f.noResetTime {
		p.LastResetTime = f.lastReset
	}
	return p, nil
}

func (f *fakeServer) ResetPower(_ context.Context, _ redfish.PowerSystem, kind string) error {
	f.mu.Lock()
	f.resets = append(f.resets, kind)
	if f.applyEffect {
		switch kind {
		case "On":
			f.state = "On"
		case "ForceOff":
			f.state = "Off"
		case "ForceRestart", "PowerCycle":
			f.state = "On"
		}
		if kind != "ForceOff" {
			f.lastReset = f.clock().Add(time.Second)
		}
	}
	hook := f.onReset
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return f.resetErr
}

func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resets)
}

// workerHarness is one stage record and the credential files a worker sees.
type workerHarness struct {
	client  client.Client
	key     types.NamespacedName
	dir     string
	server  *fakeServer
	now     time.Time
	bmcMade int
	mu      sync.Mutex
}

func newWorkerHarness(t *testing.T, action inventory.PowerAction, server *fakeServer, funcs *interceptor.Funcs) *workerHarness {
	t.Helper()
	scheme, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	op := operation("op", "device-a", action)
	in := Inputs{OperationUID: string(op.UID), Project: project, Namespace: deviceNS, Operation: op.Name, Device: "device-a", Endpoint: endpoint,
		SystemUUID: uuidA, PublicKeySHA256: keyA, Action: action, CapacityCheckedAt: metav1.NewTime(t0),
		ObserveTimeout: metav1.Duration{Duration: 30 * time.Second}, RequestTimeout: metav1.Duration{Duration: 5 * time.Second}}
	rec := record(siteNS, in)
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rec)
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	h := &workerHarness{client: builder.Build(), key: types.NamespacedName{Namespace: siteNS, Name: rec.Name}, dir: t.TempDir(), server: server, now: t0.Add(10 * time.Second)}
	server.clock = h.clock
	h.writeCredential(t, "operator", "synthetic-secret", keyA)
	return h
}

func (h *workerHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *workerHarness) writeCredential(t *testing.T, user, password, pin string) {
	t.Helper()
	for k, v := range map[string]string{"username": user, "password": password, "tlsPublicKeySHA256": pin} {
		if err := os.WriteFile(filepath.Join(h.dir, k), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *workerHarness) worker(pod string) *Worker {
	return &Worker{Records: h.client, Record: h.key, PodUID: pod, CredentialDir: h.dir, Poll: 2 * time.Second,
		NewBMC: func(endpoint, user, password, pin string, _ time.Duration) (BMC, error) {
			h.mu.Lock()
			h.bmcMade++
			h.mu.Unlock()
			if endpoint != "https://192.0.2.10:443" || user != "operator" || pin != keyA {
				return nil, errors.New("unexpected BMC inputs")
			}
			return h.server, nil
		},
		Now: h.clock,
		Sleep: func(_ context.Context, d time.Duration) {
			h.mu.Lock()
			h.now = h.now.Add(d)
			h.mu.Unlock()
		}}
}

func (h *workerHarness) progress(t *testing.T) Progress {
	t.Helper()
	var cm corev1.ConfigMap
	if err := h.client.Get(context.Background(), h.key, &cm); err != nil {
		t.Fatal(err)
	}
	_, p, err := decodeRecord(&cm)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWorkerOutcomes(t *testing.T) {
	refused := redfish.Status{Code: 400}
	lost := errors.New("redfish: transport failure or unreadable response")
	for _, tc := range []struct {
		name    string
		action  inventory.PowerAction
		server  func() *fakeServer
		result  inventory.PowerResult
		reason  string
		resets  int
		sentAt  bool
		before  string
		after   string
		message string
	}{
		{name: "on", action: inventory.PowerOn, server: func() *fakeServer { return newServer("Off") },
			result: inventory.PowerSucceeded, reason: ReasonRequestedStateReached, resets: 1, sentAt: true, before: "Off", after: "On"},
		{name: "force off", action: inventory.PowerForceOff, server: func() *fakeServer { return newServer("On") },
			result: inventory.PowerSucceeded, reason: ReasonRequestedStateReached, resets: 1, sentAt: true, before: "On", after: "Off"},
		{name: "restart", action: inventory.PowerForceRestart, server: func() *fakeServer { return newServer("On") },
			result: inventory.PowerSucceeded, reason: ReasonResetObserved, resets: 1, sentAt: true, before: "On", after: "On"},
		{name: "already on", action: inventory.PowerOn, server: func() *fakeServer { return newServer("On") },
			result: inventory.PowerSucceeded, reason: ReasonAlreadyInState, before: "On", after: "On"},
		{name: "restart while off", action: inventory.PowerForceRestart, server: func() *fakeServer { return newServer("Off") },
			result: inventory.PowerFailed, reason: ReasonSystemOff, before: "Off"},
		{name: "power changing", action: inventory.PowerOn, server: func() *fakeServer { return newServer("PoweringOn") },
			result: inventory.PowerFailed, reason: ReasonPowerStateChanging, before: "PoweringOn"},
		{name: "replaced server", action: inventory.PowerOn, server: func() *fakeServer { s := newServer("Off"); s.uuid = uuidB; return s },
			result: inventory.PowerFailed, reason: inventory.ReasonSystemUUIDMismatch},
		{name: "action no longer offered", action: inventory.PowerForceRestart, server: func() *fakeServer { s := newServer("On"); s.resetTypes = []string{"On"}; return s },
			result: inventory.PowerFailed, reason: inventory.ReasonResetTypeNotOffered},
		{name: "wrong TLS key", action: inventory.PowerOn, server: func() *fakeServer { s := newServer("Off"); s.findErr = redfish.ErrPinMismatch; return s },
			result: inventory.PowerFailed, reason: ReasonPinMismatch},
		{name: "credential rejected", action: inventory.PowerOn, server: func() *fakeServer { s := newServer("Off"); s.findErr = redfish.ErrUnauthorized; return s },
			result: inventory.PowerFailed, reason: ReasonCredentialRejected},
		{name: "unreachable", action: inventory.PowerOn, server: func() *fakeServer { s := newServer("Off"); s.findErr = lost; return s },
			result: inventory.PowerFailed, reason: ReasonBMCUnreachable},
		{name: "refused", action: inventory.PowerOn, server: func() *fakeServer {
			s := newServer("Off")
			s.resetErr, s.applyEffect = refused, false
			return s
		}, result: inventory.PowerFailed, reason: ReasonCommandRefused, resets: 1, sentAt: true, before: "Off", after: "Off"},
		{name: "refused but changed", action: inventory.PowerOn, server: func() *fakeServer {
			s := newServer("Off")
			s.resetErr = refused
			return s
		}, result: inventory.PowerUnknown, reason: ReasonPowerChangedAnyway, resets: 1, sentAt: true, before: "Off", after: "On"},
		{name: "lost response then on", action: inventory.PowerOn, server: func() *fakeServer {
			s := newServer("Off")
			s.resetErr = lost
			return s
		}, result: inventory.PowerSucceeded, reason: ReasonRequestedStateReached, resets: 1, sentAt: true, before: "Off", after: "On",
			message: "the server reached On after the BMC's response was lost; the command was not resent"},
		{name: "lost response no effect", action: inventory.PowerOn, server: func() *fakeServer {
			s := newServer("Off")
			s.resetErr, s.applyEffect = lost, false
			return s
		}, result: inventory.PowerUnknown, reason: ReasonNotObserved, resets: 1, sentAt: true, before: "Off", after: "Off"},
		{name: "lost response after force off", action: inventory.PowerForceOff, server: func() *fakeServer {
			s := newServer("On")
			s.resetErr, s.applyEffect = lost, false
			return s
		}, result: inventory.PowerUnknown, reason: ReasonNotObserved, resets: 1, sentAt: true, before: "On", after: "On"},
		{name: "restart without reset time", action: inventory.PowerForceRestart, server: func() *fakeServer {
			s := newServer("On")
			s.noResetTime = true
			return s
		}, result: inventory.PowerUnknown, reason: ReasonNotObserved, resets: 1, sentAt: true, before: "On", after: "On"},
		{name: "lost restart response", action: inventory.PowerForceRestart, server: func() *fakeServer {
			s := newServer("On")
			s.resetErr, s.applyEffect = lost, false
			return s
		}, result: inventory.PowerUnknown, reason: ReasonNotObserved, resets: 1, sentAt: true, before: "On", after: "On"},
		{name: "server swapped after the command", action: inventory.PowerOn, server: func() *fakeServer {
			s := newServer("Off")
			s.applyEffect = false
			s.onReset = func() { s.mu.Lock(); s.observeErr = redfish.ErrIdentity; s.mu.Unlock() }
			return s
		}, result: inventory.PowerUnknown, reason: ReasonIdentityChanged, resets: 1, sentAt: true, before: "Off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := tc.server()
			h := newWorkerHarness(t, tc.action, server, nil)
			if err := h.worker("pod-1").Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			p := h.progress(t)
			if p.Stage != StageFinished || p.Result != tc.result || p.Reason != tc.reason || p.ClaimedBy != "pod-1" {
				t.Fatalf("progress = %+v", p)
			}
			if server.count() != tc.resets {
				t.Fatalf("%d resets reached the BMC, want %d", server.count(), tc.resets)
			}
			if (p.RequestSentAt != nil) != tc.sentAt {
				t.Fatalf("requestSentAt = %v", p.RequestSentAt)
			}
			if tc.before != "" && (p.Before == nil || p.Before.State != tc.before) {
				t.Fatalf("before = %+v", p.Before)
			}
			if tc.after != "" && (p.After == nil || p.After.State != tc.after) {
				t.Fatalf("after = %+v", p.After)
			}
			if tc.message != "" && p.Message != tc.message {
				t.Fatalf("message = %q", p.Message)
			}
		})
	}
}

func TestWorkerRefusesBeforeContactingTheBMC(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*testing.T, *workerHarness)
		reason string
	}{
		{"stale start", func(_ *testing.T, h *workerHarness) { h.now = t0.Add(StaleAfter + time.Second) }, ReasonStale},
		{"no credential", func(t *testing.T, h *workerHarness) {
			for _, f := range []string{"username", "password", "tlsPublicKeySHA256"} {
				os.Remove(filepath.Join(h.dir, f))
			}
		}, ReasonCredentialNotFound},
		{"pin not approved", func(t *testing.T, h *workerHarness) { h.writeCredential(t, "operator", "synthetic-secret", "") }, ReasonPinNotApproved},
		{"credential for another key", func(t *testing.T, h *workerHarness) {
			h.writeCredential(t, "operator", "synthetic-secret", "bb"+keyA[2:])
		}, ReasonCredentialMismatch},
		{"malformed credential", func(t *testing.T, h *workerHarness) { h.writeCredential(t, "oper:ator", "synthetic-secret", keyA) }, ReasonCredentialInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkerHarness(t, inventory.PowerOn, newServer("Off"), nil)
			tc.setup(t, h)
			if err := h.worker("pod-1").Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if p := h.progress(t); p.Result != inventory.PowerFailed || p.Reason != tc.reason || p.RequestSentAt != nil {
				t.Fatalf("progress = %+v", p)
			}
			if h.bmcMade != 0 || h.server.count() != 0 {
				t.Fatalf("the BMC was contacted: clients %d, resets %d", h.bmcMade, h.server.count())
			}
		})
	}
}

// A second worker for one operation, started by mistake or by a recreated
// Job, stops before it contacts the BMC and leaves the first result alone.
func TestSecondWorkerStopsAtTheClaim(t *testing.T) {
	h := newWorkerHarness(t, inventory.PowerForceRestart, newServer("On"), nil)
	if err := h.worker("pod-1").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := h.progress(t)
	made := h.bmcMade
	if err := h.worker("pod-2").Run(context.Background()); !errors.Is(err, ErrNotClaimed) {
		t.Fatalf("second worker: %v", err)
	}
	if h.bmcMade != made || h.server.count() != 1 {
		t.Fatalf("second worker contacted the BMC: clients %d, resets %d", h.bmcMade, h.server.count())
	}
	if got := h.progress(t); got.ClaimedBy != "pod-1" || got.Result != first.Result || got.FinishedAt.Time != first.FinishedAt.Time {
		t.Fatalf("first result changed: %+v", got)
	}
}

// Workers started at once race only on the claim: exactly one sends.
func TestConcurrentWorkersSendOnce(t *testing.T) {
	h := newWorkerHarness(t, inventory.PowerForceRestart, newServer("On"), nil)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = h.worker("pod-" + string(rune('a'+i))).Run(context.Background())
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrNotClaimed):
			t.Fatal(err)
		}
	}
	if won != 1 || h.server.count() != 1 || h.progress(t).Result != inventory.PowerSucceeded {
		t.Fatalf("%d workers ran, %d resets, result %s", won, h.server.count(), h.progress(t).Result)
	}
}

// When the dispatcher fences a record (its worker looked lost), the worker's
// write-ahead conflicts and it sends nothing.
func TestFencedWorkerSendsNothing(t *testing.T) {
	server := newServer("Off")
	h := newWorkerHarness(t, inventory.PowerOn, server, nil)
	fenced := false
	server.findErr = nil
	w := h.worker("pod-1")
	inner := w.NewBMC
	w.NewBMC = func(endpoint, user, password, pin string, timeout time.Duration) (BMC, error) {
		// The dispatcher fences between the claim and the send.
		var cm corev1.ConfigMap
		if err := h.client.Get(context.Background(), h.key, &cm); err != nil {
			return nil, err
		}
		_, p, _ := decodeRecord(&cm)
		p.Stage, p.Result, p.Reason = StageAbandoned, inventory.PowerFailed, ReasonWorkerLost
		setProgress(&cm, p)
		if err := h.client.Update(context.Background(), &cm); err != nil {
			return nil, err
		}
		fenced = true
		return inner(endpoint, user, password, pin, timeout)
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("a fenced worker reported success")
	}
	if !fenced || server.count() != 0 {
		t.Fatalf("fenced %v, resets %d", fenced, server.count())
	}
	if p := h.progress(t); p.Stage != StageAbandoned || p.Reason != ReasonWorkerLost {
		t.Fatalf("fenced record changed: %+v", p)
	}
}

// If the write-ahead record cannot be saved, nothing is sent.
func TestUnrecordableSendIsNotSent(t *testing.T) {
	server := newServer("Off")
	updates := 0
	funcs := &interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		updates++
		if updates == 2 { // the claim succeeds; the write-ahead fails
			return errors.New("site API unavailable")
		}
		return c.Update(ctx, obj, opts...)
	}}
	h := newWorkerHarness(t, inventory.PowerOn, server, funcs)
	if err := h.worker("pod-1").Run(context.Background()); err == nil {
		t.Fatal("worker succeeded without recording the send")
	}
	if server.count() != 0 {
		t.Fatalf("%d resets were sent without a write-ahead record", server.count())
	}
	if p := h.progress(t); p.Stage != StageClaimed {
		t.Fatalf("stage = %s", p.Stage)
	}
}

func TestSendOutcomeClassification(t *testing.T) {
	for err, want := range map[error]string{
		nil:                       SendAnswered,
		redfish.ErrUnauthorized:   SendRefused,
		redfish.ErrPrecondition:   SendRefused,
		redfish.Status{Code: 400}: SendRefused,
		redfish.Status{Code: 409}: SendRefused,
		redfish.Status{Code: 500}: SendLost,
		redfish.Status{Code: 503}: SendLost,
		errors.New("timeout"):     SendLost,
		context.DeadlineExceeded:  SendLost,
	} {
		if got := sendOutcome(err); got != want {
			t.Errorf("sendOutcome(%v) = %s, want %s", err, got, want)
		}
	}
}
