package redfishwork

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

const (
	syncToken = "synthetic-dhcp-synchronizer-token-0123456789"
	address   = "192.0.2.20"
)

func newRequest(method, url, body string) (*http.Request, error) {
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err == nil {
		request.Header.Set("Authorization", "Bearer "+syncToken)
	}
	return request, err
}

func bootNetwork() *ops.BootNetworkInput {
	return &ops.BootNetworkInput{AddressClaim: ops.LocalReference{Name: "alloc-example-install-boot"}, Address: address}
}

// synchronizer stands in for one Kea instance and its synchronizer: each
// tick it fetches the site's feed as that instance and reports it.
type synchronizer struct {
	server   BootServer
	instance string
	mu       sync.Mutex
	paused   bool
}

func (s *synchronizer) run(ctx context.Context, t *testing.T) {
	handler, err := s.server.DHCPHandler(syncToken)
	if err != nil {
		t.Error(err)
		return
	}
	for ctx.Err() == nil {
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if !paused {
			fetch, _ := newRequest("GET", "/dhcp/v1/reservations", "")
			fetch.Header.Set(bootnet.InstanceHeader, s.instance)
			fetched := httptest.NewRecorder()
			handler.ServeHTTP(fetched, fetch)
			var feed bootnet.Feed
			if fetched.Code == http.StatusOK && json.Unmarshal(fetched.Body.Bytes(), &feed) == nil {
				data, _ := json.Marshal(bootnet.Applied{Instance: s.instance, Feed: feed})
				request, _ := newRequest("POST", "/dhcp/v1/applied", string(data))
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusNoContent && ctx.Err() == nil {
					t.Errorf("applied report = %d", recorder.Code)
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestASyncedInstallServesItsAddressOnlyWhileItRuns runs an installation at
// a synchronized site. The reservation carries the snapshot's boot MAC and
// the admitted address, DHCP serves it before the network boot, and the
// install succeeds only after DHCP withdrew it.
func TestASyncedInstallServesItsAddressOnlyWhileItRuns(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	synced(r)
	server := BootServer{Store: r.Store, BootRoot: l.bootRoot, DHCPInstances: []string{"dhcp-a"}}
	seen := make(chan bootnet.Feed, 1)
	l.onHook = func() {
		feed, _ := server.Reservations()
		select {
		case seen <- feed:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		(&synchronizer{server: server, instance: "dhcp-a"}).run(ctx, t)
	}()
	// The synchronizer stops before the test's directories are removed.
	defer func() { cancel(); <-stopped }()
	spec := installSpec()
	spec.BootNetwork = bootNetwork()
	result, err := r.Run(context.Background(), assignment(l, "DeviceInstallation", "op-synced", 1, spec))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseSucceeded || l.onCount() != 2 {
		t.Fatalf("result = %+v, %d power-ons", result, l.onCount())
	}
	if feed := <-seen; !feed.Has(mac, address) || len(feed.Reservations) != 1 {
		t.Fatalf("feed at power-on = %+v, want the snapshot MAC at the admitted address", feed)
	}
	if feed, _ := server.Reservations(); len(feed.Reservations) != 0 {
		t.Fatalf("reservation outlived its operation: %+v", feed)
	}
}

func synced(r *site.Runner) {
	for name, kind := range r.Kinds {
		switch k := kind.(type) {
		case Installation:
			k.Synced, k.DHCPInstances, k.ContactQuiet = true, []string{"dhcp-a"}, time.Millisecond
			r.Kinds[name] = k
		case Wipe:
			k.Synced, k.DHCPInstances, k.ContactQuiet = true, []string{"dhcp-a"}, time.Millisecond
			r.Kinds[name] = k
		}
	}
}

// TestABootAddressMustFitTheSiteDHCP refuses, with nothing sent, a network
// boot without a boot address at a synchronized site and one with a boot
// address elsewhere.
func TestABootAddressMustFitTheSiteDHCP(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	spec := installSpec()
	spec.BootNetwork = bootNetwork()
	result, _ := r.Run(context.Background(), assignment(l, "DeviceInstallation", "op-static", 1, spec))
	if result.Phase != ops.PhaseFailed || result.Reason != custody.ReasonBootNetworkUnsupported || l.onCount() != 0 {
		t.Fatalf("static site: %+v", result)
	}
	m := newLab(t)
	s := runner(t, m)
	synced(s)
	wipe := ops.DeviceWipeSpec{Step: 1, CleaningPolicyDigest: "sha256:" + strings.Repeat("b", 64), TargetDisks: []string{"disk-0"}, BootInterface: "primary"}
	result, _ = s.Run(context.Background(), assignment(m, "DeviceWipe", "op-no-address", 1, wipe))
	if result.Phase != ops.PhaseFailed || result.Reason != custody.ReasonBootNetworkRequired || m.onCount() != 0 {
		t.Fatalf("synced site without an address: %+v", result)
	}
	if feed, _ := (BootServer{Store: s.Store}).Reservations(); len(feed.Reservations) != 0 {
		t.Fatalf("a refused operation published %+v", feed)
	}
	// Kea DHCPv4 cannot serve an IPv6 boot address, so the site refuses it
	// before it can stall the other reservations.
	n := newLab(t)
	u := runner(t, n)
	synced(u)
	v6 := installSpec()
	v6.BootNetwork = bootNetwork()
	v6.BootNetwork.Address = "2001:db8::20"
	result, _ = u.Run(context.Background(), assignment(n, "DeviceInstallation", "op-v6", 1, v6))
	if result.Phase != ops.PhaseFailed || result.Reason != ReasonInputs || n.onCount() != 0 {
		t.Fatalf("IPv6 boot address: %+v", result)
	}
}

// TestAPublishedReservationNeverEndsFailed lets DHCP never apply the
// reservation. The worker stops before any power-on, but the reservation
// was published, so the outcome is unknown and the address stays claimed.
func TestAPublishedReservationNeverEndsFailed(t *testing.T) {
	l := newLab(t)
	r := runner(t, l)
	synced(r)
	installation := r.Kinds["DeviceInstallation"].(Installation)
	installation.BootWindow = 200 * time.Millisecond
	r.Kinds["DeviceInstallation"] = installation
	spec := installSpec()
	spec.BootNetwork = bootNetwork()
	result, err := r.Run(context.Background(), assignment(l, "DeviceInstallation", "op-unapplied", 1, spec))
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != ops.PhaseOutcomeUnknown || l.onCount() != 0 {
		t.Fatalf("result = %+v, %d power-ons", result, l.onCount())
	}
}

// gate is a site store, a feed server expecting dhcp-a and dhcp-b, and a
// worker configuration reading the same reports, on a fixed site clock.
type gate struct {
	t       *testing.T
	store   *site.Store
	server  BootServer
	handler http.Handler
	config  Config
	now     time.Time
}

func newGate(t *testing.T) *gate {
	root := t.TempDir()
	for _, dir := range []string{"store", "boot"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := site.OpenStore(filepath.Join(root, "store"), nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{t: t, store: store, now: time.Now().UTC().Truncate(time.Second)}
	clock := func() time.Time { return g.now }
	instances := []string{"dhcp-a", "dhcp-b"}
	g.server = BootServer{Store: store, BootRoot: filepath.Join(root, "boot"), DHCPInstances: instances, Now: clock}
	if g.handler, err = g.server.DHCPHandler(syncToken); err != nil {
		t.Fatal(err)
	}
	g.config = Config{BootRoot: g.server.BootRoot, Poll: 5 * time.Millisecond, Now: clock, Synced: true, DHCPInstances: instances, ContactQuiet: time.Second}
	return g
}

// serve sends one request to the feed handler.
func (g *gate) serve(method, path, instance, body string) *httptest.ResponseRecorder {
	request, _ := newRequest(method, path, body)
	if instance != "" {
		request.Header.Set(bootnet.InstanceHeader, instance)
	}
	recorder := httptest.NewRecorder()
	g.handler.ServeHTTP(recorder, request)
	return recorder
}

// fetch is the feed one instance gets now.
func (g *gate) fetch(instance string) bootnet.Feed {
	g.t.Helper()
	recorder := g.serve("GET", "/dhcp/v1/reservations", instance, "")
	var feed bootnet.Feed
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &feed) != nil || feed.Verify() != nil {
		g.t.Fatalf("feed for %s = %d %s", instance, recorder.Code, recorder.Body)
	}
	return feed
}

// post reports that instance applied feed, and returns the status.
func (g *gate) post(instance string, feed bootnet.Feed) int {
	data, _ := json.Marshal(bootnet.Applied{Instance: instance, Feed: feed})
	return g.serve("POST", "/dhcp/v1/applied", "", string(data)).Code
}

// apply fetches the current feed as instance and reports it.
func (g *gate) apply(instance string) bootnet.Feed {
	g.t.Helper()
	feed := g.fetch(instance)
	if code := g.post(instance, feed); code != http.StatusNoContent {
		g.t.Fatalf("applied report from %s = %d", instance, code)
	}
	return feed
}

// tick moves the site clock on by a second, the feed's issue precision.
func (g *gate) tick() { g.now = g.now.Add(time.Second) }

// admit stores an open admission at fence, published one second after it
// was created.
func (g *gate) admit(operation string, fence int64, address string) Boot {
	g.t.Helper()
	if err := g.store.Raise(device, fence); err != nil {
		g.t.Fatal(err)
	}
	boot := Boot{Operation: operation, Fence: fence, MAC: mac, Address: address, Token: strings.Repeat("a", 64), Created: g.now, Published: g.now.Add(time.Second), Expires: g.now.Add(time.Hour)}
	if err := g.store.PutRecord(device, fence, bootRecords, operation, boot); err != nil {
		g.t.Fatal(err)
	}
	g.tick()
	g.tick()
	return boot
}

func (g *gate) close(boot Boot) {
	g.t.Helper()
	boot.Closed = true
	if err := g.store.PutRecord(device, boot.Fence, bootRecords, boot.Operation, boot); err != nil {
		g.t.Fatal(err)
	}
	g.tick()
}

// withdrawn runs the withdrawal gate briefly.
func (g *gate) withdrawn(boot Boot) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	return g.config.awaitWithdrawal(ctx, g.store, device, boot)
}

func (g *gate) reserved(boot Boot) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	return g.config.awaitReservation(ctx, boot)
}

// applied is the retained applied reports, without refused contacts.
func applied(retained []report) []report {
	var out []report
	for _, r := range retained {
		if !r.Rejected {
			out = append(out, r)
		}
	}
	return out
}

// contacts is the retained refused contacts, by instance name.
func (g *gate) contacts() []string {
	g.t.Helper()
	retained, err := reports(g.server.BootRoot)
	if err != nil {
		g.t.Fatal(err)
	}
	var out []string
	for _, r := range retained {
		if r.Rejected {
			out = append(out, r.Instance)
		}
	}
	return out
}

// removed is a handler of the same feed that still expects dhcp-c, as the
// feed did before dhcp-c was removed from the enrollment.
func (g *gate) removed() http.Handler {
	g.t.Helper()
	before := g.server
	before.DHCPInstances = []string{"dhcp-a", "dhcp-b", "dhcp-c"}
	handler, err := before.DHCPHandler(syncToken)
	if err != nil {
		g.t.Fatal(err)
	}
	return handler
}

// TestARemovedInstanceStillPollingBlocksWithdrawal: dhcp-c was removed from
// the expected set while its replica still runs. Its last report served
// nothing, but it keeps polling the feed, which refuses and retains each
// contact, so withdrawal stays unproven. Once it stops, a later boot's
// withdrawal is proven.
func TestARemovedInstanceStillPollingBlocksWithdrawal(t *testing.T) {
	g := newGate(t)
	current := g.handler
	g.handler = g.removed()
	g.apply("dhcp-c")
	g.handler = current
	boot := g.admit("op-a", 1, address)
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	if code := g.serve("GET", "/dhcp/v1/reservations", "dhcp-c", "").Code; code != http.StatusForbidden {
		t.Fatalf("removed instance's feed = %d", code)
	}
	g.close(boot)
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("withdrawal proven while a removed instance still polls the feed")
	}
	// dhcp-c stopped. A later boot, published after its last contact,
	// passes.
	later := g.admit("op-b", 2, "192.0.2.21")
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	g.close(later)
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	if err := g.withdrawn(later); err != nil {
		t.Fatalf("withdrawal after the removed instance stopped: %v", err)
	}
}

// TestTheRateLimitCannotHideAPollingInstance: contacts are retained at most
// once per contactInterval, so the gate cannot pass before contactQuiet
// after the admission was published; by then a still-polling instance has
// been retained again.
func TestTheRateLimitCannotHideAPollingInstance(t *testing.T) {
	g := newGate(t)
	g.config.ContactQuiet = 0
	g.serve("GET", "/dhcp/v1/reservations", "dhcp-c", "")
	boot := g.admit("op-a", 1, address)
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	g.close(boot)
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	// dhcp-c polls again within the interval: refused, not retained.
	g.serve("GET", "/dhcp/v1/reservations", "dhcp-c", "")
	if got := g.contacts(); len(got) != 1 {
		t.Fatalf("contacts = %q", got)
	}
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("withdrawal proven before a rate-limited contact could be retained")
	}
	g.now = g.now.Add(contactInterval)
	g.serve("GET", "/dhcp/v1/reservations", "dhcp-c", "")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("withdrawal proven while a removed instance still polls the feed")
	}
}

// TestRefusedContactsAreRetainedOncePerInterval retains one record per
// instance name per contactInterval, for a missing name too, for a refused
// feed request and a refused report alike, and never for an unauthenticated
// request. A contact that cannot be retained is still refused.
func TestRefusedContactsAreRetainedOncePerInterval(t *testing.T) {
	g := newGate(t)
	request, _ := newRequest("GET", "/dhcp/v1/reservations", "")
	request.Header.Set("Authorization", "Bearer wrong-token-wrong-token-wrong-token-0000")
	request.Header.Set(bootnet.InstanceHeader, "dhcp-c")
	recorder := httptest.NewRecorder()
	g.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || len(g.contacts()) != 0 {
		t.Fatalf("unauthenticated contact = %d, retained %q", recorder.Code, g.contacts())
	}
	empty, _ := bootnet.NewFeed(nil)
	for i := 0; i < 3; i++ {
		g.serve("GET", "/dhcp/v1/reservations", "dhcp-c", "")
		g.serve("GET", "/dhcp/v1/reservations", "", "")
		if code := g.post("dhcp-c", empty); code != http.StatusForbidden {
			t.Fatalf("report from a removed instance = %d", code)
		}
		g.tick()
	}
	if got := g.contacts(); len(got) != 2 || got[0] != "dhcp-c" || got[1] != "" {
		t.Fatalf("contacts within one interval = %q", got)
	}
	g.now = g.now.Add(contactInterval)
	g.post("dhcp-c", empty)
	if got := g.contacts(); len(got) != 3 || got[2] != "dhcp-c" {
		t.Fatalf("contacts after the interval = %q", got)
	}
	// The log cannot be written: still refused.
	broken := g.server
	broken.BootRoot = filepath.Join(g.server.BootRoot, dhcpLog)
	handler, err := broken.DHCPHandler(syncToken)
	if err != nil {
		t.Fatal(err)
	}
	g.handler = handler
	if code := g.serve("GET", "/dhcp/v1/reservations", "dhcp-d", "").Code; code != http.StatusForbidden {
		t.Fatalf("an unretained contact = %d", code)
	}
}

// TestTheFeedServesOnlyExpectedInstances refuses a feed request without an
// instance or from an instance outside the expected set, and refuses and
// does not retain such an instance's report. An expected instance gets the
// feed stamped with the site clock.
func TestTheFeedServesOnlyExpectedInstances(t *testing.T) {
	g := newGate(t)
	if code := g.serve("GET", "/dhcp/v1/reservations", "", "").Code; code != http.StatusForbidden {
		t.Fatalf("feed without an instance = %d", code)
	}
	if code := g.serve("GET", "/dhcp/v1/reservations", "dhcp-c", "").Code; code != http.StatusForbidden {
		t.Fatalf("feed for an unexpected instance = %d", code)
	}
	request, _ := newRequest("GET", "/dhcp/v1/reservations", "")
	request.Header.Del("Authorization")
	request.Header.Set(bootnet.InstanceHeader, "dhcp-a")
	recorder := httptest.NewRecorder()
	g.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated feed = %d", recorder.Code)
	}
	feed := g.fetch("dhcp-a")
	if !feed.Issued.Equal(g.now) || feed.Issued.Location() != time.UTC {
		t.Fatalf("feed issued %v, want the site clock %v", feed.Issued, g.now)
	}
	if code := g.post("dhcp-c", feed); code != http.StatusForbidden {
		t.Fatalf("report from an unexpected instance = %d", code)
	}
	if retained, err := reports(g.server.BootRoot); err != nil || len(applied(retained)) != 0 {
		t.Fatalf("an unexpected instance's report was retained: %+v, %v", retained, err)
	}
	if code := g.post("dhcp-a", feed); code != http.StatusNoContent {
		t.Fatalf("report from an expected instance = %d", code)
	}
	if retained, err := reports(g.server.BootRoot); err != nil || len(applied(retained)) != 1 || !applied(retained)[0].Feed.Issued.Equal(feed.Issued) {
		t.Fatalf("retained %+v, %v", retained, err)
	}
	if _, err := (BootServer{}).DHCPHandler(syncToken); err == nil {
		t.Fatal("a feed without an expected instance set started")
	}
}

// TestWithdrawalNeedsEveryExpectedInstance holds success while an expected
// instance never reported, even though the other reported the reservation
// served and withdrawn. Past the window the outcome stays unknown.
func TestWithdrawalNeedsEveryExpectedInstance(t *testing.T) {
	g := newGate(t)
	g.apply("dhcp-a")
	boot := g.admit("op-a", 1, address)
	if feed := g.apply("dhcp-a"); !feed.Has(mac, address) {
		t.Fatalf("feed = %+v", feed)
	}
	if err := g.reserved(boot); err != nil {
		t.Fatalf("served reservation not seen: %v", err)
	}
	g.close(boot)
	g.apply("dhcp-a")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("withdrawal proven without dhcp-b ever reporting")
	}
	// Past the window the gate gives up, leaving the outcome unknown.
	start := g.now
	steps := 0
	g.config.Now = func() time.Time { steps++; return start.Add(time.Duration(steps) * withdrawalWindow / 2) }
	if err := g.config.awaitWithdrawal(context.Background(), g.store, device, boot); err == nil || !strings.Contains(err.Error(), "not reported the boot reservation withdrawn") {
		t.Fatalf("gate past its window = %v", err)
	}
}

