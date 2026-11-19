package redfish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/executor"
)

const syncToken = "synthetic-dhcp-synchronizer-token-0123456789"

// fakeSync stands in for the DHCP owner's synchronizer: each instance applies
// the current feed and reports it whenever its revision changes. Withholding
// an instance's reports models a DHCP instance that may still serve.
type fakeSync struct {
	t       *testing.T
	h       http.Handler
	mu      sync.Mutex
	silent  map[string]bool
	applied map[string]string
	stop    chan struct{}
	done    sync.WaitGroup
}

func startSync(t *testing.T, p *Provider, instances ...string) *fakeSync {
	t.Helper()
	h, err := p.DHCPHandler(syncToken)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSync{t: t, h: h, silent: map[string]bool{}, applied: map[string]string{}, stop: make(chan struct{})}
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		for {
			select {
			case <-s.stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
			for _, instance := range instances {
				s.cycle(instance)
			}
		}
	}()
	t.Cleanup(s.close)
	return s
}

func (s *fakeSync) close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.done.Wait()
}

func (s *fakeSync) silence(instance string, v bool) {
	s.mu.Lock()
	s.silent[instance] = v
	s.mu.Unlock()
}

func (s *fakeSync) cycle(instance string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.silent[instance] {
		return
	}
	w := httptest.NewRecorder()
	q := httptest.NewRequest(http.MethodGet, "/dhcp/v1/reservations", nil)
	q.Header.Set("Authorization", "Bearer "+syncToken)
	s.h.ServeHTTP(w, q)
	var feed bootnet.Feed
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &feed) != nil || s.applied[instance] == feed.Revision {
		return
	}
	if code := post(s.h, syncToken, bootnet.Applied{Instance: instance, Feed: feed}); code != http.StatusNoContent {
		return
	}
	s.applied[instance] = feed.Revision
}

func post(h http.Handler, token string, a any) int {
	b, _ := json.Marshal(a)
	w := httptest.NewRecorder()
	q := httptest.NewRequest(http.MethodPost, "/dhcp/v1/applied", bytes.NewReader(b))
	q.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(w, q)
	return w.Code
}

