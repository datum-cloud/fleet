package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// inventoryBMC is a TLS Redfish service that records every request and
// whether it carried a credential.
type inventoryBMC struct {
	mu       sync.Mutex
	requests []string
	authed   []bool
	docs     map[string]any
}

func newInventoryBMC() *inventoryBMC {
	nic := func(owner, id, mac string) map[string]any {
		return map[string]any{"@odata.id": owner + "/EthernetInterfaces/" + id, "Id": id, "MACAddress": mac, "LinkStatus": "LinkUp", "SpeedMbps": 25000,
			// An off-schema vendor property must not fail the lenient decode.
			"Oem": map[string]any{"Vendor": map[string]any{"Speed": "fast"}}}
	}
	return &inventoryBMC{docs: map[string]any{
		"/redfish/v1/": map[string]any{"@odata.id": "/redfish/v1", "RedfishVersion": "1.15.0", "UUID": "00000000-0000-4000-8000-0000000000AA", "Vendor": "Example",
			"Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}, "Managers": map[string]string{"@odata.id": "/redfish/v1/Managers"}},
		"/redfish/v1/Managers": map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Managers/BMC"}}},
		"/redfish/v1/Managers/BMC": map[string]any{"Id": "BMC", "Model": "Example BMC", "FirmwareVersion": "1.2.3",
			"EthernetInterfaces": map[string]string{"@odata.id": "/redfish/v1/Managers/BMC/EthernetInterfaces"}},
		"/redfish/v1/Managers/BMC/EthernetInterfaces":   map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Managers/BMC/EthernetInterfaces/1"}}},
		"/redfish/v1/Managers/BMC/EthernetInterfaces/1": nic("/redfish/v1/Managers/BMC", "1", "00-00-5E-00-53-10"),
		"/redfish/v1/Systems":                           map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/1"}}},
		"/redfish/v1/Systems/1": map[string]any{"Id": "1", "UUID": "00000000-0000-4000-8000-00000000000A", "Manufacturer": "Example Vendor",
			"Model": "Example 1U", "SerialNumber": "SYNTHETIC-0001", "SKU": "TAG0001", "PowerState": "Off", "Boot": map[string]any{"BootSourceOverrideTarget": 7},
			"EthernetInterfaces": map[string]string{"@odata.id": "/redfish/v1/Systems/1/EthernetInterfaces"}},
		"/redfish/v1/Systems/1/EthernetInterfaces":       map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/1/EthernetInterfaces/NIC.1"}}},
		"/redfish/v1/Systems/1/EthernetInterfaces/NIC.1": nic("/redfish/v1/Systems/1", "NIC.1", "00:00:5E:00:53:01"),
	}}
}

func (b *inventoryBMC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _, authed := r.BasicAuth()
	b.mu.Lock()
	b.requests = append(b.requests, r.Method+" "+r.URL.Path)
	b.authed = append(b.authed, authed)
	doc, ok := b.docs[r.URL.Path]
	b.mu.Unlock()
	if user, pass, _ := r.BasicAuth(); r.URL.Path != "/redfish/v1/" && (user != "operator" || pass != "synthetic-secret") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !ok || r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(doc)
}

func (b *inventoryBMC) seen() ([]string, []bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.requests...), append([]bool(nil), b.authed...)
}

func tlsBMC(t *testing.T) (*inventoryBMC, *httptest.Server, string) {
	t.Helper()
	bmc := newInventoryBMC()
	server := httptest.NewTLSServer(bmc)
	t.Cleanup(server.Close)
	return bmc, server, PublicKeySHA256(server.Certificate())
}

func TestParsePin(t *testing.T) {
	pin := strings.Repeat("Ab", 32)
	for _, in := range []string{pin, " " + pin + "\n", strings.ToUpper(pin)} {
		if got, err := ParsePin(in); err != nil || got != strings.ToLower(pin) {
			t.Fatalf("ParsePin(%q) = %q, %v", in, got, err)
		}
	}
	colons := strings.TrimSuffix(strings.Repeat("ab:", 32), ":")
	if got, err := ParsePin(colons); err != nil || got != strings.Repeat("ab", 32) {
		t.Fatalf("colon form = %q, %v", got, err)
	}
	for _, bad := range []string{"", "ab", strings.Repeat("zz", 32), strings.Repeat("ab", 31)} {
		if _, err := ParsePin(bad); err == nil {
			t.Fatalf("ParsePin(%q) accepted", bad)
		}
	}
}

