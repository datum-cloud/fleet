package registration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	TailBudget    = 650000
	SegmentBudget = 750000
	MaxSegments   = 16
	RootBudget    = 850000
	SegmentPrefix = "fleet-registration-segment-"
)

var ErrGrowthStopped = errors.New("registration growth stopped; signed lossless expansion required, reservations remain retained")

// ErrSegmentsExhausted is the terminal growth stop: every sealed segment is in
// use, so expansion itself is refused. It still matches ErrGrowthStopped for
// callers that only distinguish stopped growth.
var ErrSegmentsExhausted error = segmentsExhausted{}

type segmentsExhausted struct{}

func (segmentsExhausted) Error() string {
	return fmt.Sprintf("registration segments exhausted (%d of %d sealed); expansion is refused and a reviewed storage migration is required, see docs/development/registration-storage-rollout.md; reservations remain retained, do not raise caps or delete records", MaxSegments, MaxSegments)
}

func (segmentsExhausted) Is(target error) bool { return target == ErrGrowthStopped }

type Segment struct {
	Name   string    `json:"name"`
	UID    types.UID `json:"uid"`
	Digest string    `json:"digest"`
}

type Routing struct {
	Version           int             `json:"version"`
	Epoch             uint64          `json:"epoch"`
	RootUID           types.UID       `json:"rootUID"`
	Segments          []Segment       `json:"segments"`
	Pending           *SignedControl  `json:"pending,omitempty"`
	Activation        *SignedControl  `json:"activation,omitempty"`
	ActivationHistory []SignedControl `json:"activationHistory,omitempty"`
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func decodeRouting(secret *corev1.Secret) (Routing, error) {
	var route Routing
	if err := json.Unmarshal(secret.Data["routing.json"], &route); err != nil {
		return route, err
	}
	if route.Version != 2 || route.RootUID != secret.UID || route.Epoch != uint64(len(route.Segments)) || len(route.Segments) > MaxSegments || len(route.ActivationHistory) > 7 {
		return route, errors.New("invalid registration routing identity or epoch")
	}
	return route, nil
}

func (store Store) readSnapshot(ctx context.Context, secret *corev1.Secret) (Ledger, Ledger, error) {
	base := Ledger{Authority: store.Authority, Projects: store.Projects}
	key := "ledger.json"
	if _, exists := secret.Data["routing.json"]; exists {
		route, err := decodeRouting(secret)
		if err != nil {
			return Ledger{}, Ledger{}, err
		}
		key = "tail.json"
		for index, reference := range route.Segments {
			if reference.Name != segmentName(secret.UID, uint64(index)) || reference.UID == "" {
				return Ledger{}, Ledger{}, errors.New("invalid segment identity")
			}
			segment := &corev1.Secret{}
			if err := store.Client.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: reference.Name}, segment); err != nil {
				return Ledger{}, Ledger{}, err
			}
			if segment.UID != reference.UID || segment.Immutable == nil || !*segment.Immutable || !segment.DeletionTimestamp.IsZero() || digest(segment.Data["ledger.json"]) != reference.Digest {
				return Ledger{}, Ledger{}, errors.New("segment lifetime or integrity unavailable")
			}
			var delta Ledger
			if err := json.Unmarshal(segment.Data["ledger.json"], &delta); err != nil {
				return Ledger{}, Ledger{}, err
			}
			if delta.Authority != store.Authority || !reflect.DeepEqual(delta.Projects, store.Projects) {
				return Ledger{}, Ledger{}, errors.New("segment scope mismatch")
			}
			base = overlay(base, delta)
		}
	}
	var tail Ledger
	if err := json.Unmarshal(secret.Data[key], &tail); err != nil {
		return Ledger{}, Ledger{}, err
	}
	if tail.Authority != store.Authority || !reflect.DeepEqual(tail.Projects, store.Projects) {
		return Ledger{}, Ledger{}, errors.New("ledger scope mismatch")
	}
	return overlay(base, tail), base, nil
}

func fenceKey(fence DeviceFence) string {
	encoded, _ := json.Marshal([]string{fence.Project, string(fence.UID)})
	return string(encoded)
}

func overlay(base, tail Ledger) Ledger {
	result := Ledger{Authority: base.Authority, Projects: base.Projects}
	entries := map[types.UID]bool{}
	fences := map[string]bool{}
	for _, entry := range tail.Entries {
		entries[entry.Operation] = true
	}
	for _, fence := range tail.DeviceFences {
		fences[fenceKey(fence)] = true
	}
	for _, entry := range base.Entries {
		if !entries[entry.Operation] {
			result.Entries = append(result.Entries, entry)
		}
	}
	for _, fence := range base.DeviceFences {
		if !fences[fenceKey(fence)] {
			result.DeviceFences = append(result.DeviceFences, fence)
		}
	}
	result.Entries = append(result.Entries, tail.Entries...)
	result.DeviceFences = append(result.DeviceFences, tail.DeviceFences...)
	return result
}

