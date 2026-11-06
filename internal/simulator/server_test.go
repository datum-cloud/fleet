package simulator

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	runtimeToken = "runtime-secret"
	adminToken   = "admin-secret"
)

func TestRuntimeAndAdminHandlersRequireSeparateAuthentication(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	runtime := RuntimeHandler(store, runtimeToken, discardLogger())
	admin := AdminHandler(store, adminToken, discardLogger())
	body := `{"id":"op","kind":"install","assetID":"asset","bindingID":"binding","epoch":1,"fenceToken":"fence","inputs":{"image":"debian@sha256:1"}}`

	assertStatus(t, runtime, http.MethodPost, "/v1/operations", body, "", http.StatusUnauthorized)
	assertStatus(t, runtime, http.MethodPost, "/v1/operations", body, adminToken, http.StatusUnauthorized)
	recorder := assertStatus(t, runtime, http.MethodPost, "/v1/operations", body, runtimeToken, http.StatusOK)
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("authenticated response can be cached or sniffed: %#v", recorder.Header())
	}
	response := recorder.Body.String()
	for _, secret := range []string{"fence", "binding", "debian@sha256:1", "inputs", "fenceToken"} {
		if strings.Contains(response, secret) {
			t.Fatalf("runtime response exposed retained authority %q: %s", secret, response)
		}
	}
	assertStatus(t, runtime, http.MethodGet, "/v1/state", "", runtimeToken, http.StatusNotFound)
	assertStatus(t, admin, http.MethodGet, "/v1/state", "", runtimeToken, http.StatusUnauthorized)
	assertStatus(t, admin, http.MethodGet, "/v1/state", "", adminToken, http.StatusOK)
	assertStatus(t, admin, http.MethodPost, "/v1/operations", body, adminToken, http.StatusNotFound)
}

func TestRuntimeHandlerReloadsRotatedTokenWhileRunning(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenSource := func() (string, error) {
		value, err := os.ReadFile(tokenFile)
		return strings.TrimSpace(string(value)), err
	}
	handler := RuntimeHandlerWithTokenSource(store, tokenSource, discardLogger())
	assertStatus(t, handler, http.MethodGet, "/v1/operations/missing", "", "first", http.StatusNotFound)

	rotated := tokenFile + ".rotated"
	if err := os.WriteFile(rotated, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rotated, tokenFile); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, handler, http.MethodGet, "/v1/operations/missing", "", "first", http.StatusUnauthorized)
	assertStatus(t, handler, http.MethodGet, "/v1/operations/missing", "", "second", http.StatusNotFound)

	if err := os.Remove(tokenFile); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, handler, http.MethodGet, "/v1/operations/missing", "", "second", http.StatusServiceUnavailable)
}

func TestLostResponseRecoversThroughSanitizedLookup(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	runtime := RuntimeHandler(store, runtimeToken, discardLogger())
	admin := AdminHandler(store, adminToken, discardLogger())
	assertStatus(t, admin, http.MethodPut, "/v1/faults/lost-response", `{"enabled":true}`, adminToken, http.StatusNoContent)
	body := `{"id":"op-lost","kind":"install","assetID":"asset","bindingID":"binding","epoch":1,"fenceToken":"fence","inputs":{"image":"debian@sha256:1"}}`
	assertStatus(t, runtime, http.MethodPost, "/v1/operations", body, runtimeToken, http.StatusServiceUnavailable)
	recorder := assertStatus(t, runtime, http.MethodGet, "/v1/operations/op-lost", "", runtimeToken, http.StatusOK)
	if !strings.Contains(recorder.Body.String(), `"result":"installed"`) {
		t.Fatalf("lookup did not expose verified result: %s", recorder.Body.String())
	}
}

func TestHTTPInputBoundariesAndErrorClassification(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	handler := RuntimeHandler(store, runtimeToken, discardLogger())
	valid := `{"id":"op","kind":"install","assetID":"asset","bindingID":"binding","epoch":1,"fenceToken":"fence","inputs":{"image":"debian@sha256:1"}}`

	assertStatus(t, handler, http.MethodPost, "/v1/operations", valid+` {}`, runtimeToken, http.StatusBadRequest)
	assertStatus(t, handler, http.MethodPost, "/v1/operations", strings.Replace(valid, `"id":"op"`, `"id":"op","unknown":true`, 1), runtimeToken, http.StatusBadRequest)
	assertStatus(t, handler, http.MethodPost, "/v1/operations", strings.Repeat("x", maxRequestBodyBytes+1), runtimeToken, http.StatusBadRequest)
	assertStatus(t, handler, http.MethodPost, "/v1/operations", strings.Replace(valid, `"image":"debian@sha256:1"`, `"image":""`, 1), runtimeToken, http.StatusBadRequest)

	assertStatus(t, handler, http.MethodPost, "/v1/operations", valid, runtimeToken, http.StatusOK)
	conflict := strings.Replace(valid, `debian@sha256:1`, `other@sha256:2`, 1)
	assertStatus(t, handler, http.MethodPost, "/v1/operations", conflict, runtimeToken, http.StatusConflict)
}

func TestInternalPersistenceErrorsAreNotDisclosed(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	store.persist = func(string, persisted) (bool, error) { return false, errors.New("secret filesystem path") }
	handler := RuntimeHandler(store, runtimeToken, discardLogger())
	body := `{"id":"op","kind":"cleanup","assetID":"asset","bindingID":"binding","epoch":1,"fenceToken":"fence"}`
	recorder := assertStatus(t, handler, http.MethodPost, "/v1/operations", body, runtimeToken, http.StatusInternalServerError)
	if strings.Contains(recorder.Body.String(), "filesystem") {
		t.Fatalf("internal error leaked to client: %s", recorder.Body.String())
	}

	store.persist = func(string, persisted) (bool, error) { return true, errors.New("directory sync uncertain") }
	recorder = assertStatus(t, handler, http.MethodPost, "/v1/operations", body, runtimeToken, http.StatusServiceUnavailable)
	if !strings.Contains(recorder.Body.String(), `"code":"outcome_unknown"`) {
		t.Fatalf("post-rename outcome was not classified for recovery: %s", recorder.Body.String())
	}
	assertStatus(t, handler, http.MethodGet, "/v1/operations/op", "", runtimeToken, http.StatusOK)
}

func TestReadinessChecksDurableStatePath(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, filepath.Join(dir, "state.json"))
	handler := RuntimeHandler(store, runtimeToken, discardLogger())
	assertStatus(t, handler, http.MethodGet, "/readyz", "", "", http.StatusOK)

	blockingFile := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(blockingFile, "state.json")
	assertStatus(t, handler, http.MethodGet, "/healthz", "", "", http.StatusOK)
	assertStatus(t, handler, http.MethodGet, "/readyz", "", "", http.StatusServiceUnavailable)
}

func assertStatus(t *testing.T, handler http.Handler, method, target, body, token string, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, target, recorder.Code, want, recorder.Body.String())
	}
	return recorder
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
