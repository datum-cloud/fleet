package dhcpsync

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.datum.net/fleet/internal/bootnet"
)

const token = "synthetic-dhcp-synchronizer-token-0123456789"

// fakeKea answers status-get with its pid and records each config-set.
type fakeKea struct {
	mu      sync.Mutex
	pid     float64
	fail    bool
	configs []map[string]any
}

func (k *fakeKea) serve(t *testing.T) string {
	dir, err := os.MkdirTemp("", "kea")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "ctrl")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var q struct {
				Command   string         `json:"command"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.NewDecoder(c).Decode(&q)
			k.mu.Lock()
			resp := map[string]any{"result": 0}
			switch {
			case q.Command == "status-get":
				resp["arguments"] = map[string]any{"pid": k.pid}
			case q.Command == "config-set" && k.fail:
				resp = map[string]any{"result": 1, "text": "synthetic rejection"}
			case q.Command == "config-set":
				k.configs = append(k.configs, q.Arguments)
			}
			k.mu.Unlock()
			_ = json.NewEncoder(c).Encode(resp)
			c.Close()
		}
	}()
	return path
}

func (k *fakeKea) applied() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.configs)
}

// fakeSite serves a feed and records applied reports.
type fakeSite struct {
	mu      sync.Mutex
	feed    bootnet.Feed
	reports []bootnet.Applied
}

func (f *fakeSite) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch q.URL.Path {
		case "/dhcp/v1/reservations":
			_ = json.NewEncoder(w).Encode(f.feed)
		case "/dhcp/v1/applied":
			var a bootnet.Applied
			b, _ := io.ReadAll(q.Body)
			if json.Unmarshal(b, &a) != nil || a.Verify() != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.reports = append(f.reports, a)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func (f *fakeSite) set(t *testing.T, rs ...bootnet.Reservation) {
	feed, err := bootnet.NewFeed(rs)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.feed = feed
	f.mu.Unlock()
}

func (f *fakeSite) reported() []bootnet.Applied {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bootnet.Applied(nil), f.reports...)
}

func newSyncer(t *testing.T) (*Syncer, *fakeKea, *fakeSite) {
	kea := &fakeKea{pid: 100}
	site := &fakeSite{}
	site.set(t)
	srv := httptest.NewTLSServer(site.handler(t))
	t.Cleanup(srv.Close)
	base := filepath.Join(t.TempDir(), "kea-dhcp4.conf")
	os.WriteFile(base, []byte(`{"Dhcp4":{"subnet4":[{"id":1,"subnet":"192.0.2.0/24","relay":{"ip-addresses":["192.0.2.1"]}},{"id":2,"subnet":"198.51.100.0/24"}]}}`), 0600)
	return &Syncer{Feed: srv.URL, Token: token, Instance: "kea-0", Socket: kea.serve(t), Base: base, HTTP: srv.Client()}, kea, site
}

var reservation = bootnet.Reservation{MAC: "52:54:00:77:01:0a", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Expires: time.Now().Add(time.Hour)}

func TestAppliesReservationIntoItsExactSubnetThenReports(t *testing.T) {
	s, kea, site := newSyncer(t)
	ctx := context.Background()
	if err := s.Cycle(ctx); err != nil || kea.applied() != 1 || len(site.reported()) != 1 {
		t.Fatalf("empty feed not applied and reported: %v", err)
	}
	site.set(t, reservation)
	if err := s.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	kea.mu.Lock()
	subnets := kea.configs[1]["Dhcp4"].(map[string]any)["subnet4"].([]any)
	kea.mu.Unlock()
	hosts, _ := json.Marshal(subnets[0].(map[string]any)["reservations"])
	if string(hosts) != `[{"hw-address":"52:54:00:77:01:0a","ip-address":"192.0.2.10","option-data":[{"data":"192.0.2.1","name":"routers"}]}]` || subnets[1].(map[string]any)["reservations"] != nil {
		t.Fatalf("reservation not placed in exactly its subnet: %s", hosts)
	}
	reports := site.reported()
	if len(reports) != 2 || !reports[1].Feed.Has(reservation.MAC, reservation.Address) || reports[1].Instance != "kea-0" {
		t.Fatalf("applied revision not reported: %+v", reports)
	}
	// An unchanged feed is neither re-applied nor re-reported; a restarted
	// Kea (new pid) lost its reservations and is re-applied without a report.
	if err := s.Cycle(ctx); err != nil || kea.applied() != 2 || len(site.reported()) != 2 {
		t.Fatalf("unchanged feed churned: %v", err)
	}
	kea.mu.Lock()
	kea.pid = 101
	kea.mu.Unlock()
	if err := s.Cycle(ctx); err != nil || kea.applied() != 3 || len(site.reported()) != 2 {
		t.Fatalf("restarted Kea not re-applied: %v", err)
	}
	site.set(t)
	if err := s.Cycle(ctx); err != nil || len(site.reported()) != 3 || site.reported()[2].Feed.Serves(reservation.MAC) {
		t.Fatalf("withdrawal not applied and reported: %v", err)
	}
}

func TestNothingIsReportedThatKeaDidNotAccept(t *testing.T) {
	s, kea, site := newSyncer(t)
	ctx := context.Background()
	kea.mu.Lock()
	kea.fail = true
	kea.mu.Unlock()
	site.set(t, reservation)
	if err := s.Cycle(ctx); err == nil || len(site.reported()) != 0 {
		t.Fatal("a rejected configuration was reported as applied")
	}
	kea.mu.Lock()
	kea.fail = false
	kea.mu.Unlock()
	site.set(t, bootnet.Reservation{MAC: reservation.MAC, Address: "203.0.113.10/24", Gateway: "203.0.113.1", Expires: reservation.Expires})
	if err := s.Cycle(ctx); err == nil || kea.applied() != 0 || len(site.reported()) != 0 {
		t.Fatal("a reservation without a base subnet was dropped or applied")
	}
	s.Token = "wrong-token-wrong-token-wrong-token-0000"
	if err := s.Cycle(ctx); err == nil {
		t.Fatal("an unauthenticated feed was accepted")
	}
}

func TestForgedFeedIsRejectedBeforeKea(t *testing.T) {
	s, kea, site := newSyncer(t)
	site.set(t, reservation)
	site.mu.Lock()
	site.feed.Reservations[0].Address = "192.0.2.99/24"
	site.mu.Unlock()
	if err := s.Cycle(context.Background()); err == nil || kea.applied() != 0 {
		t.Fatal("a feed whose revision does not match its reservations was applied")
	}
}
