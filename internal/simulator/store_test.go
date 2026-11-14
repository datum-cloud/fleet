package simulator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistenceIdempotencyFencingAndCleanupRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := openStore(t, path)
	install := operationRequest("op-install", "install", 1, `{"image":"debian@sha256:1"}`)

	op, lost, err := store.Submit(install)
	if err != nil || lost || op.Result != "installed" {
		t.Fatalf("submit install: op=%#v lost=%v err=%v", op, lost, err)
	}
	for i := range install.Inputs {
		install.Inputs[i] = 'x'
	}
	got, ok := store.Get("op-install")
	if !ok || string(got.Inputs) != `{"image":"debian@sha256:1"}` {
		t.Fatalf("retained input aliases caller memory: %#v", got)
	}
	got.Inputs[0] = 'x'
	gotAgain, _ := store.Get("op-install")
	if string(gotAgain.Inputs) != `{"image":"debian@sha256:1"}` {
		t.Fatalf("returned operation aliases retained state: %#v", gotAgain)
	}

	retry := operationRequest("op-install", "install", 1, `{"image":"debian@sha256:1"}`)
	if _, lost, err := store.Submit(retry); err != nil || lost {
		t.Fatalf("idempotent retry: lost=%v err=%v", lost, err)
	}
	conflict := retry
	conflict.Inputs = json.RawMessage(`{"image":"other"}`)
	if _, _, err := store.Submit(conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("want operation conflict, got %v", err)
	}
	sameEpochOtherToken := operationRequest("op-other-token", "power", 1, `{"state":"On"}`)
	sameEpochOtherToken.FenceToken = "other-token"
	if _, _, err := store.Submit(sameEpochOtherToken); !errors.Is(err, ErrFenced) {
		t.Fatalf("want same-epoch token fenced, got %v", err)
	}
	prematureBinding := operationRequest("op-premature-binding", "power", 2, `{"state":"On"}`)
	prematureBinding.BindingID = "binding-b"
	prematureBinding.FenceToken = "token-b"
	if _, _, err := store.Submit(prematureBinding); !errors.Is(err, ErrFenced) {
		t.Fatalf("want new binding fenced before cleanup, got %v", err)
	}

	cleanup := operationRequest("op-cleanup", "cleanup", 1, "")
	if _, _, err := store.Submit(cleanup); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	late := operationRequest("op-late", "power", 1, `{"state":"On"}`)
	if _, _, err := store.Submit(late); !errors.Is(err, ErrFenced) {
		t.Fatalf("want cleaned binding fenced, got %v", err)
	}

	newBinding := operationRequest("op-new-binding", "power", 2, `{"state":"On"}`)
	newBinding.BindingID = "binding-b"
	newBinding.FenceToken = "token-b"
	if _, _, err := store.Submit(newBinding); err != nil {
		t.Fatalf("new binding: %v", err)
	}

	reopened := openStore(t, path)
	snapshot := reopened.Snapshot()
	asset := snapshot.Assets["asset-1"]
	if asset == nil || asset.Epoch != 2 || asset.BindingID != "binding-b" || asset.PowerState != "On" || asset.AuthorityClosed {
		t.Fatalf("unexpected recovered asset: %#v", asset)
	}
	if recovered, ok := reopened.Get("op-cleanup"); !ok || recovered.Result != "clean" {
		t.Fatalf("cleanup operation not recovered: %#v ok=%v", recovered, ok)
	}
}

func TestLostResponseIsConsumedWithDurableEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := openStore(t, path)
	if err := store.SetFault("lost-response", true); err != nil {
		t.Fatal(err)
	}
	req := operationRequest("op-lost", "install", 1, `{"image":"debian@sha256:1"}`)
	if _, lost, err := store.Submit(req); err != nil || !lost {
		t.Fatalf("want durable lost response, lost=%v err=%v", lost, err)
	}

	reopened := openStore(t, path)
	if op, ok := reopened.Get(req.ID); !ok || op.Result != "installed" {
		t.Fatalf("lost response effect was not durable: %#v ok=%v", op, ok)
	}
	if _, lost, err := reopened.Submit(req); err != nil || lost {
		t.Fatalf("recovery lookup/retry re-fired fault: lost=%v err=%v", lost, err)
	}
	next := operationRequest("op-next", "power", 1, `{"state":"On"}`)
	if _, lost, err := reopened.Submit(next); err != nil || lost {
		t.Fatalf("one-shot fault leaked to next operation: lost=%v err=%v", lost, err)
	}
}

