package redfish

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// bmcStub serves fixed Redfish documents and records every request.
type bmcStub struct {
	mu       sync.Mutex
	docs     map[string]any
	headers  map[string]http.Header
	status   map[string]int // "METHOD path" -> status
	hold     map[string]time.Duration
	requests []string
	ifMatch  []string
}

func newStub() *bmcStub {
	sys := func(id, uuid string) map[string]any {
		p := "/redfish/v1/Systems/" + id
		return map[string]any{"@odata.id": p, "Id": id, "UUID": uuid, "PowerState": "Off",
			"Boot": map[string]any{"BootSourceOverrideEnabled": "Disabled", "BootSourceOverrideTarget": "None",
				"BootSourceOverrideTarget@Redfish.AllowableValues": []string{"Pxe", "Hdd"}},
			"@Redfish.Settings": map[string]any{"SettingsObject": map[string]string{"@odata.id": p + "/Settings"}},
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": p + "/Actions/ComputerSystem.Reset",
				"ResetType@Redfish.AllowableValues": []string{"On", "ForceOff"}}}}
	}
	return &bmcStub{docs: map[string]any{
		"/redfish/v1/":                 map[string]any{"@odata.id": "/redfish/v1", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}},
		"/redfish/v1/Systems":          map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/A"}}, "Members@odata.nextLink": "/redfish/v1/Systems?$skip=1"},
		"/redfish/v1/Systems?$skip=1":  map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/B"}}},
		"/redfish/v1/Systems/A":        sys("A", uuidA),
		"/redfish/v1/Systems/B":        sys("B", uuidB),
		"/redfish/v1/Systems/Settings": map[string]any{},
	}, headers: map[string]http.Header{}, status: map[string]int{}, hold: map[string]time.Duration{}}
}

func (b *bmcStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path
	if r.URL.RawQuery != "" {
		key += "?" + r.URL.RawQuery
	}
	b.mu.Lock()
	b.requests = append(b.requests, r.Method+" "+key)
	if r.Method != http.MethodGet {
		b.ifMatch = append(b.ifMatch, r.Header.Get("If-Match"))
	}
	doc, ok := b.docs[key]
	code, coded := b.status[r.Method+" "+key]
	hold := b.hold[r.Method+" "+key]
	for k, v := range b.headers[key] {
		w.Header()[k] = v
	}
	b.mu.Unlock()
	time.Sleep(hold)
	if user, pass, _ := r.BasicAuth(); key != "/redfish/v1/" && (user != "operator" || pass != "synthetic-secret" || r.Header.Get("OData-Version") != "4.0") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case coded:
		w.WriteHeader(code)
		w.Write([]byte(`{"error":{"message":"echo synthetic-secret"}}`))
	case r.Method != http.MethodGet:
		w.WriteHeader(http.StatusNoContent)
	case !ok:
		w.WriteHeader(http.StatusNotFound)
	default:
		json.NewEncoder(w).Encode(doc)
	}
}

func (b *bmcStub) count(prefix string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, r := range b.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func stubClient(t *testing.T, b *bmcStub, timeout time.Duration) *Client {
	t.Helper()
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "operator", "synthetic-secret", timeout)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte("operator:synthetic-secret"))) || strings.Contains(err.Error(), "127.0.0.1")) {
		t.Fatalf("error exposes a credential, body or endpoint: %v", err)
	}
}

func TestDiscoveryWalksEveryPageOfTheSystemsCollection(t *testing.T) {
	b := newStub()
	c := stubClient(t, b, time.Second)
	s, err := c.Discover(context.Background(), Target{SystemID: "B", SystemUUID: strings.ToUpper(uuidB)})
	if err != nil || s.Path != "/redfish/v1/Systems/B" || s.ResetTarget != s.Path+"/Actions/ComputerSystem.Reset" || strings.Join(s.BootTargets, ",") != "Pxe,Hdd" {
		t.Fatalf("paged discovery: %+v %v", s, err)
	}
	// The enrolled UUID also on a later page is ambiguous, never first-match.
	b.docs["/redfish/v1/Systems/B"].(map[string]any)["UUID"] = uuidA
	if _, err := c.Discover(context.Background(), Target{SystemID: "A", SystemUUID: uuidA}); !errors.Is(err, ErrIdentity) {
		t.Fatalf("duplicate UUID on a later page: %v", err)
	}
	if n := b.count("POST") + b.count("PATCH"); n != 0 {
		t.Fatalf("discovery mutated: %d", n)
	}
}

