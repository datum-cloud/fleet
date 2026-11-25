package bmcdiscovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/redfish"
)

const (
	project  = "example-provider-project"
	credNS   = "bmc-discovery"
	deviceNS = "default"
)

var (
	pinA = strings.Repeat("a", 64)
	pinB = strings.Repeat("b", 64)
	t0   = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

// fakeBMC answers per endpoint and records every call.
type fakeBMC struct {
	mu          sync.Mutex
	pins        map[string]string
	probeErr    error
	rootErr     error // returned with the certificate, as for a failed anonymous service-root read
	invErr      error
	inv         redfish.Inventory
	probes      []string
	inventories []string // "endpoint pin"
	active, max atomic.Int32
	hold        time.Duration
}

func (f *fakeBMC) Probe(_ context.Context, endpoint string) (redfish.Certificate, redfish.ServiceRoot, error) {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		m := f.max.Load()
		if n <= m || f.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(f.hold)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, endpoint)
	if f.probeErr != nil {
		return redfish.Certificate{}, redfish.ServiceRoot{}, f.probeErr
	}
	cert := redfish.Certificate{PublicKeySHA256: f.pins[endpoint], NotAfter: t0.Add(24 * time.Hour), SelfSigned: true}
	if f.rootErr != nil {
		return cert, redfish.ServiceRoot{}, f.rootErr
	}
	return cert, redfish.ServiceRoot{RedfishVersion: "1.15.0"}, nil
}

func (f *fakeBMC) Inventory(_ context.Context, endpoint, user, password, pin string) (redfish.Inventory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventories = append(f.inventories, endpoint+" "+pin)
	if f.invErr != nil {
		return redfish.Inventory{}, f.invErr
	}
	if user != "operator" || password != "synthetic-secret" {
		return redfish.Inventory{}, redfish.ErrUnauthorized
	}
	return f.inv, nil
}

func (f *fakeBMC) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.probes), len(f.inventories)
}

func system() redfish.InventorySystem {
	return redfish.InventorySystem{ID: "1", UUID: "00000000-0000-4000-8000-000000000001", Manufacturer: "Example Vendor", Model: "Example Family Example 1U",
		SerialNumber: "SYNTHETIC-0001", PowerState: "On", NetworkInterfaces: []redfish.NetworkInterface{{ID: "NIC.1", MACAddress: "00:00:5e:00:53:01"}}}
}

func newFakeBMC() *fakeBMC {
	return &fakeBMC{pins: map[string]string{"https://192.0.2.10:443": pinA, "https://192.0.2.11:443": pinB},
		inv: redfish.Inventory{Managers: []redfish.Manager{{ID: "BMC", FirmwareVersion: "1.0"}}, Systems: []redfish.InventorySystem{system()}}}
}

func device(name, endpoint string) *inventory.Device {
	return &inventory.Device{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: deviceNS, Generation: 1},
		Spec: inventory.DeviceSpec{ClaimRef: inventory.LocalReference{Name: "claim-" + name},
			Identity: inventory.DeviceIdentity{Manufacturer: "Example", Model: "Example 1U", Serial: "SYNTHETIC-0001"},
			BMC:      &inventory.BMC{Endpoint: endpoint}},
	}
}

func secret(deviceName, pin string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bmc-" + deviceName, Namespace: credNS, UID: types.UID("uid-" + deviceName), ResourceVersion: "1",
		Labels:      map[string]string{CredentialLabel: "true"},
		Annotations: map[string]string{ProjectAnnotation: project, NamespaceAnnotation: deviceNS, NameAnnotation: deviceName}},
		Data: map[string][]byte{UsernameKey: []byte("operator"), PasswordKey: []byte("synthetic-secret")}}
	if pin != "" {
		s.Data[PinKey] = []byte(pin)
	}
	return s
}

func condition(t *testing.T, status inventory.DeviceStatus, kind string) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(status.Conditions, kind)
	if c == nil {
		t.Fatalf("no %s condition in %+v", kind, status.Conditions)
	}
	return *c
}