// TestAnInstanceThatStoppedAfterFetchingTheReservationBlocks: dhcp-b's latest
// report is of a feed issued before the admission, which serves nothing, but
// it fetched a feed carrying the reservation and stopped before reporting
// it. It may serve the reservation, so withdrawal stays unproven.
func TestAnInstanceThatStoppedAfterFetchingTheReservationBlocks(t *testing.T) {
	g := newGate(t)
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	boot := g.admit("op-a", 1, address)
	g.apply("dhcp-a")
	if feed := g.fetch("dhcp-b"); !feed.Has(mac, address) {
		t.Fatalf("feed = %+v", feed)
	}
	g.close(boot)
	g.apply("dhcp-a")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("withdrawal proven from dhcp-b's report older than the admission")
	}
}

// TestWithdrawalIsProvenByEveryExpectedInstance: each expected instance
// reported a feed issued after the admission was published without the
// reservation, so it is withdrawn everywhere. A report from an instance
// outside the set, retained earlier, neither blocks nor serves.
func TestWithdrawalIsProvenByEveryExpectedInstance(t *testing.T) {
	g := newGate(t)
	// A removed instance's earlier report, retained while it was expected,
	// served the address.
	removed := g.server
	removed.DHCPInstances = []string{"dhcp-a", "dhcp-b", "dhcp-c"}
	handler, err := removed.DHCPHandler(syncToken)
	if err != nil {
		t.Fatal(err)
	}
	g.handler, handler = handler, g.handler
	boot := g.admit("op-a", 1, address)
	g.apply("dhcp-c")
	g.handler = handler
	if err := g.reserved(boot); err == nil {
		t.Fatal("a report from an instance outside the expected set counted as served")
	}
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("withdrawal proven while the admission is open")
	}
	g.close(boot)
	g.apply("dhcp-a")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("withdrawal proven before dhcp-b reported it")
	}
	g.apply("dhcp-b")
	if err := g.withdrawn(boot); err != nil {
		t.Fatalf("withdrawal reported by every expected instance: %v", err)
	}
	// An admission that was never published proves nothing.
	unpublished := boot
	unpublished.Published = time.Time{}
	if err := g.withdrawn(unpublished); err == nil {
		t.Fatal("withdrawal proven for an admission without a publication time")
	}
}

