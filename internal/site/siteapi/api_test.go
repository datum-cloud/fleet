package siteapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

const (
	token  = "synthetic-site-token"
	device = "fleet-provider/server-example-001"
)

// access admits every assignment; the kind counts its runs.
type access struct{}

func (access) Prepare(context.Context, site.Assignment) (any, *custody.Refusal) { return nil, nil }

type counting struct{ runs atomic.Int32 }

func (*counting) Check(context.Context, *site.Work) *custody.Refusal { return nil }
func (k *counting) Run(ctx context.Context, work *site.Work) (any, error) {
	k.runs.Add(1)
	if err := work.Step(ctx, site.Step{Name: "effect", Send: func(context.Context) error { time.Sleep(50 * time.Millisecond); return nil }}); err != nil {
		return nil, err
	}
	return map[string]string{"done": "true"}, nil
}

func assignment(t *testing.T) site.Assignment {
	inputs := json.RawMessage(`{"step":1}`)
	digest, err := custody.SpecDigest(map[string]int{"step": 1})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return site.Assignment{Device: device, Operation: ops.OperationReference{Kind: "DeviceTestKind", Name: "op-example", UID: "00000000-0000-4000-8000-000000000401"}, Fence: 2,
		AdmittedAt: now, Deadline: now.Add(time.Minute), InputDigest: digest, Inputs: inputs,
		Snapshot: ops.DeviceSnapshot{BMCEndpoint: "https://[2001:db8::1]", SystemUUID: "00000000-0000-4000-8000-000000000001", BMCPublicKeySHA256: "aa"}}
}

// TestTheSiteAPI authenticates every call but readiness, runs a redelivered
// assignment once, reports the site's fence and stored result, and forgets
// a result only when the coordinator answers it.
func TestTheSiteAPI(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	kind := &counting{}
	results := site.NewResults(store)
	runner := &site.Runner{Site: "site-example-a", Store: store, Results: results, Access: access{}, Kinds: map[string]site.Kind{"DeviceTestKind": kind}, Poll: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher := &Dispatcher{Runner: runner, Context: ctx}
	server := httptest.NewServer(Handler(dispatcher, store, results, func() (string, error) { return token, nil }))
	defer server.Close()
	for name, header := range map[string]string{"no token": "", "wrong token": "Bearer other"} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/results", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		response, err := server.Client().Do(request)
		if err != nil || response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: %v, %v", name, response, err)
		}
		response.Body.Close()
	}
	if response, err := server.Client().Get(server.URL + "/readyz"); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("readiness = %v, %v", response, err)
	}
	remote, err := NewClient(server.URL, func() (string, error) { return token, nil }, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	a := assignment(t)
	for range 3 {
		if err := remote.Deliver(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	var held []site.Stored
	deadline := time.Now().Add(10 * time.Second)
	for len(held) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no result was stored")
		}
		time.Sleep(20 * time.Millisecond)
		if held, err = remote.Results(ctx); err != nil {
			t.Fatal(err)
		}
	}
	dispatcher.Wait()
	if kind.runs.Load() != 1 || len(held) != 1 || held[0].Result.Phase != ops.PhaseSucceeded || held[0].Result.Fence != 2 {
		t.Fatalf("runs = %d, results = %+v", kind.runs.Load(), held)
	}
	if report, reported, err := remote.Fence(ctx, device); err != nil || !reported || report.Seen != 2 || report.Stopped != 2 {
		t.Fatalf("fence = %+v, %v, %v", report, reported, err)
	}
	// A redelivery after the result is stored returns it and runs nothing.
	if err := remote.Deliver(ctx, a); err != nil {
		t.Fatal(err)
	}
	dispatcher.Wait()
	if kind.runs.Load() != 1 {
		t.Fatalf("a redelivered assignment ran again: %d", kind.runs.Load())
	}
	if err := remote.Forget(ctx, held[0]); err != nil {
		t.Fatal(err)
	}
	if held, err := remote.Results(ctx); err != nil || len(held) != 0 {
		t.Fatalf("results after forget = %+v, %v", held, err)
	}
	// An invalid assignment is refused before anything runs.
	bad := a
	bad.Fence = 0
	if err := remote.Deliver(ctx, bad); err == nil {
		t.Fatal("an invalid assignment was accepted")
	}
}