func TestProbeRecordsTheKeyAnonymously(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	cert, root, err := Probe(context.Background(), server.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if cert.PublicKeySHA256 != pin || !cert.NotAfter.Equal(server.Certificate().NotAfter) || !cert.SelfSigned {
		t.Fatalf("certificate = %+v, want pin %s", cert, pin)
	}
	if root.RedfishVersion != "1.15.0" || root.UUID != "00000000-0000-4000-8000-0000000000aa" || root.Vendor != "Example" {
		t.Fatalf("service root = %+v", root)
	}
	requests, authed := bmc.seen()
	if len(requests) != 1 || requests[0] != "GET /redfish/v1/" || authed[0] {
		t.Fatalf("probe sent %v (credential %v), want one anonymous GET of the service root", requests, authed)
	}
}

func TestProbeWithoutHandshake(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler()) // plain HTTP: no TLS handshake
	defer server.Close()
	endpoint := "https://" + strings.TrimPrefix(server.URL, "http://")
	if _, _, err := Probe(context.Background(), endpoint, 2*time.Second); !errors.Is(err, ErrNoHandshake) {
		t.Fatalf("err = %v, want ErrNoHandshake", err)
	}
	if _, _, err := Probe(context.Background(), server.URL, 2*time.Second); err == nil {
		t.Fatal("probe accepted a plain http endpoint")
	}
}

func TestPinnedClientReadsInventoryWithTheApprovedPin(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	c, err := NewPinnedClient(server.URL, "operator", "synthetic-secret", strings.ToUpper(pin), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := c.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Managers) != 1 || inv.Managers[0].FirmwareVersion != "1.2.3" || inv.Managers[0].NetworkInterfaces[0].MACAddress != "00:00:5e:00:53:10" {
		t.Fatalf("managers = %+v", inv.Managers)
	}
	s := inv.Systems
	if len(s) != 1 || s[0].UUID != "00000000-0000-4000-8000-00000000000a" || s[0].SerialNumber != "SYNTHETIC-0001" || s[0].SKU != "TAG0001" ||
		s[0].PowerState != "Off" || len(s[0].NetworkInterfaces) != 1 || s[0].NetworkInterfaces[0].MACAddress != "00:00:5e:00:53:01" || s[0].NetworkInterfaces[0].SpeedMbps != 25000 {
		t.Fatalf("systems = %+v", s)
	}
	requests, _ := bmc.seen()
	for _, r := range requests {
		if !strings.HasPrefix(r, "GET ") {
			t.Fatalf("inventory issued %s; discovery is GET only", r)
		}
	}
}

func TestPinMismatchSendsNoRequest(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	other := strings.Repeat("0", 64)
	if other == pin {
		t.Fatal("synthetic pin collided")
	}
	c, err := NewPinnedClient(server.URL, "operator", "synthetic-secret", other, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Inventory(context.Background()); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("err = %v, want ErrPinMismatch", err)
	}
	// The handshake fails before any request is written, so the server never
	// sees one, let alone a credential.
	if requests, _ := bmc.seen(); len(requests) != 0 {
		t.Fatalf("server received %v with a mismatched pin", requests)
	}
}

func TestPinnedClientRequiresHTTPSAndAPin(t *testing.T) {
	if _, err := NewPinnedClient("http://192.0.2.10:80", "operator", "synthetic-secret", strings.Repeat("ab", 32), time.Second); err == nil {
		t.Fatal("pinned client accepted http")
	}
	if _, err := NewPinnedClient("https://192.0.2.10:443", "operator", "synthetic-secret", "", time.Second); err == nil {
		t.Fatal("pinned client accepted an empty pin")
	}
}

func TestInventoryRejectsForeignLinks(t *testing.T) {
	bmc, server, pin := tlsBMC(t)
	bmc.docs["/redfish/v1/Systems"] = map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Managers/BMC"}}}
	c, _ := NewPinnedClient(server.URL, "operator", "synthetic-secret", pin, 5*time.Second)
	if _, err := c.Inventory(context.Background()); err == nil {
		t.Fatal("a System member outside the Systems collection was accepted")
	}
}
