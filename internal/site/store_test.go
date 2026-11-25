package site

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestStoreRefusesOlderFencesAndSurvivesARestart(t *testing.T) {
	store := newStore(t)
	device := "default/server-example-001"
	if err := store.Raise(device, 5); err != nil {
		t.Fatal(err)
	}
	if err := store.Raise(device, 5); err != nil {
		t.Fatalf("a replayed delivery at the stored fence: %v", err)
	}
	if err := store.Raise(device, 4); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("an older fence: %v", err)
	}
	if _, err := store.Intend(device, 5, "op-a", "power-off"); err != nil {
		t.Fatal(err)
	}
	if err := store.Raise(device, 6); err != nil {
		t.Fatal(err)
	}
	// The older worker can neither journal nor finish a step.
	if _, err := store.Intend(device, 5, "op-a", "boot"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("a stale step: %v", err)
	}
	if err := store.Done(device, 5, "op-a", "power-off"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("a stale completion: %v", err)
	}
	before, err := os.ReadFile(store.deviceDir(device) + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	// A restarted site reads the same fence from disk.
	reopened, err := OpenStore(store.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Intend(device, 5, "op-a", "boot"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("a stale step after restart: %v", err)
	}
	after, _ := os.ReadFile(store.deviceDir(device) + "/state.json")
	if string(before) != string(after) {
		t.Fatal("a refused step changed the journal")
	}
	if fence, _, _ := reopened.Current(device); fence != 6 {
		t.Fatalf("fence after restart = %d", fence)
	}
}

func TestStoreReplaysAJournaledStepAsAnObservation(t *testing.T) {
	store := newStore(t)
	device := "default/server-example-002"
	if err := store.Raise(device, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Intend(device, 1, "op", "reset"); err != nil {
		t.Fatal(err)
	}
	// The worker crashed after journaling and before recording the step.
	if _, err := store.Intend(device, 1, "op", "reset"); !errors.Is(err, ErrStepReplayed) {
		t.Fatalf("a replayed step: %v", err)
	}
	if err := store.Done(device, 1, "op", "reset"); err != nil {
		t.Fatal(err)
	}
	if state, err := store.Intend(device, 1, "op", "reset"); err != nil || state != StepDone {
		t.Fatalf("a done step = %s %v", state, err)
	}
	if err := store.Done(device, 1, "op", "never-journaled"); err == nil {
		t.Fatal("a step was done without being journaled")
	}
}

// TestTwoWorkersOnOneDevice races a stream of steps from an older worker
// against a newer admission. Once the newer fence is stored, no older step is
// journaled.
func TestTwoWorkersOnOneDevice(t *testing.T) {
	store := newStore(t)
	device := "default/server-example-003"
	if err := store.Raise(device, 1); err != nil {
		t.Fatal(err)
	}
	var raised atomic.Bool
	var lateSteps atomic.Int32
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for index := range 200 {
			wasRaised := raised.Load()
			_, err := store.Intend(device, 1, "op-old", fmt.Sprintf("step-%d", index))
			if err == nil && wasRaised {
				lateSteps.Add(1)
			}
			if errors.Is(err, ErrStaleFence) {
				return
			}
		}
	}()
	go func() {
		defer group.Done()
		time.Sleep(2 * time.Millisecond)
		if err := store.Raise(device, 2); err != nil {
			t.Error(err)
		}
		raised.Store(true)
	}()
	group.Wait()
	if lateSteps.Load() != 0 {
		t.Fatalf("%d older steps journaled after the newer fence", lateSteps.Load())
	}
	journal, err := store.Journal(device)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range journal {
		if entry.Fence != 1 || entry.Operation != "op-old" {
			t.Fatalf("entry = %+v", entry)
		}
	}
}

func TestTwoDevicesInParallel(t *testing.T) {
	store := newStore(t)
	first, second := "default/server-example-004", "default/server-example-005"
	for _, device := range []string{first, second} {
		if err := store.Raise(device, 1); err != nil {
			t.Fatal(err)
		}
	}
	// A worker holding one Device's run lock does not block the other.
	held, err := store.tryRunLock(first)
	if err != nil || held == nil {
		t.Fatalf("first run lock: %v", err)
	}
	defer held.release()
	other, err := store.tryRunLock(second)
	if err != nil || other == nil {
		t.Fatalf("second Device blocked: %v", err)
	}
	other.release()
	if again, err := store.tryRunLock(first); err != nil || again != nil {
		t.Fatal("two workers held one Device's run lock")
	}
	var group sync.WaitGroup
	for _, device := range []string{first, second} {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range 50 {
				if _, err := store.Intend(device, 1, "op", fmt.Sprintf("step-%d", index)); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	group.Wait()
	for _, device := range []string{first, second} {
		if journal, _ := store.Journal(device); len(journal) != 50 {
			t.Fatalf("%s journaled %d steps", device, len(journal))
		}
	}
}

func TestStoreRootMustBeOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, nil); err == nil {
		t.Fatal("a shared store root was accepted")
	}
}
