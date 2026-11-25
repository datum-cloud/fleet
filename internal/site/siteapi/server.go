// Package siteapi is a site's HTTP API for a coordinator that reaches it
// directly, as the simulator site does in the standalone smoke test and in
// synthetic staging. The coordinator delivers assignments and reads the
// site's fence reports and results. The site holds no credential that can
// change custody: it only answers.
package siteapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

// Dispatcher runs each delivered assignment once at a time. A redelivery of
// an assignment that is running is ignored; one that finished returns its
// stored result through the runner.
type Dispatcher struct {
	Runner *site.Runner
	// Context ends every worker. A worker stopped this way stores no
	// result, so the next site process resumes from the journal.
	Context context.Context
	Logger  *slog.Logger

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

func key(assignment site.Assignment) string {
	return assignment.Device + "/" + assignment.Operation.UID + "/" + jsonInt(assignment.Fence)
}

func jsonInt(value int64) string { data, _ := json.Marshal(value); return string(data) }

// Dispatch starts the assignment unless it runs already.
func (d *Dispatcher) Dispatch(assignment site.Assignment) error {
	if err := assignment.Validate(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running == nil {
		d.running = map[string]bool{}
	}
	id := key(assignment)
	if d.running[id] {
		return nil
	}
	d.running[id] = true
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			d.mu.Lock()
			delete(d.running, id)
			d.mu.Unlock()
		}()
		var err error
		if assignment.Operation.Kind == "DeviceRecovery" {
			// A recovery runs nothing: the site raises the fence and
			// confirms the older worker stopped.
			err = d.Runner.Fence(d.Context, assignment)
		} else {
			_, err = d.Runner.Run(d.Context, assignment)
		}
		if err != nil && !errors.Is(err, site.ErrAlreadyRunning) && d.Logger != nil {
			d.Logger.Info("assignment stopped without a stored result", "device", assignment.Device, "operation", assignment.Operation.Name, "fence", assignment.Fence, "reason", err.Error())
		}
	}()
	return nil
}

// Wait returns once every worker stopped.
func (d *Dispatcher) Wait() { d.wg.Wait() }

// FenceReport is the site's fence report for one Device.
type FenceReport struct {
	Seen    int64 `json:"seen"`
	Stopped int64 `json:"stopped"`
}

// Handler serves the site API behind a bearer token.
func Handler(dispatcher *Dispatcher, store *site.Store, results *site.Results, token func() (string, error)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/assignments", func(w http.ResponseWriter, r *http.Request) {
		var assignment site.Assignment
		if err := decode(r, &assignment); err != nil {
			http.Error(w, "the assignment could not be decoded", http.StatusBadRequest)
			return
		}
		if err := dispatcher.Dispatch(assignment); err != nil {
			http.Error(w, "the assignment is invalid", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /v1/fences/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
		seen, stopped, err := store.Current(r.PathValue("namespace") + "/" + r.PathValue("name"))
		if err != nil {
			http.Error(w, "the fence is unreadable", http.StatusInternalServerError)
			return
		}
		write(w, FenceReport{Seen: seen, Stopped: stopped})
	})
	mux.HandleFunc("GET /v1/results", func(w http.ResponseWriter, r *http.Request) {
		held, err := results.Pending()
		if err != nil {
			http.Error(w, "the results are unreadable", http.StatusInternalServerError)
			return
		}
		if held == nil {
			held = []site.Stored{}
		}
		write(w, held)
	})
	mux.HandleFunc("DELETE /v1/results", func(w http.ResponseWriter, r *http.Request) {
		var stored site.Stored
		if err := decode(r, &stored); err != nil || stored.Device == "" || stored.Result.Operation.UID == "" {
			http.Error(w, "the result could not be decoded", http.StatusBadRequest)
			return
		}
		if err := results.Forget(stored.Device, custody.SiteResult{Operation: ops.OperationReference{UID: stored.Result.Operation.UID}, Fence: stored.Result.Fence}); err != nil {
			http.Error(w, "the result could not be deleted", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return bearer(token, mux)
}

func bearer(token func() (string, error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		want, err := token()
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err != nil || want == "" || !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const maxBody = 64 << 10

func decode(r *http.Request, into any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data")
	}
	return nil
}

func write(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
