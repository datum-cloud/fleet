package fleet

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalRedfishSessionCanBeDeleted(t *testing.T) {
	nextCalls := 0
	h := newLocalAuth("fleet", "secret", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalls++ }))
	login := httptest.NewRequest(http.MethodPost, "/redfish/v1/SessionService/Sessions", strings.NewReader(`{"UserName":"fleet","Password":"secret"}`))
	login.Host = "127.0.0.1:9443"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, login)
	if w.Code != http.StatusCreated || w.Header().Get("X-Auth-Token") == "" {
		t.Fatalf("login: %d %#v", w.Code, w.Header())
	}
	token := w.Header().Get("X-Auth-Token")
	del := httptest.NewRequest(http.MethodDelete, "/redfish/v1/SessionService/Sessions/local", nil)
	del.Host = "127.0.0.1:9443"
	del.Header.Set("X-Auth-Token", token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, del)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	get := httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems", nil)
	get.Host = "127.0.0.1:9443"
	get.Header.Set("X-Auth-Token", token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, get)
	if w.Code != http.StatusUnauthorized || nextCalls != 0 {
		t.Fatalf("deleted token accepted: status=%d calls=%d", w.Code, nextCalls)
	}
}

func TestLocalConnectorAllowsSlashlessServiceRootDiscovery(t *testing.T) {
	called := false
	h := newLocalAuth("fleet", "secret", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/redfish/v1/" {
			t.Fatalf("path = %q, want canonical service root", r.URL.Path)
		}
	}))
	r := httptest.NewRequest(http.MethodGet, "/redfish/v1", nil)
	r.Host = "127.0.0.1:9443"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("service root discovery: called=%v status=%d", called, w.Code)
	}
}

func TestLocalConnectorRejectsArbitraryRouting(t *testing.T) {
	h := newLocalAuth("fleet", "secret", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("rejected target reached proxy")
	}))
	for _, test := range []struct {
		target string
		host   string
	}{
		{target: "/redfish/v1/Systems", host: "bmc.private"},
		{target: "http://bmc.private/redfish/v1/Systems", host: "bmc.private"},
		{target: "/redfish/v1/Systems/../Managers", host: "127.0.0.1:9443"},
	} {
		r := httptest.NewRequest(http.MethodGet, test.target, nil)
		r.Host = test.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("target=%q host=%q: %d", test.target, test.host, w.Code)
		}
	}
}

func TestSessionClientUsesMetalAPI(t *testing.T) {
	a := &apiClient{base: "https://api.example.invalid"}
	if got, want := a.url("default", "session-a"), "https://api.example.invalid/apis/metal.datumapis.com/v1alpha1/namespaces/default/machinemanagementsessions/session-a"; got != want {
		t.Fatalf("session endpoint = %q, want %q", got, want)
	}
}
