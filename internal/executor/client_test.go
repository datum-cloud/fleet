package executor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.datum.net/fleet/internal/simulator"
)

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func clientForHandler(t *testing.T, handler http.Handler, token string) *HTTPClient {
	t.Helper()
	client, err := NewHTTPClient("http://executor", token, &http.Client{Transport: handlerTransport{handler: handler}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestHTTPClientAuthenticatesAndRecoversLostResponse(t *testing.T) {
	store, err := simulator.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetFault("lost-response", true); err != nil {
		t.Fatal(err)
	}
	client := clientForHandler(t, simulator.RuntimeHandler(store, "secret", slog.New(slog.NewTextHandler(os.Stderr, nil))), "secret")
	request := simulator.Request{ID: "op-1", Kind: "cleanup", AssetID: "asset", BindingID: "binding", Epoch: 1, FenceToken: "fence"}
	if _, err := client.Execute(context.Background(), request); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Execute error = %v", err)
	}
	result, found, err := client.Lookup(context.Background(), request.ID)
	if err != nil || !found || result.ID != request.ID || result.Phase != "Succeeded" {
		t.Fatalf("Lookup = %#v, %v, %v", result, found, err)
	}
}

func TestHTTPClientRejectsWrongAuthenticationAndMalformedTerminalResult(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer right" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})
		client := clientForHandler(t, handler, "wrong")
		if _, _, err := client.Lookup(context.Background(), "op"); err == nil {
			t.Fatal("expected authentication failure")
		}
	})
	t.Run("terminal result", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"id":"wrong","kind":"cleanup","phase":"Succeeded","completedAt":"2026-01-01T00:00:00Z"}`)
		})
		client := clientForHandler(t, handler, "token")
		_, err := client.Execute(context.Background(), simulator.Request{ID: "wanted", Kind: "cleanup"})
		if !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestHTTPClientRejectsOperationSpecificFalseEvidence(t *testing.T) {
	completed := `"2026-01-01T00:00:00Z"`
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := "op"
				if method == http.MethodPost {
					id = "op"
				}
				_, _ = io.WriteString(w, `{"id":"`+id+`","kind":"cleanup","phase":"Succeeded","result":"installed","completedAt":`+completed+`}`)
			})
			client := clientForHandler(t, handler, "token")
			if method == http.MethodPost {
				if _, err := client.Execute(context.Background(), simulator.Request{ID: "op", Kind: "cleanup"}); !errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("Execute error = %v", err)
				}
			} else if _, _, err := client.Lookup(context.Background(), "op"); err == nil {
				t.Fatal("Lookup accepted false cleanup evidence")
			}
		})
	}
}

func TestHTTPClientRequiresBoundedTimeout(t *testing.T) {
	if _, err := NewHTTPClient("http://executor", "token", &http.Client{}); err == nil {
		t.Fatal("accepted unbounded custom client")
	}
	client, err := NewHTTPClient("http://executor", "token", nil)
	if err != nil || client.http.Timeout <= 0 {
		t.Fatalf("default client timeout = %v, error=%v", client.http.Timeout, err)
	}
}

func TestHTTPClientReloadsRotatedTokenForEveryRequest(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "first"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client, err := NewHTTPClientFromTokenFile("http://executor", tokenFile, &http.Client{Transport: handlerTransport{handler: handler}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Lookup(context.Background(), "before-rotation"); err != nil {
		t.Fatalf("Lookup before rotation: %v", err)
	}

	// Model kubelet replacing the projected file while only the provider side
	// restarts and begins requiring the new token.
	want = "second"
	rotated := tokenFile + ".rotated"
	if err := os.WriteFile(rotated, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rotated, tokenFile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Lookup(context.Background(), "after-provider-restart"); err != nil {
		t.Fatalf("Lookup after rotation: %v", err)
	}
}

func TestHTTPClientFailsClosedWhenRotatedTokenIsUnavailable(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	client, err := NewHTTPClientFromTokenFile("http://executor", tokenFile, &http.Client{Transport: handlerTransport{handler: handler}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tokenFile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Lookup(context.Background(), "op"); err == nil {
		t.Fatal("Lookup accepted an unavailable token")
	}
	if called {
		t.Fatal("request was sent without a current token")
	}
}