func TestContactSendsCredentialOnlyForTheApprovedPin(t *testing.T) {
	for name, tc := range map[string]struct {
		pin       string
		credErr   error
		reason    string
		inventory bool
	}{
		"no pin approved":  {reason: ReasonPinNotApproved},
		"pin mismatch":     {pin: pinB, reason: ReasonPinMismatch},
		"secret conflict":  {pin: pinA, credErr: errCredentialConflict, reason: ReasonCredentialConflict},
		"secret malformed": {credErr: errCredentialInvalid, reason: ReasonCredentialInvalid},
		"pin approved":     {pin: pinA, reason: ReasonCredentialAccepted, inventory: true},
	} {
		t.Run(name, func(t *testing.T) {
			bmc := newFakeBMC()
			a := contact(context.Background(), bmc, "https://192.0.2.10:443", Credential{Username: "operator", Password: "synthetic-secret", Pin: tc.pin}, tc.credErr, t0)
			var status inventory.DeviceStatus
			apply(&status, a, 1)
			if got := condition(t, status, inventory.ConditionAuthenticated); got.Reason != tc.reason {
				t.Fatalf("Authenticated = %+v, want %s", got, tc.reason)
			}
			// The observed pin is recorded whatever happens next.
			if status.Discovery.TLS == nil || status.Discovery.TLS.PublicKeySHA256 != pinA || !status.Discovery.TLS.SelfSigned || status.Discovery.TLS.NotAfter == nil {
				t.Fatalf("TLS observation = %+v", status.Discovery.TLS)
			}
			if probes, invs := bmc.calls(); probes != 1 || (invs == 1) != tc.inventory {
				t.Fatalf("probes %d, credentialed reads %d, want credential sent = %v", probes, invs, tc.inventory)
			}
			if (status.Discovery.Inventory != nil) != tc.inventory {
				t.Fatalf("inventory = %+v", status.Discovery.Inventory)
			}
		})
	}
}

func TestContactOutcomes(t *testing.T) {
	cred := Credential{Username: "operator", Password: "synthetic-secret", Pin: pinA}
	for name, tc := range map[string]struct {
		endpoint              string
		probeErr, invErr      error
		reachable, discovered metav1.ConditionStatus
		reason                string
		uncertain             bool
	}{
		"endpoint not https": {endpoint: "http://192.0.2.10:80", reachable: metav1.ConditionFalse, discovered: metav1.ConditionFalse, reason: ReasonEndpointInvalid},
		"no handshake":       {probeErr: redfish.ErrNoHandshake, reachable: metav1.ConditionFalse, discovered: metav1.ConditionUnknown, reason: ReasonUnreachable, uncertain: true},
		"rejected":           {invErr: redfish.ErrUnauthorized, reachable: metav1.ConditionTrue, discovered: metav1.ConditionFalse, reason: ReasonCredentialRejected},
		"key changed":        {invErr: redfish.ErrPinMismatch, reachable: metav1.ConditionTrue, discovered: metav1.ConditionFalse, reason: ReasonPinMismatch},
		"read failed":        {invErr: errors.New("synthetic-secret leaked in error"), reachable: metav1.ConditionTrue, discovered: metav1.ConditionUnknown, reason: ReasonReadFailed, uncertain: true},
	} {
		t.Run(name, func(t *testing.T) {
			bmc := newFakeBMC()
			bmc.probeErr, bmc.invErr = tc.probeErr, tc.invErr
			endpoint := tc.endpoint
			if endpoint == "" {
				endpoint = "https://192.0.2.10:443"
			}
			a := contact(context.Background(), bmc, endpoint, cred, nil, t0)
			var status inventory.DeviceStatus
			apply(&status, a, 1)
			if r := condition(t, status, inventory.ConditionReachable); r.Status != tc.reachable {
				t.Fatalf("Reachable = %+v", r)
			}
			d := condition(t, status, inventory.ConditionDiscovered)
			if d.Status != tc.discovered || d.Reason != tc.reason || a.uncertain != tc.uncertain {
				t.Fatalf("Discovered = %+v, uncertain %v", d, a.uncertain)
			}
			for _, c := range status.Conditions {
				if strings.Contains(c.Message, "synthetic-secret") || strings.Contains(c.Message, "192.0.2") {
					t.Fatalf("condition message leaks a credential or URL: %q", c.Message)
				}
			}
		})
	}
}

