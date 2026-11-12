package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.datum.net/fleet/internal/journal"
)

type fakeAdapter struct {
	resets int
	boots  int
	lost   bool
}

func (a *fakeAdapter) Observe(context.Context) (System, error) {
	return System{PowerState: "Off", BootTarget: "Hdd"}, nil
}
func (a *fakeAdapter) SetOneTimeBoot(context.Context, string) error { a.boots++; return nil }
func (a *fakeAdapter) Reset(context.Context, string) error {
	a.resets++
	if a.lost {
		a.lost = false
		return ErrResponseLost
	}
	return nil
}
func (a *fakeAdapter) PhysicalTarget() string { return "private-target" }

func newHandler(t *testing.T, a Adapter) *Handler {
	t.Helper()
	s, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	return &Handler{Adapter: a, Journal: s, Now: func() time.Time { return now }, Authority: Authority{SessionUID: "session", ClaimUID: "claim", MachineUID: "machine", BindingID: "binding", BindingEpoch: 7, AdapterRoute: "sushy-libvirt", OperationEpoch: 1, ExpiresAt: now.Add(time.Hour), FreshUntil: now.Add(time.Minute)}}
}

func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:9443"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNarrowProfileAndSingleSystem(t *testing.T) {
	h := newHandler(t, &fakeAdapter{})
	for _, p := range []string{"/redfish/v1/AccountService", "/redfish/v1/Managers", "/redfish/v1/UpdateService", "/redfish/v1/Systems/guessed", "/redfish/v1/Systems/allocated/Oem/Reset"} {
		if got := request(h, http.MethodGet, p, "").Code; got != http.StatusNotFound {
			t.Errorf("%s: %d", p, got)
		}
	}
	w := request(h, http.MethodGet, "/redfish/v1/Systems", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-target") {
		t.Fatalf("systems: %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["Members@odata.count"].(float64) != 1 {
		t.Fatalf("unexpected collection %s", w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems", nil)
	r.Host = "second-bmc.internal"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("Host manipulation accepted: %d", rr.Code)
	}
}

func TestUnsupportedMutationsRejected(t *testing.T) {
	a := &fakeAdapter{}
	h := newHandler(t, a)
	bad := []string{`{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Usb"}}`, `{"AssetTag":"x"}`}
	for _, body := range bad {
		if got := request(h, http.MethodPatch, "/redfish/v1/Systems/allocated", body).Code; got != 400 {
			t.Fatalf("patch %s: %d", body, got)
		}
	}
	if got := request(h, http.MethodPost, "/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset", `{"ResetType":"Nmi"}`).Code; got != 400 {
		t.Fatalf("reset: %d", got)
	}
	if a.boots != 0 || a.resets != 0 {
		t.Fatal("rejected request reached adapter")
	}
}

func TestLostResponseIsUnknownAndNeverDuplicatesReset(t *testing.T) {
	a := &fakeAdapter{lost: true}
	h := newHandler(t, a)
	body := `{"ResetType":"ForceRestart"}`
	w1 := request(h, http.MethodPost, "/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset", body)
	w2 := request(h, http.MethodPost, "/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset", body)
	if w1.Code != 202 || w2.Code != 202 {
		t.Fatalf("codes %d %d", w1.Code, w2.Code)
	}
	if a.resets != 1 {
		t.Fatalf("reset executed %d times", a.resets)
	}
	if !strings.Contains(w2.Body.String(), "Unknown") {
		t.Fatalf("retry did not preserve unknown: %s", w2.Body.String())
	}
}

func TestTaskCollectionListsOnlyCurrentSession(t *testing.T) {
	h := newHandler(t, &fakeAdapter{})
	mutation := request(h, http.MethodPost, "/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`)
	if mutation.Code != http.StatusAccepted {
		t.Fatalf("mutation: %d %s", mutation.Code, mutation.Body.String())
	}
	collection := request(h, http.MethodGet, "/redfish/v1/TaskService/Tasks", "")
	if collection.Code != http.StatusOK || !strings.Contains(collection.Body.String(), `"Members@odata.count":1`) {
		t.Fatalf("tasks: %d %s", collection.Code, collection.Body.String())
	}
}

func TestStaleAuthorityFailsClosed(t *testing.T) {
	h := newHandler(t, &fakeAdapter{})
	h.Authority.FreshUntil = time.Unix(999, 0)
	w := request(h, http.MethodPost, "/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "AuthorityStale") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestExpiredSessionFailsClosed(t *testing.T) {
	a := &fakeAdapter{}
	h := newHandler(t, a)
	h.Authority.ExpiresAt = time.Unix(999, 0)
	w := request(h, http.MethodPost, "/redfish/v1/Systems/allocated/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "SessionExpired") || a.resets != 0 {
		t.Fatalf("expired request: code=%d resets=%d body=%s", w.Code, a.resets, w.Body.String())
	}
}

func TestAdapterResponseLostSentinel(t *testing.T) {
	if !errors.Is(ErrResponseLost, ErrResponseLost) {
		t.Fatal("sentinel")
	}
}
