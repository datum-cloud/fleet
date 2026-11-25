package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// sealedAssignment returns a payload and an assignment whose admitted inputs
// name it.
func (f *fixture) sealedAssignment(fence int64, uid string) (Assignment, Payload) {
	salt := make([]byte, 32)
	data := []byte(`{"userData":"#cloud-config\n"}`)
	hash := sha256.New()
	hash.Write(salt)
	hash.Write(data)
	payload := Payload{Ref: "sealed-" + uid, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Salt: salt, Data: data}
	assignment := f.assignment(fence, uid)
	assignment.Inputs = json.RawMessage(fmt.Sprintf(`{"image":"debian-12","bootstrap":{"ref":%q,"digest":%q}}`, payload.Ref, payload.Digest))
	sum := sha256.Sum256(assignment.Inputs)
	assignment.InputDigest = "sha256:" + hex.EncodeToString(sum[:])
	return assignment, payload
}

// TestABootstrapPayloadIsCheckedThenDeleted: the worker sees the verified
// payload, and the site deletes it once the result is stored.
func TestABootstrapPayloadIsCheckedThenDeleted(t *testing.T) {
	f := newFixture(t, testUUID)
	payloads := StorePayloads{Store: f.runner.Store}
	f.runner.Payloads = payloads
	assignment, payload := f.sealedAssignment(1, "op-sealed")
	if err := payloads.Put(assignment, payload); err != nil {
		t.Fatal(err)
	}
	var seen *Payload
	f.kind.check = func(work *Work) { seen = work.Bootstrap() }
	result, err := f.runner.Run(context.Background(), assignment)
	if err != nil || result.Phase != ops.PhaseSucceeded || seen == nil || seen.Digest != payload.Digest {
		t.Fatalf("result = %+v, %v; worker saw %v", result, err, seen)
	}
	if got, err := payloads.Get(context.Background(), assignment); err != nil || got != nil {
		t.Fatalf("the site kept the payload: %v, %v", got, err)
	}
}

// TestABadOrMissingPayloadSendsNothing refuses before any BMC write.
func TestABadOrMissingPayloadSendsNothing(t *testing.T) {
	for name, deliver := range map[string]func(*fixture, Assignment, Payload) *Runner{
		"not delivered": func(f *fixture, _ Assignment, _ Payload) *Runner {
			f.runner.Payloads = StorePayloads{Store: f.runner.Store}
			return f.runner
		},
		"altered after sealing": func(f *fixture, a Assignment, p Payload) *Runner {
			f.runner.Payloads = StorePayloads{Store: f.runner.Store}
			p.Data = []byte(`{"userData":"other"}`)
			_ = f.runner.Store.PutRecord(a.Device, a.Fence, payloadRecords, payloadKey(a), storedPayload{Fence: a.Fence, Payload: p})
			return f.runner
		},
		"a site that cannot receive bootstrap data": func(f *fixture, _ Assignment, _ Payload) *Runner {
			f.runner.Payloads = nil
			return f.runner
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, testUUID)
			assignment, payload := f.sealedAssignment(1, "op-bad")
			result, err := deliver(f, assignment, payload).Run(context.Background(), assignment)
			if err != nil || result.Phase != ops.PhaseFailed || f.kind.runs.Load() != 0 || f.bmc.count(notGet) != 0 {
				t.Fatalf("result = %+v, %v; runs %d, writes %d", result, err, f.kind.runs.Load(), f.bmc.count(notGet))
			}
		})
	}
}

// TestPayloadsAreStoredOnlyForTheirAssignmentAndPruned: the site stores a
// payload only for the assignment whose inputs name it, and a newer fence
// removes a superseded operation's payload.
func TestPayloadsAreStoredOnlyForTheirAssignmentAndPruned(t *testing.T) {
	f := newFixture(t, testUUID)
	payloads := StorePayloads{Store: f.runner.Store}
	first, payload := f.sealedAssignment(1, "op-first")
	other, _ := f.sealedAssignment(1, "op-other")
	if err := payloads.Put(other, payload); err == nil {
		t.Fatal("a payload was stored for an assignment that does not name it")
	}
	if err := payloads.Put(first, payload); err != nil {
		t.Fatal(err)
	}
	second, next := f.sealedAssignment(2, "op-second")
	if err := f.runner.Store.Raise(second.Device, 2); err != nil {
		t.Fatal(err)
	}
	if err := payloads.Put(second, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := payloads.Get(context.Background(), first); got != nil {
		t.Fatal("a superseded operation's payload remains")
	}
	if got, _ := payloads.Get(context.Background(), second); got == nil {
		t.Fatal("the current payload was pruned")
	}
}
