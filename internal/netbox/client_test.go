package netbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const syntheticDevice = `{
  "id": 1234,
  "name": "server-01.rack-01.example",
  "serial": "SYNTHETIC-0001",
  "asset_tag": null,
  "position": 12.0,
  "device_type": {"model": "Example 1U", "manufacturer": {"name": "Example Vendor", "slug": "example-vendor"}},
  "role": {"name": "Compute", "slug": "compute"},
  "site": {"name": "Example Site", "slug": "example-site"},
  "rack": {"name": "rack-01.example"},
  "face": {"value": "front", "label": "Front"},
  "status": {"value": "active", "label": "Active"},
  "primary_ip4": {"address": "192.0.2.10/28"},
  "primary_ip6": null,
  "oob_ip": {"address": "192.0.2.100/27"},
  "last_updated": "2026-01-02T03:04:05.123456Z",
  "custom_fields": {"oob_password": "synthetic-not-a-secret"},
  "comments": "synthetic comment",
  "config_context": {"secret": "synthetic-context"}
}`

type fakeNetBox struct {
	mu         sync.Mutex
	interfaces int
	auth       []string
	methods    []string
	status     int
	pageShrink bool
}

func (f *fakeNetBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.methods = append(f.methods, r.Method)
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	switch {
	case r.URL.Path == "/api/dcim/devices/1234/":
		_, _ = w.Write([]byte(syntheticDevice))
	case r.URL.Path == "/api/dcim/devices/77/":
		_, _ = w.Write([]byte(`{"id": 78, "name": "wrong"}`))
	case r.URL.Path == "/api/dcim/interfaces/":
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		count := f.interfaces
		var items []string
		for i := offset; i < count && i < offset+limit; i++ {
			items = append(items, fmt.Sprintf(`{"id": %d, "name": "eth%03d", "enabled": true, "next": "https://elsewhere.example/steal"}`, i+1, i))
		}
		if f.pageShrink && offset > 0 {
			count--
		}
		fmt.Fprintf(w, `{"count": %d, "next": "https://elsewhere.example/api/dcim/interfaces/?offset=%d", "results": [%s]}`, count, offset+limit, strings.Join(items, ","))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestClient(t *testing.T, handler http.Handler, token string) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL+"/api/", func() (string, error) { return token, nil }, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestDeviceDecodesOnlyAllowlistedFields(t *testing.T) {
	fake := &fakeNetBox{}
	device, err := newTestClient(t, fake, "synthetic-legacy-token\n").Device(context.Background(), 1234)
	if err != nil {
		t.Fatal(err)
	}
	if device.Name != "server-01.rack-01.example" || device.Serial != "SYNTHETIC-0001" || device.OOBIP.Address != "192.0.2.100/27" || *device.Position != 12 {
		t.Fatalf("unexpected device: %+v", device)
	}
	if got := fake.auth[0]; got != "Token synthetic-legacy-token" {
		t.Fatalf("legacy token scheme = %q", got)
	}
}

func TestV2TokenUsesBearerScheme(t *testing.T) {
	fake := &fakeNetBox{}
	if _, err := newTestClient(t, fake, "nbt_synthetic.value").Device(context.Background(), 1234); err != nil {
		t.Fatal(err)
	}
	if got := fake.auth[0]; got != "Bearer nbt_synthetic.value" {
		t.Fatalf("v2 token scheme = %q", got)
	}
}

func TestInterfacesPaginateWithoutFollowingServerURLs(t *testing.T) {
	fake := &fakeNetBox{interfaces: pageSize + 5}
	interfaces, err := newTestClient(t, fake, "synthetic").Interfaces(context.Background(), 1234)
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) != pageSize+5 {
		t.Fatalf("got %d interfaces", len(interfaces))
	}
	for _, method := range fake.methods {
		if method != http.MethodGet {
			t.Fatalf("client issued %s", method)
		}
	}
}

func TestInterfaceCountChangeDuringPaginationIsUnavailable(t *testing.T) {
	fake := &fakeNetBox{interfaces: pageSize + 5, pageShrink: true}
	if _, err := newTestClient(t, fake, "synthetic").Interfaces(context.Background(), 1234); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestStatusMapping(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusNotFound:            ErrNotFound,
		http.StatusUnauthorized:        ErrUnauthorized,
		http.StatusForbidden:           ErrUnauthorized,
		http.StatusInternalServerError: ErrUnavailable,
		http.StatusFound:               ErrUnavailable,
	} {
		fake := &fakeNetBox{status: status}
		if _, err := newTestClient(t, fake, "synthetic").Device(context.Background(), 1234); !errors.Is(err, want) {
			t.Errorf("HTTP %d: err = %v, want %v", status, err, want)
		}
	}
}

func TestMismatchedDeviceIDIsRejected(t *testing.T) {
	if _, err := newTestClient(t, &fakeNetBox{}, "synthetic").Device(context.Background(), 77); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestEmptyTokenIsNotSent(t *testing.T) {
	fake := &fakeNetBox{}
	if _, err := newTestClient(t, fake, "  \n").Device(context.Background(), 1234); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if len(fake.auth) != 0 {
		t.Fatal("request sent without a credential")
	}
}

func TestOriginValidation(t *testing.T) {
	token := func() (string, error) { return "synthetic", nil }
	for _, origin := range []string{"http://netbox.example", "https://user:pass@netbox.example", "https://netbox.example/?q=1", "netbox.example", "ftp://netbox.example"} {
		if _, err := NewClient(origin, token, nil); err == nil {
			t.Errorf("origin %q accepted", origin)
		}
	}
	for _, origin := range []string{"https://netbox.example", "https://netbox.example/api", "http://127.0.0.1:8080", "http://localhost:8080/"} {
		if _, err := NewClient(origin, token, nil); err != nil {
			t.Errorf("origin %q rejected: %v", origin, err)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect target received %s with Authorization %q", r.URL.Path, r.Header.Get("Authorization"))
	}))
	defer target.Close()
	redirector := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	if _, err := newTestClient(t, redirector, "synthetic").Device(context.Background(), 1234); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