// TestOnlyAnExpectedInstanceProvesTheReservationServed: an instance removed
// from the expected set served the reservation, and both expected instances
// reported it withdrawn without ever serving it. Nothing expected served it,
// so withdrawal stays unproven.
func TestOnlyAnExpectedInstanceProvesTheReservationServed(t *testing.T) {
	g := newGate(t)
	removed := g.server
	removed.DHCPInstances = []string{"dhcp-a", "dhcp-b", "dhcp-c"}
	handler, err := removed.DHCPHandler(syncToken)
	if err != nil {
		t.Fatal(err)
	}
	boot := g.admit("op-a", 1, address)
	g.handler, handler = handler, g.handler
	g.apply("dhcp-c")
	g.handler = handler
	g.close(boot)
	g.apply("dhcp-a")
	g.apply("dhcp-b")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("a report from an instance outside the expected set proved the reservation served")
	}
}

// TestWithdrawalCoversEveryReservationOfTheDevice holds a later operation's
// success while an expected instance's latest report serves a reservation
// the site published for the Device, such as a held operation's.
func TestWithdrawalCoversEveryReservationOfTheDevice(t *testing.T) {
	g := newGate(t)
	held := g.admit("op-held", 2, "192.0.2.30")
	g.apply("dhcp-a")
	if feed := g.apply("dhcp-b"); !feed.Has(mac, held.Address) {
		t.Fatalf("feed = %+v", feed)
	}
	boot := g.admit("op-later", 3, address)
	feed := g.fetch("dhcp-a")
	if len(feed.Reservations) != 1 || !feed.Has(mac, address) {
		t.Fatalf("feed = %+v: only the current admission is published", feed)
	}
	if err := g.reserved(boot); err == nil {
		t.Fatal("a worker booted before DHCP applied the reservation")
	}
	g.apply("dhcp-a")
	if err := g.reserved(boot); err != nil {
		t.Fatalf("applied reservation not seen: %v", err)
	}
	g.close(boot)
	g.apply("dhcp-a")
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("dhcp-b, last serving the held reservation, did not block success")
	}
	g.apply("dhcp-b")
	if err := g.withdrawn(boot); err != nil {
		t.Fatalf("withdrawal after every instance reported: %v", err)
	}
	// A newer fence supersedes the admission: its reservation goes.
	if err := g.store.Raise(device, 4); err != nil {
		t.Fatal(err)
	}
	if feed, err := g.server.Reservations(); err != nil || len(feed.Reservations) != 0 {
		t.Fatalf("feed after a newer fence = %+v, %v", feed, err)
	}
}

