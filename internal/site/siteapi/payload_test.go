package siteapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/site"
)

// seenPayload records the payload the worker saw.
type seenPayload struct {
	counting
	seen chan *site.Payload
}

func (k *seenPayload) Run(ctx context.Context, work *site.Work) (any, error) {
	k.seen <- work.Bootstrap()
	return k.counting.Run(ctx, work)
}

func sealedAssignment(t *testing.T, uid string, fence int64) (site.Assignment, site.Payload) {
	salt := make([]byte, 32)
	data := []byte(`{"userData":"#cloud-config\n"}`)
	hash := sha256.New()
	hash.Write(salt)
	hash.Write(data)
	payload := site.Payload{Ref: "sealed-" + uid[len(uid)-4:], Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Salt: salt, Data: data}
	a := assignment(t)
	a.Operation.Name, a.Fence = a.Operation.Name+"-"+uid, fence
	a.Inputs = json.RawMessage(fmt.Sprintf(`{"bootstrap":{"ref":%q,"digest":%q}}`, payload.Ref, payload.Digest))
	sum := sha256.Sum256(a.Inputs)
	a.InputDigest = "sha256:" + hex.EncodeToString(sum[:])
	return a, payload
}

// TestTheSiteAPICarriesAPayloadBesideItsAssignment stores a payload only
// when it matches the admitted inputs, hands it to the worker, and keeps no
// copy once the result is stored and answered.
func TestTheSiteAPICarriesAPayloadBesideItsAssignment(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	kind := &seenPayload{seen: make(chan *site.Payload, 4)}
	results := site.NewResults(store)
	runner := &site.Runner{Site: "site-example-a", Store: store, Results: results, Access: access{}, Kinds: map[string]site.Kind{"DeviceTestKind": kind}, Payloads: site.StorePayloads{Store: store}, Poll: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher := &Dispatcher{Runner: runner, Context: ctx}
	server := httptest.NewServer(Handler(dispatcher, store, results, func() (string, error) { return token, nil }))
	defer server.Close()
	remote, err := NewClient(server.URL, func() (string, error) { return token, nil }, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	a, payload := sealedAssignment(t, "00000000-0000-4000-8000-000000000501", 2)

	// A payload the inputs do not name, or none when they name one, is
	// refused and nothing runs.
	altered := payload
	altered.Data = []byte(`{"userData":"other"}`)
	for name, p := range map[string]*site.Payload{"altered": &altered, "missing": nil} {
		if err := remote.Deliver(ctx, a, p); err == nil {
			t.Fatalf("%s payload accepted", name)
		}
	}
	plain := assignment(t)
	if err := remote.Deliver(ctx, plain, &payload); err == nil {
		t.Fatal("a payload for an assignment that names none was accepted")
	}
	dispatcher.Wait()
	if kind.runs.Load() != 0 {
		t.Fatalf("runs = %d", kind.runs.Load())
	}

	if err := remote.Deliver(ctx, a, &payload); err != nil {
		t.Fatal(err)
	}
	if seen := <-kind.seen; seen == nil || seen.Digest != payload.Digest {
		t.Fatalf("the worker saw %v", seen)
	}
	dispatcher.Wait()
	held, err := remote.Results(ctx)
	if err != nil || len(held) != 1 || held[0].Result.Phase != ops.PhaseSucceeded {
		t.Fatalf("results = %+v, %v", held, err)
	}
	// A late redelivery can store the payload again after the worker
	// deleted its copy, while the dispatcher still counts it running.
	// Answering the result deletes it.
	if err := (site.StorePayloads{Store: store}).Put(a, payload); err != nil {
		t.Fatal(err)
	}
	if err := remote.Forget(ctx, held[0]); err != nil {
		t.Fatal(err)
	}
	if records, err := store.Records("payload"); err != nil || len(records) != 0 {
		t.Fatalf("payloads after the answer = %d, %v", len(records), err)
	}
}