func TestUnreadableServiceRootBacksOffWhateverTheCredential(t *testing.T) {
	for name, cred := range map[string]Credential{
		"pin not approved": {Username: "operator", Password: "synthetic-secret"},
		"pin refused":      {Username: "operator", Password: "synthetic-secret", Pin: pinB},
	} {
		t.Run(name, func(t *testing.T) {
			bmc := newFakeBMC()
			bmc.rootErr = errors.New("service root returned 503")
			a := contact(context.Background(), bmc, "https://192.0.2.10:443", cred, nil, t0)
			if !a.uncertain {
				t.Fatal("an unreadable service root must be retried with backoff")
			}
			var status inventory.DeviceStatus
			apply(&status, a, 1)
			if r := condition(t, status, inventory.ConditionReachable); r.Reason != ReasonServiceRootUnreadable {
				t.Fatalf("Reachable = %+v", r)
			}
			if _, inv := bmc.calls(); inv != 0 {
				t.Fatalf("credential was sent %d times without an approved pin", inv)
			}
		})
	}
}

func TestFailedAttemptRetainsEarlierObservations(t *testing.T) {
	bmc := newFakeBMC()
	cred := Credential{Username: "operator", Password: "synthetic-secret", Pin: pinA}
	var status inventory.DeviceStatus
	apply(&status, contact(context.Background(), bmc, "https://192.0.2.10:443", cred, nil, t0), 1)
	before := *status.Discovery.Inventory
	tlsBefore := *status.Discovery.TLS
	bmc.probeErr = redfish.ErrNoHandshake
	later := t0.Add(time.Hour)
	apply(&status, contact(context.Background(), bmc, "https://192.0.2.10:443", cred, nil, later), 1)
	if status.Discovery.Inventory == nil || !status.Discovery.Inventory.ObservedAt.Equal(&before.ObservedAt) || len(status.Discovery.Inventory.Systems) != 1 {
		t.Fatalf("inventory not retained: %+v", status.Discovery.Inventory)
	}
	if status.Discovery.TLS == nil || status.Discovery.TLS.PublicKeySHA256 != tlsBefore.PublicKeySHA256 || !status.Discovery.TLS.ObservedAt.Equal(&tlsBefore.ObservedAt) {
		t.Fatalf("TLS observation not retained: %+v", status.Discovery.TLS)
	}
	if !status.Discovery.LastAttemptAt.Time.Equal(later) {
		t.Fatalf("last attempt = %v", status.Discovery.LastAttemptAt)
	}
	if d := condition(t, status, inventory.ConditionDiscovered); d.Status != metav1.ConditionUnknown {
		t.Fatalf("Discovered after a transient failure = %+v, want Unknown", d)
	}
}