func TestInvalidRequestsHaveNoAuthorityOrEffect(t *testing.T) {
	tests := map[string]Request{
		"unsupported kind": operationRequest("op", "erase", 1, `{}`),
		"missing image":    operationRequest("op", "install", 1, `{}`),
		"unknown input":    operationRequest("op", "install", 1, `{"image":"x","extra":true}`),
		"malformed input":  operationRequest("op", "install", 1, `{"image":`),
		"invalid power":    operationRequest("op", "power", 1, `{"state":"Reboot"}`),
		"cleanup input":    operationRequest("op", "cleanup", 1, `{"force":true}`),
		"zero epoch":       operationRequest("op", "cleanup", 0, ""),
		"empty id":         operationRequest("", "cleanup", 1, ""),
	}
	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
			if _, _, err := store.Submit(req); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want invalid request, got %v", err)
			}
			snapshot := store.Snapshot()
			if len(snapshot.Assets) != 0 || len(snapshot.Operations) != 0 {
				t.Fatalf("invalid request mutated state: %#v", snapshot)
			}
		})
	}
}

func TestPersistenceFailureKeepsMemoryAlignedWithRenameBoundary(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	req := operationRequest("op", "install", 1, `{"image":"debian@sha256:1"}`)

	store.persist = func(string, persisted) (bool, error) { return false, errors.New("write failed") }
	if _, _, err := store.Submit(req); err == nil {
		t.Fatal("want pre-rename persistence error")
	}
	if _, ok := store.Get(req.ID); ok {
		t.Fatal("pre-rename failure changed in-memory state")
	}

	store.persist = func(string, persisted) (bool, error) { return true, errors.New("directory sync uncertain") }
	if _, _, err := store.Submit(req); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("want post-rename unknown outcome, got %v", err)
	}
	if op, ok := store.Get(req.ID); !ok || op.Result != "installed" {
		t.Fatalf("post-rename state not retained for lookup: %#v ok=%v", op, ok)
	}
}

func TestSetFaultUsesTransactionalPersistence(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	store.persist = func(string, persisted) (bool, error) { return false, errors.New("write failed") }
	if err := store.SetFault("lost-response", true); err == nil {
		t.Fatal("want persistence error")
	}
	if store.Snapshot().Faults["lost-response"] {
		t.Fatal("failed pre-rename fault write changed memory")
	}

	store.persist = func(string, persisted) (bool, error) { return true, errors.New("sync uncertain") }
	if err := store.SetFault("lost-response", true); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("want post-rename unknown outcome, got %v", err)
	}
	if !store.Snapshot().Faults["lost-response"] {
		t.Fatal("post-rename fault state did not remain queryable")
	}
}

func TestOpenRejectsCorruptOrIncompatibleState(t *testing.T) {
	tests := map[string]string{
		"invalid json":         `{`,
		"missing version":      `{"assets":{},"operations":{},"faults":{}}`,
		"null asset":           `{"schemaVersion":1,"assets":{"a":null},"operations":{},"faults":{}}`,
		"incomplete authority": `{"schemaVersion":1,"assets":{"a":{"id":"a","epoch":1,"powerState":"Off"}},"operations":{},"faults":{}}`,
		"closed dirty asset":   `{"schemaVersion":1,"assets":{"a":{"id":"a","bindingID":"b","epoch":1,"fenceToken":"f","authorityClosed":true,"dirty":true,"powerState":"Off"}},"operations":{},"faults":{}}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("want corrupt state to fail closed")
			}
		})
	}
}

func TestOpenRejectsOperationWithoutRetainedAsset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := openStore(t, path)
	req := operationRequest("op", "install", 1, `{"image":"debian@sha256:1"}`)
	if _, _, err := store.Submit(req); err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	delete(state.Assets, req.AssetID)
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("operation without retained asset was accepted")
	}
}

func TestConcurrentIdempotentSubmissionsCreateOneOperation(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	const operations = 32
	req := operationRequest("op", "power", 1, `{"state":"On"}`)
	var wg sync.WaitGroup
	for attempt := 0; attempt < operations; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.Submit(req)
			if err != nil {
				t.Errorf("idempotent submit: %v", err)
			}
		}()
	}
	wg.Wait()
	state := store.Snapshot()
	if len(state.Operations) != 1 || state.Assets["asset-1"].Epoch != 1 {
		t.Fatalf("concurrent retries produced duplicate state: %#v", state)
	}
}

func openStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func operationRequest(id, kind string, epoch int64, inputs string) Request {
	return Request{
		ID: id, Kind: kind, AssetID: "asset-1", BindingID: "binding-a",
		Epoch: epoch, FenceToken: "token-a", Inputs: json.RawMessage(inputs),
	}
}
