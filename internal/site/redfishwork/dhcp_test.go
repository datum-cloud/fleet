package redfishwork

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.datum.net/fleet/internal/bootnet"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
)

const syncToken = "synthetic-dhcp-synchronizer-token-0123456789"

// TestSyncedDHCPFollowsTheOpenAdmission serves a reservation only for an
// open admission at the Device's stored fence, retains the DHCP owner's
// applied reports, and makes a worker wait for the reservation before a
// network boot and for its withdrawal before it reports.
func TestSyncedDHCPFollowsTheOpenAdmission(t *testing.T) {
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
	const device = "fleet-provider/server-example-001"
	target := redfish.Target{Device: device, MAC: "52:54:00:77:01:0a", Provisioning: redfish.Provisioning{Address: "192.0.2.20/24", Gateway: "192.0.2.1"}}
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	if err := store.Raise(device, 3); err != nil {
		t.Fatal(err)
	}
	boot := Boot{Operation: "00000000-0000-4000-8000-000000000301", Fence: 3, MAC: target.MAC, Token: strings.Repeat("a", 64), Created: now.Add(-time.Minute), Expires: now.Add(time.Hour)}
	if err := store.PutRecord(device, 3, bootRecords, boot.Operation, boot); err != nil {
		t.Fatal(err)
	}
	server := BootServer{Store: store, BootRoot: filepath.Join(root, "boot"), Enrollment: redfish.Enrollment{DHCP: redfish.DHCPSynced, Targets: []redfish.Target{target}}, Now: clock}
	feed, err := server.Reservations()
	if err != nil || !feed.Has(target.MAC, target.Provisioning.Address) {
		t.Fatalf("feed = %+v, %v", feed, err)
	}
	handler, err := server.DHCPHandler(syncToken)
	if err != nil {
		t.Fatal(err)
	}
	feedServer := httptest.NewServer(handler)
	defer feedServer.Close()
	post := func(applied bootnet.Applied) {
		t.Helper()
		data, _ := json.Marshal(applied)
		request, _ := newRequest("POST", feedServer.URL+"/dhcp/v1/applied", string(data))
		response, err := feedServer.Client().Do(request)
		if err != nil || response.StatusCode != 204 {
			t.Fatalf("applied report = %v, %v", response, err)
		}
		response.Body.Close()
	}
	request, _ := newRequest("GET", feedServer.URL+"/dhcp/v1/reservations", "")
	request.Header.Del("Authorization")
	if response, err := feedServer.Client().Do(request); err != nil || response.StatusCode != 401 {
		t.Fatalf("unauthenticated feed = %v, %v", response, err)
	}

	config := Config{BootRoot: server.BootRoot, Poll: 5 * time.Millisecond, Now: clock, Synced: true}
	work := &site.Work{Connection: &site.RedfishConnection{Target: target}}
	short := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	if err := config.awaitReservation(short(), work, boot); err == nil {
		t.Fatal("a worker booted before DHCP applied the reservation")
	}
	post(bootnet.Applied{Instance: "dhcp-a", Feed: feed})
	if err := config.awaitReservation(short(), work, boot); err != nil {
		t.Fatalf("applied reservation not seen: %v", err)
	}
	if err := config.awaitWithdrawal(short(), work, boot); err == nil {
		t.Fatal("a reservation still served counted as withdrawn")
	}
	empty, _ := bootnet.NewFeed(nil)
	post(bootnet.Applied{Instance: "dhcp-a", Feed: empty})
	if err := config.awaitWithdrawal(short(), work, boot); err != nil {
		t.Fatalf("withdrawal not seen: %v", err)
	}
	// A newer fence supersedes the admission: its reservation goes.
	if err := store.Raise(device, 4); err != nil {
		t.Fatal(err)
	}
	if feed, err := server.Reservations(); err != nil || len(feed.Reservations) != 0 {
		t.Fatalf("feed after a newer fence = %+v, %v", feed, err)
	}
}

func newRequest(method, url, body string) (*http.Request, error) {
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err == nil {
		request.Header.Set("Authorization", "Bearer "+syncToken)
	}
	return request, err
}