func TestIdentityComparison(t *testing.T) {
	endpoint := "https://192.0.2.10:443"
	observe := func(systems ...inventory.ObservedSystem) *inventory.DeviceStatus {
		return &inventory.DeviceStatus{Discovery: &inventory.DeviceDiscovery{Inventory: &inventory.ObservedInventory{Endpoint: endpoint, Systems: systems}}}
	}
	base := inventory.ObservedSystem{ID: "1", UUID: "00000000-0000-4000-8000-000000000001", Model: "PowerFamily Example 1U", SerialNumber: "SYNTHETIC-0001"}
	other := base
	other.ID, other.UUID, other.SerialNumber = "2", "00000000-0000-4000-8000-000000000002", "SYNTHETIC-0002"
	with := func(mutate func(*inventory.ObservedSystem)) inventory.ObservedSystem { s := base; mutate(&s); return s }
	for name, tc := range map[string]struct {
		status   *inventory.DeviceStatus
		identity inventory.DeviceIdentity
		want     metav1.ConditionStatus
		reason   string
	}{
		"not observed":          {status: &inventory.DeviceStatus{}, want: metav1.ConditionUnknown, reason: ReasonNotObserved},
		"serial and model":      {status: observe(base), want: metav1.ConditionTrue, reason: ReasonMatched},
		"service tag as SKU":    {status: observe(with(func(s *inventory.ObservedSystem) { s.SerialNumber, s.SKU = "CHASSIS-9", "synthetic-0001" })), want: metav1.ConditionTrue, reason: ReasonMatched},
		"serial differs":        {status: observe(with(func(s *inventory.ObservedSystem) { s.SerialNumber = "SYNTHETIC-0009" })), want: metav1.ConditionFalse, reason: ReasonSerialMismatch},
		"model differs":         {status: observe(with(func(s *inventory.ObservedSystem) { s.Model = "Example 2U" })), want: metav1.ConditionFalse, reason: ReasonModelMismatch},
		"model is not a word":   {status: observe(with(func(s *inventory.ObservedSystem) { s.Model = "Example 1UX" })), want: metav1.ConditionFalse, reason: ReasonModelMismatch},
		"picks by serial":       {status: observe(other, base), want: metav1.ConditionTrue, reason: ReasonMatched},
		"uuid differs":          {status: observe(other), identity: inventory.DeviceIdentity{Model: "Example 1U", SystemUUID: base.UUID}, want: metav1.ConditionFalse, reason: ReasonSystemUUIDMismatch},
		"uuid then serial":      {status: observe(with(func(s *inventory.ObservedSystem) { s.SerialNumber = "X" })), identity: inventory.DeviceIdentity{Model: "Example 1U", Serial: "SYNTHETIC-0001", SystemUUID: base.UUID}, want: metav1.ConditionFalse, reason: ReasonSerialMismatch},
		"model only, ambiguous": {status: observe(base, other), identity: inventory.DeviceIdentity{Model: "Example 1U", Unidentified: true}, want: metav1.ConditionUnknown, reason: ReasonAmbiguousSystem},
		"stale endpoint":        {status: &inventory.DeviceStatus{Discovery: &inventory.DeviceDiscovery{Inventory: &inventory.ObservedInventory{Endpoint: "https://192.0.2.99:443", Systems: []inventory.ObservedSystem{base}}}}, want: metav1.ConditionUnknown, reason: ReasonStaleObservation},
	} {
		t.Run(name, func(t *testing.T) {
			d := device("example-device-01", endpoint)
			if tc.identity.Model != "" {
				d.Spec.Identity = tc.identity
			}
			specBefore, _ := json.Marshal(d.Spec)
			got := identityCondition(tc.status, d)
			if got.Status != tc.want || got.Reason != tc.reason {
				t.Fatalf("IdentityMatched = %+v, want %s %s", got, tc.want, tc.reason)
			}
			if specAfter, _ := json.Marshal(d.Spec); !bytes.Equal(specBefore, specAfter) {
				t.Fatal("comparison changed spec")
			}
		})
	}
}

// harness is a reconciler over a fake Milo client that records every write.
type harness struct {
	r      *Reconciler
	c      client.Client
	bmc    *fakeBMC
	now    time.Time
	writes []string
}

