package registration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// errSegmentIntegrity is returned for any missing, replaced, deleting, mutable
// or corrupt sealed segment. Callers deny; a segment is never treated as empty.
var errSegmentIntegrity = errors.New("segment lifetime or integrity unavailable")

// segmentKey names one sealed segment incarnation and its exact content. A
// root reference only matches a cached segment when all four fields agree.
type segmentKey struct {
	Namespace string
	Name      string
	UID       types.UID
	Digest    string
}

// SegmentCache keeps digest-verified, decoded sealed segments so an admission
// does not download and decode every segment on each ledger read. Segments are
// immutable Secrets: a given name and UID can never carry different data, and a
// replacement gets a new UID. Entries are keyed by name, UID and content digest
// as referenced by the root, so a root that points at a different incarnation
// or digest always misses and reloads with full verification.
//
// Cached ledgers are shared and must be treated as read-only.
type SegmentCache struct {
	mu       sync.Mutex
	segments map[segmentKey]Ledger
	prefixes map[string]Ledger
	order    []string
	// load serializes cold loads so concurrent admissions after a restart
	// decode each segment once instead of multiplying peak memory.
	load sync.Mutex
}

// NewSegmentCache returns an empty cache. Production wiring gives each handler
// its own; a Store without one uses a process-wide default.
func NewSegmentCache() *SegmentCache {
	return &SegmentCache{segments: map[segmentKey]Ledger{}, prefixes: map[string]Ledger{}}
}

var defaultSegmentCache = NewSegmentCache()

func (store Store) segmentCache() *SegmentCache {
	if store.Cache != nil {
		return store.Cache
	}
	return defaultSegmentCache
}

func (cache *SegmentCache) segment(key segmentKey) (Ledger, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delta, found := cache.segments[key]
	return delta, found
}

func (cache *SegmentCache) prefix(key string) (Ledger, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	base, found := cache.prefixes[key]
	return base, found
}

// remember stores a verified segment and its folded prefix, keeping only the
// segments the current root references once the cache grows past its bound.
func (cache *SegmentCache) remember(keys []segmentKey, deltas []Ledger, prefix string, base Ledger) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for index, key := range keys {
		cache.segments[key] = deltas[index]
	}
	if len(cache.segments) > 2*MaxSegments {
		for key := range cache.segments {
			if !slices.Contains(keys, key) {
				delete(cache.segments, key)
			}
		}
	}
	if _, exists := cache.prefixes[prefix]; !exists {
		cache.prefixes[prefix] = base
		cache.order = append(cache.order, prefix)
	}
	for len(cache.order) > 2 {
		delete(cache.prefixes, cache.order[0])
		cache.order = cache.order[1:]
	}
}

// sealed returns the overlay of every segment the root references, in order.
// Every call confirms each referenced segment incarnation still exists and is
// not deleting; content is only ever taken from a digest-verified load.
func (store Store) sealed(ctx context.Context, secret *corev1.Secret, route Routing) (Ledger, error) {
	cache := store.segmentCache()
	keys := make([]segmentKey, len(route.Segments))
	for index, reference := range route.Segments {
		if reference.Name != segmentName(secret.UID, uint64(index)) || reference.UID == "" || reference.Digest == "" {
			return Ledger{}, errors.New("invalid segment identity")
		}
		keys[index] = segmentKey{Namespace: store.Namespace, Name: reference.Name, UID: reference.UID, Digest: reference.Digest}
	}
	deltas := make([]Ledger, len(keys))
	for index, key := range keys {
		delta, cached := cache.segment(key)
		var err error
		if cached {
			err = store.segmentLive(ctx, key)
		} else {
			delta, err = store.loadSegment(ctx, cache, key)
		}
		if err != nil {
			return Ledger{}, err
		}
		if delta.Authority != store.Authority || !reflect.DeepEqual(delta.Projects, store.Projects) {
			return Ledger{}, errors.New("segment scope mismatch")
		}
		deltas[index] = delta
	}
	prefix := prefixKey(store, keys)
	base, found := cache.prefix(prefix)
	if !found {
		base = Ledger{Authority: store.Authority, Projects: store.Projects}
		for _, delta := range deltas {
			base = overlay(base, delta)
		}
		cache.remember(keys, deltas, prefix, base)
	}
	// Clipped so an append by a caller reallocates instead of writing into the
	// shared cached backing array. Callers must not modify elements in place.
	return Ledger{Authority: base.Authority, Projects: base.Projects, Entries: slices.Clip(base.Entries), DeviceFences: slices.Clip(base.DeviceFences)}, nil
}

func prefixKey(store Store, keys []segmentKey) string {
	encoded, _ := json.Marshal(struct {
		Authority string
		Projects  []string
		Keys      []segmentKey
	}{store.Authority, store.Projects, keys})
	return string(encoded)
}

// segmentLive confirms a cached segment incarnation is still present and not
// deleting. Immutable Secret data cannot change under an unchanged UID, so a
// metadata read is enough to keep missing or replaced segments fail-closed.
func (store Store) segmentLive(ctx context.Context, key segmentKey) error {
	metadata := &metav1.PartialObjectMetadata{}
	metadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	if err := store.Client.Get(ctx, client.ObjectKey{Namespace: key.Namespace, Name: key.Name}, metadata); err != nil {
		return err
	}
	if metadata.UID != key.UID || !metadata.DeletionTimestamp.IsZero() {
		return errSegmentIntegrity
	}
	return nil
}

// loadSegment reads a segment in full and verifies identity, immutability,
// lifetime and the referenced content digest before decoding or caching it.
func (store Store) loadSegment(ctx context.Context, cache *SegmentCache, key segmentKey) (Ledger, error) {
	cache.load.Lock()
	defer cache.load.Unlock()
	if delta, found := cache.segment(key); found {
		return delta, store.segmentLive(ctx, key)
	}
	segment := &corev1.Secret{}
	if err := store.Client.Get(ctx, client.ObjectKey{Namespace: key.Namespace, Name: key.Name}, segment); err != nil {
		return Ledger{}, err
	}
	data := segment.Data["ledger.json"]
	if segment.UID != key.UID || segment.Immutable == nil || !*segment.Immutable || !segment.DeletionTimestamp.IsZero() || digest(data) != key.Digest {
		return Ledger{}, errSegmentIntegrity
	}
	var delta Ledger
	if err := json.Unmarshal(data, &delta); err != nil {
		return Ledger{}, err
	}
	if delta.Authority != store.Authority || !reflect.DeepEqual(delta.Projects, store.Projects) {
		return Ledger{}, errors.New("segment scope mismatch")
	}
	cache.mu.Lock()
	cache.segments[key] = delta
	cache.mu.Unlock()
	return delta, nil
}

// fenceKey identifies one Device incarnation within a project. Quoting keeps
// it unambiguous without encoding a JSON document per fence on every read.
func fenceKey(fence DeviceFence) string {
	return strconv.Quote(fence.Project) + "," + strconv.Quote(string(fence.UID))
}
