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
// tick it applies the site's current feed and reports it.
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
			feed, err := s.server.Reservations()
			if err == nil {
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
	server := BootServer{Store: r.Store, BootRoot: l.bootRoot}
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
			k.Synced = true
			r.Kinds[name] = k
		case Wipe:
			k.Synced = true
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

// TestWithdrawalCoversEveryReservationOfTheDevice holds a later operation's
// success while any instance's latest report, however old, serves a
// reservation the site published for the Device, until that instance
// reports again or an operator retires it.
func TestWithdrawalCoversEveryReservationOfTheDevice(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	server := BootServer{Store: store, BootRoot: filepath.Join(root, "boot"), Now: clock}
	handler, err := server.DHCPHandler(syncToken)
	if err != nil {
		t.Fatal(err)
	}
	post := func(instance string, rs ...bootnet.Reservation) {
		t.Helper()
		feed, err := bootnet.NewFeed(rs)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(bootnet.Applied{Instance: instance, Feed: feed})
		request, _ := newRequest("POST", "/dhcp/v1/applied", string(data))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("applied report = %d", recorder.Code)
		}
	}
	request, _ := newRequest("GET", "/dhcp/v1/reservations", "")
	request.Header.Del("Authorization")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated feed = %d", recorder.Code)
	}

	// A held operation at fence 2 published 192.0.2.30, and instance
	// dhcp-b reported serving it before it went silent.
	held := Boot{Operation: "op-held", Fence: 2, MAC: mac, Address: "192.0.2.30", Token: strings.Repeat("b", 64), Created: now.Add(-time.Hour), Expires: now.Add(-30 * time.Minute)}
	if err := store.Raise(device, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRecord(device, 2, bootRecords, held.Operation, held); err != nil {
		t.Fatal(err)
	}
	post("dhcp-b", bootnet.Reservation{MAC: mac, Address: held.Address, Expires: now.Add(time.Hour)})
	// The later operation at fence 3.
	if err := store.Raise(device, 3); err != nil {
		t.Fatal(err)
	}
	boot := Boot{Operation: "op-later", Fence: 3, MAC: mac, Address: address, Token: strings.Repeat("a", 64), Created: now.Add(-time.Minute), Expires: now.Add(time.Hour)}
	if err := store.PutRecord(device, 3, bootRecords, boot.Operation, boot); err != nil {
		t.Fatal(err)
	}
	feed, err := server.Reservations()
	if err != nil || len(feed.Reservations) != 1 || !feed.Has(mac, address) {
		t.Fatalf("feed = %+v, %v: only the current admission is published", feed, err)
	}
	config := Config{BootRoot: server.BootRoot, Poll: 5 * time.Millisecond, Now: clock, Synced: true}
	short := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	if err := config.awaitReservation(short(), boot); err == nil {
		t.Fatal("a worker booted before DHCP applied the reservation")
	}
	post("dhcp-a", feed.Reservations...)
	if err := config.awaitReservation(short(), boot); err != nil {
		t.Fatalf("applied reservation not seen: %v", err)
	}
	post("dhcp-a")
	if err := config.awaitWithdrawal(short(), store, device, boot); err == nil {
		t.Fatal("a silent instance serving the held reservation did not block success")
	}
	if err := RetireInstance(server.BootRoot, "dhcp-b"); err != nil {
		t.Fatal(err)
	}
	if err := config.awaitWithdrawal(short(), store, device, boot); err != nil {
		t.Fatalf("withdrawal after retiring the silent instance: %v", err)
	}
	// A retired instance that reports again counts again.
	post("dhcp-b", bootnet.Reservation{MAC: mac, Address: held.Address, Expires: now.Add(time.Hour)})
	if err := config.awaitWithdrawal(short(), store, device, boot); err == nil {
		t.Fatal("a report after retirement was ignored")
	}
	// A newer fence supersedes the admission: its reservation goes.
	if err := store.Raise(device, 4); err != nil {
		t.Fatal(err)
	}
	if feed, err := server.Reservations(); err != nil || len(feed.Reservations) != 0 {
		t.Fatalf("feed after a newer fence = %+v, %v", feed, err)
	}
}