func newHarness(t *testing.T, objects ...client.Object) *harness {
	t.Helper()
	scheme, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{bmc: newFakeBMC(), now: t0}
	var devices []client.Object
	var secrets []client.Object
	for _, o := range objects {
		if _, ok := o.(*corev1.Secret); ok {
			secrets = append(secrets, o)
		} else {
			devices = append(devices, o)
		}
	}
	record := func(kind string) { h.writes = append(h.writes, kind) }
	h.c = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&inventory.Device{}).WithObjects(devices...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			record("create")
			return c.Create(ctx, o, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			record("update")
			return c.Update(ctx, o, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			record("patch")
			return c.Patch(ctx, o, p, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			record("delete")
			return c.Delete(ctx, o, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			record(sub + " update")
			return c.SubResource(sub).Update(ctx, o, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			record(sub + " patch")
			return c.SubResource(sub).Patch(ctx, o, p, opts...)
		},
	}).Build()
	secretClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secrets...).Build()
	h.r = &Reconciler{Devices: h.c, Secrets: secretClient, Namespace: credNS, Project: project, BMC: h.bmc,
		Interval: time.Hour, RetryBase: 30 * time.Second, MinContact: time.Minute, Now: func() time.Time { return h.now }}
	return h
}

func (h *harness) reconcile(t *testing.T, name string) reconcile.Result {
	t.Helper()
	res, err := h.r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: deviceNS, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (h *harness) get(t *testing.T, name string) *inventory.Device {
	t.Helper()
	var d inventory.Device
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: deviceNS, Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestReconcileWritesOnlyStatus(t *testing.T) {
	d := device("example-device-01", "https://192.0.2.10:443")
	h := newHarness(t, d, secret("example-device-01", pinA))
	specBefore, _ := json.Marshal(h.get(t, "example-device-01").Spec)
	res := h.reconcile(t, "example-device-01")
	got := h.get(t, "example-device-01")
	if specAfter, _ := json.Marshal(got.Spec); !bytes.Equal(specBefore, specAfter) {
		t.Fatalf("spec changed:\n%s\n%s", specBefore, specAfter)
	}
	for _, w := range h.writes {
		if w != "status patch" {
			t.Fatalf("writes = %v, want status patches only", h.writes)
		}
	}
	if len(h.writes) != 1 || res.RequeueAfter != time.Hour {
		t.Fatalf("writes %v, requeue %v", h.writes, res.RequeueAfter)
	}
	for kind, reason := range map[string]string{inventory.ConditionReachable: ReasonServiceRootRead, inventory.ConditionAuthenticated: ReasonCredentialAccepted,
		inventory.ConditionDiscovered: ReasonInventoryRead, inventory.ConditionIdentityMatched: ReasonMatched, inventory.ConditionPublicKeyUnique: ReasonUnique} {
		if c := condition(t, got.Status, kind); c.Reason != reason || c.ObservedGeneration != 1 {
			t.Fatalf("%s = %+v, want %s", kind, c, reason)
		}
	}
	if got.Status.Discovery.Inventory.Systems[0].PowerState != "On" {
		t.Fatalf("inventory = %+v", got.Status.Discovery.Inventory)
	}
}

func TestReconcileSkipsDevicesWithoutBMCOrCredential(t *testing.T) {
	noBMC := device("no-bmc", "")
	noBMC.Spec.BMC = nil
	otherProject := secret("other-project", pinA)
	otherProject.Annotations[ProjectAnnotation] = "another-project"
	h := newHarness(t, noBMC, secret("no-bmc", pinA), device("no-secret", "https://192.0.2.10:443"), device("other-project", "https://192.0.2.11:443"), otherProject)
	for _, name := range []string{"no-bmc", "no-secret", "other-project", "missing"} {
		h.reconcile(t, name)
	}
	if probes, _ := h.bmc.calls(); probes != 0 || len(h.writes) != 0 {
		t.Fatalf("probes %d, writes %v; want none", probes, h.writes)
	}
}

func TestReconcileSchedulesContacts(t *testing.T) {
	h := newHarness(t, device("example-device-01", "https://192.0.2.10:443"), secret("example-device-01", ""))
	h.reconcile(t, "example-device-01")
	if got := condition(t, h.get(t, "example-device-01").Status, inventory.ConditionAuthenticated); got.Reason != ReasonPinNotApproved {
		t.Fatalf("Authenticated = %+v", got)
	}
	// An event before the interval re-derives without contacting the BMC.
	h.now = h.now.Add(10 * time.Minute)
	res := h.reconcile(t, "example-device-01")
	if probes, _ := h.bmc.calls(); probes != 1 || res.RequeueAfter != 50*time.Minute {
		t.Fatalf("probes %d requeue %v", probes, res.RequeueAfter)
	}
	// Approving the pin is a Secret change: discovery contacts at once and
	// sends the credential.
	var s corev1.Secret
	secrets := h.r.Secrets.(client.Client)
	if err := secrets.Get(context.Background(), types.NamespacedName{Namespace: credNS, Name: "bmc-example-device-01"}, &s); err != nil {
		t.Fatal(err)
	}
	s.Data[PinKey] = []byte(strings.ToUpper(pinA))
	if err := secrets.Update(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	h.reconcile(t, "example-device-01")
	if probes, invs := h.bmc.calls(); probes != 2 || invs != 1 {
		t.Fatalf("probes %d, credentialed reads %d after approval", probes, invs)
	}
	if got := condition(t, h.get(t, "example-device-01").Status, inventory.ConditionDiscovered); got.Status != metav1.ConditionTrue {
		t.Fatalf("Discovered = %+v", got)
	}
}

func TestReconcileBacksOffAndRespectsTheEndpointGap(t *testing.T) {
	h := newHarness(t, device("example-device-01", "https://192.0.2.10:443"), secret("example-device-01", pinA),
		device("example-device-02", "https://192.0.2.10:443"), secret("example-device-02", pinA))
	h.bmc.probeErr = redfish.ErrNoHandshake
	if res := h.reconcile(t, "example-device-01"); res.RequeueAfter != 30*time.Second {
		t.Fatalf("first retry after %v, want 30s", res.RequeueAfter)
	}
	// A second Device on the same endpoint waits for the per-endpoint gap.
	if res := h.reconcile(t, "example-device-02"); res.RequeueAfter != time.Minute {
		t.Fatalf("same-endpoint Device requeued after %v, want the 1m gap", res.RequeueAfter)
	}
	if probes, _ := h.bmc.calls(); probes != 1 {
		t.Fatalf("probes = %d, want the endpoint contacted once", probes)
	}
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		h.now = h.now.Add(time.Hour)
		if res := h.reconcile(t, "example-device-01"); res.RequeueAfter != want {
			t.Fatalf("retry %d after %v, want %v", i+2, res.RequeueAfter, want)
		}
	}
}

func TestSharedPublicKeyIsReportedOnEveryHolder(t *testing.T) {
	h := newHarness(t, device("example-device-01", "https://192.0.2.10:443"), secret("example-device-01", ""),
		device("example-device-02", "https://192.0.2.12:443"), secret("example-device-02", ""))
	h.bmc.pins["https://192.0.2.12:443"] = pinA // the same default key on two BMCs
	h.reconcile(t, "example-device-01")
	if got := condition(t, h.get(t, "example-device-01").Status, inventory.ConditionPublicKeyUnique); got.Status != metav1.ConditionTrue {
		t.Fatalf("before the second BMC is seen: %+v", got)
	}
	h.reconcile(t, "example-device-02")
	// The watch re-derives the first Device from retained status; no new
	// contact is needed.
	h.reconcile(t, "example-device-01")
	for _, name := range []string{"example-device-01", "example-device-02"} {
		got := condition(t, h.get(t, name).Status, inventory.ConditionPublicKeyUnique)
		if got.Status != metav1.ConditionFalse || got.Reason != ReasonSharedPublicKey {
			t.Fatalf("%s PublicKeyUnique = %+v", name, got)
		}
	}
	if probes, invs := h.bmc.calls(); probes != 2 || invs != 0 {
		t.Fatalf("probes %d, credentialed reads %d; first contact sends no credential", probes, invs)
	}
}

func TestRunOnceWritesNothingAndBoundsConcurrency(t *testing.T) {
	var objects []client.Object
	bmc := newFakeBMC()
	bmc.hold = 20 * time.Millisecond
	for i := range 12 {
		name := fmt.Sprintf("example-device-%02d", i)
		endpoint := fmt.Sprintf("https://192.0.2.%d:443", 20+i)
		bmc.pins[endpoint] = pinA
		objects = append(objects, device(name, endpoint), secret(name, ""))
	}
	h := newHarness(t, objects...)
	var out bytes.Buffer
	c := Config{Project: project, DeviceNamespaces: []string{deviceNS}, CredentialNamespace: credNS, Concurrency: 3}
	if err := RunOnce(context.Background(), c, h.c, h.r.Secrets, bmc, &out); err != nil {
		t.Fatal(err)
	}
	if len(h.writes) != 0 {
		t.Fatalf("--once wrote %v", h.writes)
	}
	if m := bmc.max.Load(); m > 3 || m < 2 {
		t.Fatalf("max concurrent probes = %d, want at most 3", m)
	}
	var results []OnceResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil || len(results) != 12 {
		t.Fatalf("results %d: %v\n%s", len(results), err, out.String())
	}
	for _, r := range results {
		if r.Status.Discovery.TLS.PublicKeySHA256 != pinA || condition(t, r.Status, inventory.ConditionPublicKeyUnique).Reason != ReasonSharedPublicKey ||
			condition(t, r.Status, inventory.ConditionAuthenticated).Reason != ReasonPinNotApproved {
			t.Fatalf("result %+v", r)
		}
	}
}

func TestSelectCredential(t *testing.T) {
	if _, found, _ := selectCredential(nil); found {
		t.Fatal("found a credential in no Secrets")
	}
	good := secret("example-device-01", strings.ToUpper(pinA))
	cred, found, err := selectCredential([]corev1.Secret{*good})
	if !found || err != nil || cred.Pin != pinA || cred.Username != "operator" || cred.Version == "" {
		t.Fatalf("cred = %+v %v %v", cred, found, err)
	}
	badPin := secret("example-device-01", "not-a-pin")
	if _, _, err := selectCredential([]corev1.Secret{*badPin}); !errors.Is(err, errCredentialInvalid) {
		t.Fatalf("bad pin err = %v", err)
	}
	noPassword := secret("example-device-01", "")
	delete(noPassword.Data, PasswordKey)
	if _, _, err := selectCredential([]corev1.Secret{*noPassword}); !errors.Is(err, errCredentialInvalid) {
		t.Fatalf("missing password err = %v", err)
	}
	second := secret("example-device-01", "")
	second.Name, second.UID = "duplicate", "uid-duplicate"
	if cred, _, err := selectCredential([]corev1.Secret{*good, *second}); !errors.Is(err, errCredentialConflict) || cred.Password != "" {
		t.Fatalf("conflict = %+v %v", cred, err)
	}
}

func TestConfigValidation(t *testing.T) {
	c := Config{Project: project, MiloKubeconfig: "/k", DeviceNamespaces: []string{deviceNS}, CredentialNamespace: credNS}
	c.Default()
	if err := c.Validate(); err != nil || c.Interval.Duration != time.Hour || c.Concurrency != 4 {
		t.Fatalf("defaults %+v: %v", c, err)
	}
	for name, mutate := range map[string]func(*Config){
		"no project":       func(c *Config) { c.Project = "" },
		"no namespaces":    func(c *Config) { c.DeviceNamespaces = nil },
		"short interval":   func(c *Config) { c.Interval.Duration = time.Second },
		"zero concurrency": func(c *Config) { c.Concurrency = -1 },
	} {
		bad := c
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// TestEndToEndOverTLS drives the real Redfish client against a TLS service:
// first contact records the pin without sending a credential; approval of
// that pin sends it and reads the inventory.
func TestEndToEndOverTLS(t *testing.T) {
	var mu sync.Mutex
	var log []string
	docs := map[string]string{
		"/redfish/v1/":             `{"RedfishVersion":"1.15.0","Systems":{"@odata.id":"/redfish/v1/Systems"},"Managers":{"@odata.id":"/redfish/v1/Managers"}}`,
		"/redfish/v1/Managers":     `{"Members":[{"@odata.id":"/redfish/v1/Managers/BMC"}]}`,
		"/redfish/v1/Managers/BMC": `{"Id":"BMC","Model":"Example BMC","FirmwareVersion":"1.2.3"}`,
		"/redfish/v1/Systems":      `{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`,
		"/redfish/v1/Systems/1": `{"Id":"1","UUID":"00000000-0000-4000-8000-000000000001","Model":"Example 1U","SerialNumber":"SYNTHETIC-0001","PowerState":"Off",
			"Actions":{"#ComputerSystem.Reset":{"target":"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset","@Redfish.ActionInfo":"/redfish/v1/Systems/1/ResetActionInfo"}}}`,
		"/redfish/v1/Systems/1/ResetActionInfo": `{"Parameters":[{"Name":"ResetType","Required":"true","DataType":"String","AllowableValues":["On","ForceOff","ForceRestart"]}]}`,
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, authed := r.BasicAuth()
		mu.Lock()
		log = append(log, r.Method+" "+r.URL.Path+map[bool]string{true: " authed", false: ""}[authed])
		mu.Unlock()
		if r.URL.Path != "/redfish/v1/" && (user != "operator" || pass != "synthetic-secret") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		doc, ok := docs[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(doc))
	}))
	defer server.Close()
	pin := redfish.PublicKeySHA256(server.Certificate())
	h := newHarness(t, device("example-device-01", server.URL), secret("example-device-01", ""))
	h.r.BMC = RedfishBMC{Timeout: 5 * time.Second}
	h.reconcile(t, "example-device-01")
	got := h.get(t, "example-device-01")
	if got.Status.Discovery.TLS.PublicKeySHA256 != pin || condition(t, got.Status, inventory.ConditionAuthenticated).Reason != ReasonPinNotApproved {
		t.Fatalf("first contact status = %+v", got.Status)
	}
	mu.Lock()
	for _, l := range log {
		if strings.HasSuffix(l, "authed") {
			t.Fatalf("credential sent before the pin was approved: %v", log)
		}
	}
	mu.Unlock()
	var s corev1.Secret
	secrets := h.r.Secrets.(client.Client)
	_ = secrets.Get(context.Background(), types.NamespacedName{Namespace: credNS, Name: "bmc-example-device-01"}, &s)
	s.Data[PinKey] = []byte(pin)
	if err := secrets.Update(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(2 * time.Minute)
	h.reconcile(t, "example-device-01")
	got = h.get(t, "example-device-01")
	if c := condition(t, got.Status, inventory.ConditionDiscovered); c.Status != metav1.ConditionTrue {
		t.Fatalf("Discovered = %+v", c)
	}
	if inv := got.Status.Discovery.Inventory; inv == nil || inv.Managers[0].FirmwareVersion != "1.2.3" || inv.Systems[0].PowerState != "Off" {
		t.Fatalf("inventory = %+v", inv)
	}
	if c := condition(t, got.Status, inventory.ConditionIdentityMatched); c.Status != metav1.ConditionTrue {
		t.Fatalf("IdentityMatched = %+v", c)
	}
	// Offered reset types come from the ActionInfo, read with a GET; the
	// action itself is never invoked.
	if types := got.Status.Discovery.Inventory.Systems[0].ResetTypes; !slices.Equal(types, []string{"On", "ForceOff", "ForceRestart"}) {
		t.Fatalf("resetTypes = %v", types)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range log {
		if !strings.HasPrefix(l, "GET ") {
			t.Fatalf("discovery issued %s", l)
		}
	}
}
