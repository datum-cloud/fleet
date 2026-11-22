package registration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// uidClient assigns object UIDs on create as a real API server does; the fake
// client leaves them empty, which the expansion protocol rightly refuses.
type uidClient struct {
	client.Client
	next int
}

func (assigning *uidClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if object.GetUID() == "" {
		assigning.next++
		object.SetUID(types.UID(fmt.Sprintf("assigned-%d", assigning.next)))
	}
	return assigning.Client.Create(ctx, object, options...)
}

// expandedFixture seals the fixture ledger into one segment, retains an
// activation, and warms a fresh segment cache with a successful read.
func expandedFixture(t *testing.T) (*Handler, *countingClient, Routing) {
	t.Helper()
	ctx := context.Background()
	handler, _ := fixture(t)
	counting := &countingClient{Client: &uidClient{Client: handler.Store.Client}}
	handler.Store.Client = counting
	handler.Store.Cache = NewSegmentCache()
	private := controlFixture(t, handler)
	secret, ledger, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ledger.DeviceFences = append(ledger.DeviceFences, DeviceFence{Project: "provider", UID: "sealed", Aliases: []string{"uuid:sealed-reservation"}})
	if err := handler.Store.Write(ctx, secret, ledger); err != nil {
		t.Fatal(err)
	}
	if err := handler.ApplyControl(ctx, controlFor(t, handler, private, "expand")); err != nil {
		t.Fatal(err)
	}
	if err := handler.ApplyControl(ctx, controlFor(t, handler, private, "activate")); err != nil {
		t.Fatal(err)
	}
	_, route, err := handler.Store.root(ctx)
	if err != nil || len(route.Segments) != 1 {
		t.Fatalf("expected one sealed segment: %+v %v", route, err)
	}
	if _, _, err := handler.Store.Read(ctx); err != nil {
		t.Fatal(err)
	}
	return handler, counting, route
}

func setRouting(t *testing.T, handler *Handler, mutate func(*Routing)) {
	t.Helper()
	ctx := context.Background()
	secret, route, err := handler.Store.root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&route)
	secret.Data["routing.json"], _ = json.Marshal(route)
	if err := handler.Store.Client.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
}

// requireDenied proves a broken segment never reads as an empty partition:
// the ledger read fails, readiness fails, and an otherwise valid admission is
// denied without retaining anything.
func requireDenied(t *testing.T, handler *Handler, reason string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := handler.Store.Read(ctx); err == nil {
		t.Fatalf("%s: ledger read succeeded", reason)
	}
	if handler.Ready(ctx) == nil {
		t.Fatalf("%s: registration reported ready", reason)
	}
	_, device := fixture(t)
	if handler.Handle(ctx, registrationRequest(t, device, "while-"+strings.ReplaceAll(reason, " ", "-"))).Allowed {
		t.Fatalf("%s: admission allowed", reason)
	}
}

func TestSegmentCacheServesOnlyVerifiedReferencedIncarnations(t *testing.T) {
	ctx := context.Background()
	handler, counting, route := expandedFixture(t)
	reference := route.Segments[0]
	loads := counting.fullSegmentReads.Load()
	for range 3 {
		if _, ledger, err := handler.Store.Read(ctx); err != nil || len(ledger.DeviceFences) != 1 {
			t.Fatalf("warm read: %+v %v", ledger, err)
		}
	}
	if counting.fullSegmentReads.Load() != loads {
		t.Fatal("warm reads reloaded an unchanged sealed segment")
	}

	t.Run("root references a different digest", func(t *testing.T) {
		setRouting(t, handler, func(route *Routing) { route.Segments[0].Digest = digest([]byte("other content")) })
		requireDenied(t, handler, "digest mismatch")
		setRouting(t, handler, func(route *Routing) { route.Segments[0] = reference })
		if _, _, err := handler.Store.Read(ctx); err != nil {
			t.Fatal("restored reference unreadable", err)
		}
	})

	t.Run("root references a different UID", func(t *testing.T) {
		setRouting(t, handler, func(route *Routing) { route.Segments[0].UID = "other-incarnation" })
		requireDenied(t, handler, "uid mismatch")
		setRouting(t, handler, func(route *Routing) { route.Segments[0] = reference })
	})

	t.Run("cached segment replaced with free content", func(t *testing.T) {
		segment := &corev1.Secret{}
		if err := handler.Store.Client.Get(ctx, client.ObjectKey{Namespace: handler.Store.Namespace, Name: reference.Name}, segment); err != nil {
			t.Fatal(err)
		}
		if err := handler.Store.Client.Delete(ctx, segment); err != nil {
			t.Fatal(err)
		}
		requireDenied(t, handler, "missing segment")
		free, _ := json.Marshal(Ledger{Authority: "milo", Projects: []string{"provider"}})
		replacement := segment.DeepCopy()
		replacement.ResourceVersion, replacement.UID = "", types.UID("replacement-segment")
		replacement.Data["ledger.json"] = free
		if err := handler.Store.Client.Create(ctx, replacement); err != nil {
			t.Fatal(err)
		}
		// The old reference no longer matches the live incarnation.
		requireDenied(t, handler, "replaced segment")
		// Pointing the root at the replacement but keeping the retained digest
		// must not let the empty replacement stand in for sealed reservations.
		setRouting(t, handler, func(route *Routing) { route.Segments[0].UID = replacement.UID })
		requireDenied(t, handler, "replacement content")
	})
}

func TestSegmentCacheRejectsMutableSegment(t *testing.T) {
	ctx := context.Background()
	handler, _, route := expandedFixture(t)
	reference := route.Segments[0]
	segment := &corev1.Secret{}
	if err := handler.Store.Client.Get(ctx, client.ObjectKey{Namespace: handler.Store.Namespace, Name: reference.Name}, segment); err != nil {
		t.Fatal(err)
	}
	if err := handler.Store.Client.Delete(ctx, segment); err != nil {
		t.Fatal(err)
	}
	mutable := false
	replacement := segment.DeepCopy()
	replacement.ResourceVersion, replacement.UID, replacement.Immutable = "", types.UID("mutable-segment"), &mutable
	if err := handler.Store.Client.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	// Same bytes and digest, but a mutable Secret cannot be trusted to keep them.
	setRouting(t, handler, func(route *Routing) { route.Segments[0].UID = replacement.UID })
	requireDenied(t, handler, "mutable segment")
	if _, cached := handler.Store.Cache.segment(segmentKey{Namespace: handler.Store.Namespace, Name: reference.Name, UID: replacement.UID, Digest: reference.Digest}); cached {
		t.Fatal("an unverified segment was cached")
	}
}
