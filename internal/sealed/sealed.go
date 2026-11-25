// Package sealed is the sealed store for customer bootstrap data. The Fleet
// provider for Metal reads a customer's bootstrap Secret, validates it, and
// copies it into one SealedPayload per operation. The operation carries only
// the payload's name and digest. The coordinator checks the payload at
// admission and deletes it once nothing needs it; the federation courier
// delivers it to the operation's site.
//
// The digest covers a random salt and the data, so a staff member who can
// read the operation cannot confirm a guessed SSH key against it.
package sealed

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/catalog"
)

// SecretType is the type of a customer's bootstrap Secret.
const SecretType corev1.SecretType = "metal.datumapis.com/bootstrap"

// The bootstrap Secret's keys.
const (
	KeyUserData          = "userData"
	KeySSHAuthorizedKeys = "sshAuthorizedKeys"
)

// MaxBytes bounds the bootstrap data, and MaxEncodedBytes its encoded form,
// which JSON escaping can make larger. A site receives the encoded form in
// base64 inside a request of at most 256 KiB.
const (
	MaxBytes        = 64 << 10
	MaxEncodedBytes = 96 << 10
)

// Refusal reasons an Installation reports as Accepted=False.
const (
	ReasonBootstrapNotFound = "BootstrapNotFound"
	ReasonBootstrapInvalid  = "BootstrapInvalid"
)

// Payload is a customer's bootstrap data, exactly as stored.
type Payload struct {
	UserData          string `json:"userData,omitempty"`
	SSHAuthorizedKeys string `json:"sshAuthorizedKeys,omitempty"`
}

// Read reads and validates the named bootstrap Secret in the
// Installation's namespace. The reader must act for the Installation's
// creator: the caller decides who may read the Secret.
func Read(ctx context.Context, reader client.Reader, namespace, name string) (Payload, error) {
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
		return Payload{}, &catalog.Refusal{Reason: ReasonBootstrapNotFound, Message: fmt.Sprintf("no readable bootstrap Secret %q in this namespace", name)}
	} else if err != nil {
		return Payload{}, err
	}
	return Validate(secret)
}

// Validate checks a bootstrap Secret's type, keys and size.
func Validate(secret *corev1.Secret) (Payload, error) {
	invalid := func(message string) (Payload, error) {
		return Payload{}, &catalog.Refusal{Reason: ReasonBootstrapInvalid, Message: message}
	}
	if secret.Type != SecretType {
		return invalid("the bootstrap Secret's type must be " + string(SecretType))
	}
	size := 0
	for key, value := range secret.Data {
		if key != KeyUserData && key != KeySSHAuthorizedKeys {
			return invalid("the bootstrap Secret may hold only userData and sshAuthorizedKeys")
		}
		// Bootstrap data is text. Encoding would rewrite invalid UTF-8,
		// and the server would not get exactly what was stored.
		if !utf8.Valid(value) {
			return invalid("the bootstrap Secret's values must be UTF-8 text")
		}
		size += len(value)
	}
	payload := Payload{UserData: string(secret.Data[KeyUserData]), SSHAuthorizedKeys: string(secret.Data[KeySSHAuthorizedKeys])}
	if payload == (Payload{}) {
		return invalid("the bootstrap Secret holds no userData or sshAuthorizedKeys")
	}
	if size > MaxBytes {
		return invalid("the bootstrap Secret holds more than 64 KiB")
	}
	if encoded, err := json.Marshal(payload); err != nil || len(encoded) > MaxEncodedBytes {
		return invalid("the bootstrap Secret holds too many characters that need escaping")
	}
	return payload, nil
}

// Digest is the digest of a salt and the encoded data.
func Digest(salt, data []byte) string {
	hash := sha256.New()
	hash.Write(salt)
	hash.Write(data)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// Name is the only name a payload may have: its operation and its digest.
// A retry that reads changed data writes a new name, never new contents
// under an old one.
func Name(operation, digest string) string {
	sum := sha256.Sum256([]byte(operation + "/" + digest))
	return "sealed-" + hex.EncodeToString(sum[:])[:20]
}

// Seal encodes a payload with a new random salt for one operation.
func Seal(payload Payload, operation, allocation, namespace string) (*ops.SealedPayload, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxEncodedBytes {
		return nil, errors.New("sealed: the bootstrap data is too large to deliver")
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	digest := Digest(salt, data)
	object := &ops.SealedPayload{Spec: ops.SealedPayloadSpec{OperationName: operation, AllocationName: allocation, Digest: digest, Salt: salt, Data: data}}
	object.Name, object.Namespace = Name(operation, digest), namespace
	return object, nil
}

// ErrMismatch means a payload is not the one an operation names.
var ErrMismatch = errors.New("sealed: the payload does not match its operation")

// Verify checks that a payload is the one the operation's reference names:
// its name, its operation, and a digest recomputed from its contents.
func Verify(payload *ops.SealedPayload, operation string, reference ops.SealedInput) error {
	spec := payload.Spec
	if payload.Name != reference.Ref || payload.Name != Name(operation, reference.Digest) || spec.OperationName != operation || spec.Digest != reference.Digest || len(spec.Salt) != 32 {
		return ErrMismatch
	}
	if subtle.ConstantTimeCompare([]byte(Digest(spec.Salt, spec.Data)), []byte(reference.Digest)) != 1 {
		return ErrMismatch
	}
	return nil
}

// Decode returns the payload's data. It refuses bytes that do not decode
// to exactly a bootstrap payload.
func Decode(data []byte) (Payload, error) {
	var payload Payload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || payload == (Payload{}) {
		return Payload{}, ErrMismatch
	}
	return payload, nil
}

// Ended reports whether an operation phase is one whose payload is no
// longer needed: no path delivers an ended operation again.
func Ended(phase ops.Phase) bool {
	return slices.Contains([]ops.Phase{ops.PhaseSucceeded, ops.PhaseFailed, ops.PhaseCancelled, ops.PhaseOutcomeUnknown}, phase)
}

// Sweep deletes every payload in the namespace that nothing can need
// again: one whose operation has ended, one whose operation names another
// payload, and one with no operation whose allocation is no longer Active.
// An allocation that is not Active cannot admit an install, so the last
// rule never removes a payload admission still needs. Each delete carries
// the payload's UID as a precondition.
func Sweep(ctx context.Context, api client.Client, namespace string) (int, error) {
	var payloads ops.SealedPayloadList
	if err := api.List(ctx, &payloads, client.InNamespace(namespace)); err != nil {
		return 0, err
	}
	deleted := 0
	var failed error
	for index := range payloads.Items {
		payload := &payloads.Items[index]
		remove, err := unneeded(ctx, api, payload)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		if !remove {
			continue
		}
		uid := payload.UID
		if err := api.Delete(ctx, payload, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			failed = errors.Join(failed, err)
			continue
		}
		deleted++
	}
	return deleted, failed
}

func unneeded(ctx context.Context, api client.Client, payload *ops.SealedPayload) (bool, error) {
	operation := &ops.DeviceInstallation{}
	err := api.Get(ctx, client.ObjectKey{Namespace: payload.Namespace, Name: payload.Spec.OperationName}, operation)
	if err == nil {
		named := operation.Spec.Bootstrap != nil && operation.Spec.Bootstrap.Ref == payload.Name
		return !named || Ended(operation.Status.Phase), nil
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	allocation := &fleetv1.MachineAllocation{}
	err = api.Get(ctx, client.ObjectKey{Namespace: payload.Namespace, Name: payload.Spec.AllocationName}, allocation)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return allocation.Status.Phase != fleetv1.AllocationActive, nil
}