// TestOldRetainedLinesAreNoEvidence reads reports retained before feeds
// carried an issue time, and records of the removed retirement command. An
// old report reads as issued at the zero time, so it cannot prove a
// withdrawal; a retirement record is skipped and changes nothing.
func TestOldRetainedLinesAreNoEvidence(t *testing.T) {
	g := newGate(t)
	boot := g.admit("op-a", 1, address)
	g.apply("dhcp-a")
	g.close(boot)
	g.apply("dhcp-a")
	empty, _ := bootnet.NewFeed(nil)
	at := g.now.Format(time.RFC3339)
	old := `{"at":"` + at + `","instance":"dhcp-b","feed":{"revision":"` + empty.Revision + `","reservations":[]}}` + "\n"
	retired := `{"at":"` + at + `","instance":"dhcp-b","feed":{"revision":"` + empty.Revision + `","reservations":[]},"retired":true}` + "\n"
	if err := appendEvidence(g.server.BootRoot, dhcpLog, old+retired); err != nil {
		t.Fatal(err)
	}
	retained, err := reports(g.server.BootRoot)
	if err != nil || len(retained) != 3 || !retained[2].Feed.Issued.IsZero() || retained[2].Instance != "dhcp-b" {
		t.Fatalf("retained = %+v, %v", retained, err)
	}
	if err := g.withdrawn(boot); err == nil {
		t.Fatal("a report without an issue time, or a retirement record, proved withdrawal")
	}
	g.apply("dhcp-b")
	if err := appendEvidence(g.server.BootRoot, dhcpLog, retired); err != nil {
		t.Fatal(err)
	}
	if err := g.withdrawn(boot); err != nil {
		t.Fatalf("a retirement record after dhcp-b's withdrawal report changed the outcome: %v", err)
	}
}

// TestTheFeedServesOnlyOverTLS refuses to start the feed without a
// certificate and key: the synchronizer fetches it only over HTTPS.
func TestTheFeedServesOnlyOverTLS(t *testing.T) {
	if err := (BootServer{}).ServeDHCP(syncToken, "", ""); err == nil {
		t.Fatal("the feed started without TLS")
	}
}
