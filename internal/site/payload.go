package site

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// Payload is an operation's sealed bootstrap data, delivered to the site
// beside its assignment and never inside it. Only that operation's worker
// reads it, and the site deletes it when the operation ends.
type Payload struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
	Salt   []byte `json:"salt"`
	Data   []byte `json:"data"`
}

// Payloads is where a site keeps delivered payloads. A Kubernetes site
// keeps each in its own Secret, which only that operation's worker may get.
type Payloads interface {
	// Get returns the assignment's payload, or nil if none was delivered.
	Get(ctx context.Context, assignment Assignment) (*Payload, error)
	// Delete removes the assignment's payload. It is idempotent.
	Delete(ctx context.Context, assignment Assignment) error
}

// Sealed returns the sealed reference in an assignment's admitted inputs,
// or nil. The reference is read from the inputs, whose digest the
// coordinator admitted, so a transport cannot substitute another payload.
func (a Assignment) Sealed() *ops.SealedInput {
	var inputs struct {
		Bootstrap *ops.SealedInput `json:"bootstrap"`
	}
	if json.Unmarshal(a.Inputs, &inputs) != nil {
		return nil
	}
	return inputs.Bootstrap
}

// Matches reports whether a payload is the one a reference names: the
// same name, and a digest recomputed from its salt and data.
func (p *Payload) Matches(reference *ops.SealedInput) bool {
	if p == nil || reference == nil || p.Ref != reference.Ref || p.Digest != reference.Digest || len(p.Salt) != 32 {
		return false
	}
	hash := sha256.New()
	hash.Write(p.Salt)
	hash.Write(p.Data)
	return subtle.ConstantTimeCompare([]byte("sha256:"+hex.EncodeToString(hash.Sum(nil))), []byte(reference.Digest)) == 1
}

// Bootstrap returns the operation's verified bootstrap data, or nil if it
// has none.
func (w *Work) Bootstrap() *Payload { return w.payload }

// ReasonBootstrapUnsupported is refused by a site that cannot receive or
// hand on bootstrap data.
const ReasonBootstrapUnsupported = custody.ReasonBootstrapUnsupported

// payload reads and verifies the assignment's payload before anything is
// sent. A missing or altered payload refuses the operation.
func (r *Runner) payload(ctx context.Context, assignment Assignment) (*Payload, *custody.Refusal) {
	reference := assignment.Sealed()
	if reference == nil {
		return nil, nil
	}
	if r.Payloads == nil {
		return nil, &custody.Refusal{Reason: ReasonBootstrapUnsupported, Message: "this site cannot receive bootstrap data"}
	}
	payload, err := r.Payloads.Get(ctx, assignment)
	if err != nil || payload == nil {
		return nil, &custody.Refusal{Reason: ReasonInputMismatch, Message: "the bootstrap payload was not delivered"}
	}
	if !payload.Matches(reference) {
		return nil, &custody.Refusal{Reason: ReasonInputMismatch, Message: "the bootstrap payload does not match the admitted digest"}
	}
	return payload, nil
}

// dropPayload deletes the assignment's payload once nothing can need it.
func (r *Runner) dropPayload(ctx context.Context, assignment Assignment) {
	if r.Payloads != nil && assignment.Sealed() != nil {
		_ = r.Payloads.Delete(context.WithoutCancel(ctx), assignment)
	}
}

const payloadRecords = "payload"

// StorePayloads keeps delivered payloads in the site store, one record per
// operation, for a site that receives them over its own API.
type StorePayloads struct{ Store *Store }

func payloadKey(assignment Assignment) string {
	sum := sha256.Sum256([]byte(assignment.Key()))
	return hex.EncodeToString(sum[:])[:32]
}

// storedPayload is a payload with the fence of the assignment it came
// with, so a newer fence can remove it.
type storedPayload struct {
	Fence   int64   `json:"fence"`
	Payload Payload `json:"payload"`
}

// Put stores a delivered payload. It refuses one that does not match the
// assignment's admitted reference, and one below the Device's stored fence.
// It deletes every payload of the Device below the stored fence first, so
// a superseded operation that never ran leaves none behind.
func (s StorePayloads) Put(assignment Assignment, payload Payload) error {
	if !payload.Matches(assignment.Sealed()) {
		return errors.New("site: the payload does not match the assignment")
	}
	if err := s.Prune(assignment.Device); err != nil {
		return err
	}
	return s.Store.PutRecord(assignment.Device, assignment.Fence, payloadRecords, payloadKey(assignment), storedPayload{Fence: assignment.Fence, Payload: payload})
}

func (s StorePayloads) Get(_ context.Context, assignment Assignment) (*Payload, error) {
	var stored storedPayload
	found, err := s.Store.GetRecord(assignment.Device, payloadRecords, payloadKey(assignment), &stored)
	if err != nil || !found || stored.Fence != assignment.Fence {
		return nil, err
	}
	return &stored.Payload, nil
}

// Prune deletes the Device's payloads below its stored fence.
func (s StorePayloads) Prune(device string) error {
	fence, _, err := s.Store.Peek(device)
	if err != nil {
		return err
	}
	records, err := s.Store.Records(payloadRecords)
	if err != nil {
		return err
	}
	for _, record := range records {
		var stored storedPayload
		if record.Device != device || json.Unmarshal(record.Data, &stored) == nil && stored.Fence >= fence {
			continue
		}
		if err := s.Store.DeleteRecord(device, payloadRecords, record.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s StorePayloads) Delete(_ context.Context, assignment Assignment) error {
	return s.Store.DeleteRecord(assignment.Device, payloadRecords, payloadKey(assignment))
}