func deltaFrom(base, current Ledger) Ledger {
	delta := Ledger{Authority: current.Authority, Projects: current.Projects}
	baseEntries := map[types.UID][]Entry{}
	currentEntries := map[types.UID][]Entry{}
	baseFences := map[string][]DeviceFence{}
	currentFences := map[string][]DeviceFence{}
	for _, entry := range base.Entries {
		baseEntries[entry.Operation] = append(baseEntries[entry.Operation], entry)
	}
	for _, entry := range current.Entries {
		currentEntries[entry.Operation] = append(currentEntries[entry.Operation], entry)
	}
	for _, fence := range base.DeviceFences {
		baseFences[fenceKey(fence)] = append(baseFences[fenceKey(fence)], fence)
	}
	for _, fence := range current.DeviceFences {
		currentFences[fenceKey(fence)] = append(currentFences[fenceKey(fence)], fence)
	}
	for _, entry := range current.Entries {
		if !reflect.DeepEqual(baseEntries[entry.Operation], currentEntries[entry.Operation]) {
			delta.Entries = append(delta.Entries, entry)
		}
	}
	for _, fence := range current.DeviceFences {
		if !reflect.DeepEqual(baseFences[fenceKey(fence)], currentFences[fenceKey(fence)]) {
			delta.DeviceFences = append(delta.DeviceFences, fence)
		}
	}
	return delta
}

func preservesReservations(before, after Ledger) bool {
	if before.Authority != after.Authority || !reflect.DeepEqual(before.Projects, after.Projects) {
		return false
	}
	entries := map[types.UID][]Entry{}
	fences := map[string][]DeviceFence{}
	for _, entry := range after.Entries {
		entries[entry.Operation] = append(entries[entry.Operation], entry)
	}
	for _, fence := range after.DeviceFences {
		fences[fenceKey(fence)] = append(fences[fenceKey(fence)], fence)
	}
	for _, previous := range before.Entries {
		found := false
		for _, next := range entries[previous.Operation] {
			next.Outcome = previous.Outcome
			if reflect.DeepEqual(previous, next) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, previous := range before.DeviceFences {
		found := false
		for _, next := range fences[fenceKey(previous)] {
			if fenceKey(previous) != fenceKey(next) || (previous.Deleted && !next.Deleted) {
				continue
			}
			if previous.ProjectUID != "" && previous.ProjectUID != next.ProjectUID {
				continue
			}
			if previous.NamespaceUID != "" && previous.NamespaceUID != next.NamespaceUID {
				continue
			}
			if previous.Name != "" && (previous.Name != next.Name || previous.Namespace != next.Namespace) {
				continue
			}
			if slices.ContainsFunc(previous.Aliases, func(alias string) bool { return !slices.Contains(next.Aliases, alias) }) {
				continue
			}
			found = true
		}
		if !found {
			return false
		}
	}
	return true
}

func (store Store) writeSnapshot(ctx context.Context, secret *corev1.Secret, current Ledger) error {
	route, err := decodeRouting(secret)
	if err != nil {
		return err
	}
	if route.Pending != nil {
		return errors.New("registration expansion is fenced")
	}
	previous, base, err := store.readSnapshot(ctx, secret)
	if err != nil {
		return err
	}
	if !preservesReservations(previous, current) {
		return errors.New("registration write would discard retained authority")
	}
	encoded, err := json.Marshal(deltaFrom(base, current))
	if err != nil {
		return err
	}
	if len(encoded) > TailBudget {
		if len(route.Segments) >= MaxSegments {
			return ErrSegmentsExhausted
		}
		return ErrGrowthStopped
	}
	updated := secret.DeepCopy()
	updated.Data["tail.json"] = encoded
	return store.updateRoot(ctx, updated)
}

func (store Store) updateRoot(ctx context.Context, secret *corev1.Secret) error {
	size := 0
	for key, value := range secret.Data {
		size += len(key) + len(value)
	}
	if size > RootBudget {
		return ErrGrowthStopped
	}
	return store.Client.Update(ctx, secret)
}

func segmentName(uid types.UID, epoch uint64) string {
	return fmt.Sprintf("%s%s-%02d", SegmentPrefix, digest([]byte(uid))[:24], epoch)
}