func TestDiscoveryRefusesPartialOrForeignCollections(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*bmcStub)
		want   error
	}{
		"unreadable member": {func(b *bmcStub) { b.status["GET /redfish/v1/Systems/B"] = http.StatusInternalServerError }, Status{}},
		"rejected member":   {func(b *bmcStub) { b.status["GET /redfish/v1/Systems/B"] = http.StatusForbidden }, ErrUnauthorized},
		"member under another collection": {func(b *bmcStub) {
			b.docs["/redfish/v1/Systems/B"].(map[string]any)["@odata.id"] = "/redfish/v1/Managers/B"
		}, ErrIdentity},
		"absolute Systems link to another host": {func(b *bmcStub) {
			b.docs["/redfish/v1/"] = map[string]any{"Systems": map[string]string{"@odata.id": "http://198.51.100.1/redfish/v1/Systems"}}
		}, ErrIdentity},
		"reset target off the System": {func(b *bmcStub) {
			b.docs["/redfish/v1/Systems/A"].(map[string]any)["Actions"] = map[string]any{"#ComputerSystem.Reset": map[string]any{"target": "/redfish/v1/Systems/B/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On", "ForceOff"}}}
		}, ErrUnsupported},
		"ActionInfo off the System": {func(b *bmcStub) {
			b.docs["/redfish/v1/Systems/A"].(map[string]any)["Actions"] = map[string]any{"#ComputerSystem.Reset": map[string]any{"target": "/redfish/v1/Systems/A/Actions/ComputerSystem.Reset", "@Redfish.ActionInfo": "/redfish/v1/Systems/B/ResetActionInfo"}}
		}, ErrUnsupported},
		"one-time override not advertised": {func(b *bmcStub) {
			b.docs["/redfish/v1/Systems/A"].(map[string]any)["Boot"].(map[string]any)["BootSourceOverrideEnabled@Redfish.AllowableValues"] = []string{"Continuous"}
		}, ErrUnsupported},
		"oversized response": {func(b *bmcStub) {
			b.docs["/redfish/v1/Systems/B"].(map[string]any)["Pad"] = strings.Repeat("x", maxBody)
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			b := newStub()
			tc.mutate(b)
			_, err := stubClient(t, b, time.Second).Discover(context.Background(), Target{SystemID: "A", SystemUUID: uuidA})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want) && !errors.As(err, new(Status))) || (tc.want == nil && errors.Is(err, ErrIdentity)) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if _, isStatus := tc.want.(Status); isStatus && !errors.As(err, new(Status)) {
				t.Fatalf("got %v, want an HTTP status", err)
			}
			noSecret(t, err)
			if n := b.count("POST") + b.count("PATCH"); n != 0 {
				t.Fatalf("discovery mutated: %d", n)
			}
		})
	}
}