func TestEnrollmentRecordsPhysicalAttachments(t *testing.T) {
	target := func(asset, endpoint, system, uuid, mac, prov string) string {
		return fmt.Sprintf(`{"assetID":%q,"endpoint":%q,"systemID":%q,"systemUUID":%q,"mac":%q%s,"diskBytes":1,"console":"c.log"}`, asset, endpoint, system, uuid, mac, prov)
	}
	prov := func(address string) string {
		return `,"provisioning":{"address":"` + address + `","gateway":"198.51.100.1"}`
	}
	// Physical BMCs each expose one System with the same ID: identity is the
	// endpoint plus System, so two "Self" systems on two BMCs are distinct.
	e, err := ParseEnrollment([]byte(`{"dhcp":"synced","targets":[` +
		target("a", "https://192.0.2.20:443", "Self", uuidA, "00-00-5E-00-53-20", prov("198.51.100.21/24")) + `,` +
		target("b", "https://192.0.2.21:443", "Self", uuidB, "00:00:5e:00:53:21", prov("198.51.100.22/24")) + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Targets[0]; got.Endpoint != "https://192.0.2.20:443" || got.MAC != "00:00:5e:00:53:20" || got.Provisioning != (Provisioning{Address: "198.51.100.21/24", Gateway: "198.51.100.1"}) {
		t.Fatalf("enrollment not canonical: %+v", got)
	}
	for name, body := range map[string]string{
		"same System on one BMC": `{"dhcp":"synced","targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", prov("198.51.100.21/24")) + `,` +
			target("b", "https://192.0.2.20:443", "Self", uuidB, "00:00:5e:00:53:21", prov("198.51.100.22/24")) + `]}`,
		"shared provisioning address": `{"dhcp":"synced","targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", prov("198.51.100.21/24")) + `,` +
			target("b", "https://192.0.2.21:443", "Self", uuidB, "00:00:5e:00:53:21", prov("198.51.100.21/24")) + `]}`,
		"synced without attachment":      `{"dhcp":"synced","targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", "") + `]}`,
		"static with attachment":         `{"dhcp":"static","targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", prov("198.51.100.21/24")) + `]}`,
		"gateway outside prefix":         `{"dhcp":"synced","targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", `,"provisioning":{"address":"198.51.100.21/24","gateway":"203.0.113.1"}`) + `]}`,
		"network address as host":        `{"dhcp":"synced","targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", prov("198.51.100.0/24")) + `]}`,
		"BMC endpoint with a path":       `{"dhcp":"static","targets":[` + target("a", "https://192.0.2.20:443/redfish", "Self", uuidA, "00:00:5e:00:53:20", "") + `]}`,
		"missing DHCP mode":              `{"targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", "") + `]}`,
		"retired site-wide BMC endpoint": `{"dhcp":"static","endpoint":"https://192.0.2.20:443","targets":[` + target("a", "https://192.0.2.20:443", "Self", uuidA, "00:00:5e:00:53:20", "") + `]}`,
	} {
		if _, err := ParseEnrollment([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSyncedInstallServesAndWithdrawsTheReservation(t *testing.T) {
	f := newDHCPFixture(t, time.Second, DHCPSynced)
	ctx := context.Background()
	if feed, err := f.p.Reservations(); err != nil || len(feed.Reservations) != 0 {
		t.Fatalf("reservations published with no admitted boot: %+v %v", feed, err)
	}
	s := startSync(t, f.p, "kea-0")
	q := f.install(1)
	if r, err := f.p.Execute(ctx, q); err != nil || r.Result != "installed" {
		t.Fatalf("install: %v %+v", err, r)
	}
	f.bmc.wg.Wait()
	s.close()
	reports, err := f.p.reports()
	if err != nil {
		t.Fatal(err)
	}
	var served, withdrawn bool
	for _, a := range reports {
		if a.Feed.Has("52:54:00:77:01:0a", "192.0.2.10/24") {
			served = true
		} else if served {
			withdrawn = true
		}
		if a.Feed.Serves("52:54:00:77:01:0b") {
			t.Fatal("the other enrolled target's attachment was published")
		}
	}
	if !served || !withdrawn {
		t.Fatalf("reports do not show the reservation served then withdrawn: %+v", reports)
	}
	if feed, _ := f.p.Reservations(); len(feed.Reservations) != 0 {
		t.Fatalf("reservation outlived the operation: %+v", feed)
	}
	if b, _ := os.ReadFile(filepath.Join(f.p.bootRoot, dhcpLog)); strings.Contains(string(b), syncToken) {
		t.Fatal("DHCP evidence contains the synchronizer token")
	}
}

func TestSyncedBootWaitsForDHCPWithoutEffects(t *testing.T) {
	f := newDHCPFixture(t, time.Second, DHCPSynced)
	q := f.install(1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := f.p.Execute(ctx, q); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("network boot proceeded without DHCP evidence: %v", err)
	}
	if got := strings.Join(f.bmc.mutationsFor("System-1"), ","); strings.Contains(got, "Pxe") || strings.Contains(got, "On") {
		t.Fatalf("boot effects before DHCP applied the reservation: %s", got)
	}
	// A report from before this operation is not evidence for it.
	feed, _ := f.p.Reservations()
	if len(feed.Reservations) != 1 || !feed.Has("52:54:00:77:01:0a", "192.0.2.10/24") {
		t.Fatalf("admitted operation not published: %+v", feed)
	}
	startSync(t, f.p, "kea-0")
	if r, err := f.p.Execute(context.Background(), q); err != nil || r.Result != "installed" {
		t.Fatalf("replay after DHCP applied: %v %+v", err, r)
	}
	f.bmc.wg.Wait()
	if got := f.bmc.mutationsFor("System-1"); strings.Join(got, ",") != "POST ForceOff,PATCH Pxe,POST On,PATCH Hdd,POST On" {
		t.Fatalf("unexpected Redfish effects: %v", got)
	}
}

func TestUnwithdrawnReservationKeepsTheOutcomeUnknown(t *testing.T) {
	f := newDHCPFixture(t, time.Second, DHCPSynced)
	s := startSync(t, f.p, "kea-0", "kea-1")
	q := f.install(1)
	result := make(chan error, 1)
	go func() {
		r, err := f.p.Execute(context.Background(), q)
		if err == nil && r.Result != "installed" {
			err = fmt.Errorf("unexpected result %+v", r)
		}
		result <- err
	}()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(2 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for " + what)
			}
		}
	}
	// Both instances serve the reservation; then kea-1 goes silent, so it
	// never reports the withdrawal and may still be serving.
	waitFor("both instances to serve", func() bool {
		reports, _ := f.p.reports()
		served := map[string]bool{}
		for _, a := range reports {
			if a.Feed.Serves("52:54:00:77:01:0a") {
				served[a.Instance] = true
			}
		}
		return len(served) == 2
	})
	s.silence("kea-1", true)
	waitFor("the withdrawal gate", func() bool { return f.stage(t, q.ID) == "disk-verified" })
	time.Sleep(50 * time.Millisecond)
	observer := *f.p
	observer.readOnly = true
	if _, found, err := observer.Lookup(context.Background(), q.ID); found || !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("completed while one DHCP instance may still serve: %v %v", found, err)
	}
	s.silence("kea-1", false)
	if err := <-result; err != nil {
		t.Fatalf("withdrawal by every serving instance did not complete the operation: %v", err)
	}
	f.bmc.wg.Wait()
}

func TestDHCPHandlerAuthenticatesAndRejectsForgedReports(t *testing.T) {
	f := newDHCPFixture(t, time.Second, DHCPSynced)
	if _, err := f.p.DHCPHandler("short"); err == nil {
		t.Fatal("a weak token was accepted")
	}
	h, _ := f.p.DHCPHandler(syncToken)
	for _, auth := range []string{"", "Bearer wrong-token-wrong-token-wrong-token-0000", "Basic " + syncToken} {
		w := httptest.NewRecorder()
		q := httptest.NewRequest(http.MethodGet, "/dhcp/v1/reservations", nil)
		if auth != "" {
			q.Header.Set("Authorization", auth)
		}
		h.ServeHTTP(w, q)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%q: %d", auth, w.Code)
		}
	}
	if code := post(h, "wrong-token-wrong-token-wrong-token-0000", bootnet.Applied{Instance: "kea-0", Feed: bootnet.Feed{Reservations: []bootnet.Reservation{}}}); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report: %d", code)
	}
	valid, _ := bootnet.NewFeed([]bootnet.Reservation{{MAC: "52:54:00:77:01:0a", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Expires: time.Now().Add(time.Minute)}})
	forged := valid
	forged.Reservations = []bootnet.Reservation{}
	for name, a := range map[string]any{
		"revision of another set": bootnet.Applied{Instance: "kea-0", Feed: forged},
		"bad instance":            bootnet.Applied{Instance: "Kea 0", Feed: valid},
		"unknown field":           map[string]any{"instance": "kea-0", "feed": valid, "extra": true},
	} {
		if code := post(h, syncToken, a); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if reports, _ := f.p.reports(); len(reports) != 0 {
		t.Fatalf("rejected reports were retained: %+v", reports)
	}
	if code := post(h, syncToken, bootnet.Applied{Instance: "kea-0", Feed: valid}); code != http.StatusNoContent {
		t.Fatalf("valid report: %d", code)
	}
	w := httptest.NewRecorder()
	q := httptest.NewRequest(http.MethodGet, "/pxe/boot.ipxe", nil)
	q.Header.Set("Authorization", "Bearer "+syncToken)
	h.ServeHTTP(w, q)
	if w.Code != http.StatusNotFound {
		t.Fatalf("DHCP listener served a boot path: %d", w.Code)
	}
	os.WriteFile(filepath.Join(f.p.bootRoot, dhcpLog), []byte("not json\n"), 0600)
	if _, err := f.p.reports(); err == nil {
		t.Fatal("corrupt DHCP evidence was read as evidence")
	}
}

func TestShippedLabEnrollmentIsValidAndStatic(t *testing.T) {
	b, err := os.ReadFile("../../config/site/redfish/graph/kustomization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, value, ok := strings.Cut(string(b), "- name: FLEET_REDFISH_ENROLLMENT\n            value: '")
	value, _, _ = strings.Cut(value, "'\n")
	e, err := ParseEnrollment([]byte(value))
	if !ok || err != nil || e.DHCP != DHCPStatic || len(e.Targets) != 2 {
		t.Fatalf("shipped lab enrollment: %v %+v", err, e)
	}
}