func TestBootOverrideIsOneConditionalPatchWithoutFallback(t *testing.T) {
	b := newStub()
	b.headers["/redfish/v1/Systems/A"] = http.Header{"Etag": {`W/"7"`}}
	c := stubClient(t, b, time.Second)
	sys, err := c.Discover(context.Background(), Target{SystemID: "A", SystemUUID: uuidA})
	if err != nil {
		t.Fatal(err)
	}
	b.status["PATCH /redfish/v1/Systems/A"] = http.StatusPreconditionFailed
	err = c.SetBootOnce(context.Background(), sys, "Pxe")
	if !errors.Is(err, ErrPrecondition) || b.count("PATCH") != 1 || b.ifMatch[0] != `W/"7"` || b.count("GET /redfish/v1/Systems/A/Settings")+b.count("PATCH /redfish/v1/Systems/A/Settings") != 0 {
		t.Fatalf("412 must be one conditional PATCH, no retry or Settings fallback: %v %v %v", err, b.requests, b.ifMatch)
	}
	noSecret(t, err)
	delete(b.status, "PATCH /redfish/v1/Systems/A")
	b.status["PATCH /redfish/v1/Systems/A"] = http.StatusInternalServerError
	if err := c.SetBootOnce(context.Background(), sys, "Pxe"); !errors.As(err, new(Status)) || b.count("PATCH") != 2 {
		t.Fatalf("a failed PATCH must not fall back to the Settings resource: %v %v", err, b.requests)
	}
	if err := c.SetBootOnce(context.Background(), sys, "Cd"); !errors.Is(err, ErrUnsupported) || b.count("PATCH") != 2 {
		t.Fatal("unadvertised boot target was sent")
	}
}

func TestResetIsOnePostOnFailureOrLostResponse(t *testing.T) {
	b := newStub()
	b.headers["/redfish/v1/Systems/A"] = http.Header{"Etag": {`"7"`}}
	c := stubClient(t, b, 200*time.Millisecond)
	sys, err := c.Discover(context.Background(), Target{SystemID: "A", SystemUUID: uuidA})
	if err != nil {
		t.Fatal(err)
	}
	reset := "POST /redfish/v1/Systems/A/Actions/ComputerSystem.Reset"
	for i, inject := range []func(){
		func() { b.status[reset] = http.StatusPreconditionFailed },
		func() { b.status[reset] = http.StatusServiceUnavailable },
		func() { delete(b.status, reset); b.hold[reset] = time.Second },
	} {
		inject()
		err := c.Reset(context.Background(), sys, "On")
		if err == nil || b.count(reset) != i+1 {
			t.Fatalf("case %d: a failed or lost Reset must be sent exactly once and reported: %v %v", i, err, b.requests)
		}
		noSecret(t, err)
	}
	for _, m := range b.ifMatch {
		if m != "" {
			t.Fatalf("Reset carried the System ETag as If-Match: %v", b.ifMatch)
		}
	}
	if err := c.Reset(context.Background(), sys, "GracefulRestart"); !errors.Is(err, ErrUnsupported) || b.count(reset) != 3 {
		t.Fatal("unadvertised ResetType was sent")
	}
	if b.count("POST /redfish/v1/SessionService") != 0 {
		t.Fatal("client created a session")
	}
}

func TestClientRejectsUntrustedTLSAndEnvironmentProxy(t *testing.T) {
	reached := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()
	c, err := NewClient(srv.URL, "operator", "synthetic-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Discover(context.Background(), Target{SystemID: "A", SystemUUID: uuidA})
	if err == nil || reached {
		t.Fatalf("untrusted certificate accepted: %v", err)
	}
	noSecret(t, err)
	if c.config.HTTPClient.Transport.(guard).next.(*http.Transport).Proxy != nil {
		t.Fatal("BMC requests may be sent through an environment proxy")
	}
}

// Observe reads the BMC's LastResetTime, in the lab's microsecond form, and
// leaves it zero when the BMC reports none or an unreadable time.
func TestObserveReadsTheLastResetTime(t *testing.T) {
	b := newStub()
	c := stubClient(t, b, time.Second)
	sys, err := c.Discover(context.Background(), Target{SystemID: "A", SystemUUID: uuidA})
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[any]time.Time{
		"2026-01-02T03:04:05.123456+00:00": time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC),
		"2026-01-02T03:04:05Z":             time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"not a time":                       {},
		nil:                                {},
	} {
		doc := b.docs["/redfish/v1/Systems/A"].(map[string]any)
		if value == nil {
			delete(doc, "LastResetTime")
		} else {
			doc["LastResetTime"] = value
		}
		power, err := c.Observe(context.Background(), sys)
		if err != nil || !power.LastResetTime.Equal(want) {
			t.Fatalf("LastResetTime %v: got %v, %v", value, power.LastResetTime, err)
		}
	}
}
