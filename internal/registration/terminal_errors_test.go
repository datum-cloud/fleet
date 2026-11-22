package registration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const runbook = "docs/development/registration-storage-rollout.md"

// sealedRoot converts the fixture root to v2 routing with the given number of
// sealed segments, each a valid immutable overlay of the configured scope.
func sealedRoot(t *testing.T, handler *Handler, sealed int) {
	t.Helper()
	ctx := context.Background()
	store := handler.Store
	secret := &corev1.Secret{}
	if err := store.Client.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: LedgerName}, secret); err != nil {
		t.Fatal(err)
	}
	empty, _ := json.Marshal(Ledger{Authority: store.Authority, Projects: store.Projects})
	route := Routing{Version: 2, RootUID: secret.UID, Epoch: uint64(sealed)}
	immutable := true
	for epoch := 0; epoch < sealed; epoch++ {
		segment := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: store.Namespace, Name: segmentName(secret.UID, uint64(epoch)), UID: types.UID(fmt.Sprint("segment-", epoch))}, Immutable: &immutable, Data: map[string][]byte{"ledger.json": empty}}
		if err := store.Client.Create(ctx, segment); err != nil {
			t.Fatal(err)
		}
		if segment.UID == "" {
			t.Fatal("segment UID not retained")
		}
		route.Segments = append(route.Segments, Segment{Name: segment.Name, UID: segment.UID, Digest: digest(empty)})
	}
	encoded, _ := json.Marshal(route)
	secret.Data = map[string][]byte{"ledger.json": []byte(`{"authority":"v2-routing-required","projects":[]}`), "tail.json": empty, "routing.json": encoded}
	if err := store.Client.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
}

func oversizedWrite(t *testing.T, handler *Handler) error {
	t.Helper()
	secret, ledger, err := handler.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ledger.DeviceFences = append(ledger.DeviceFences, DeviceFence{Project: "provider", UID: "oversize", Aliases: []string{strings.Repeat("x", TailBudget)}})
	return handler.Store.Write(context.Background(), secret, ledger)
}

func TestSegmentExhaustionIsDistinctAndActionable(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	key := controlFixture(t, handler)
	sealedRoot(t, handler, MaxSegments)
	for name, err := range map[string]error{
		"expand": handler.ApplyControl(ctx, controlFor(t, handler, key, "expand")),
		"write":  oversizedWrite(t, handler),
	} {
		if !errors.Is(err, ErrSegmentsExhausted) || !errors.Is(err, ErrGrowthStopped) {
			t.Fatalf("%s: terminal growth stop not distinct: %v", name, err)
		}
		message := err.Error()
		if strings.Contains(message, "expansion required") || !strings.Contains(message, "segments exhausted") || !strings.Contains(message, "reviewed storage migration") || !strings.Contains(message, runbook) {
			t.Fatalf("%s: terminal error is not operator-actionable: %s", name, message)
		}
	}
	_, route, err := handler.Store.root(ctx)
	if err != nil || route.Pending != nil || len(route.Segments) != MaxSegments {
		t.Fatalf("refused expansion changed routing: %+v %v", route, err)
	}
}

func TestGrowthStopBelowSegmentCapStillAsksForExpansion(t *testing.T) {
	handler, _ := fixture(t)
	controlFixture(t, handler)
	sealedRoot(t, handler, MaxSegments-1)
	err := oversizedWrite(t, handler)
	if !errors.Is(err, ErrGrowthStopped) || errors.Is(err, ErrSegmentsExhausted) {
		t.Fatalf("expandable root reported terminal exhaustion: %v", err)
	}
}

func TestFullLegacyLedgerNamesExpandRequest(t *testing.T) {
	handler, _ := fixture(t)
	err := oversizedWrite(t, handler)
	if err == nil || !strings.Contains(err.Error(), "sign an expand") || !strings.Contains(err.Error(), runbook) {
		t.Fatalf("full v1 ledger does not say how to proceed: %v", err)
	}
}

func TestActivationGateIsDefaultAndProductionForcesIt(t *testing.T) {
	ctx := context.Background()
	handler, device := fixture(t)
	handler.Store.AllowInactiveForTests = false
	if handler.Handle(ctx, registrationRequest(t, device, "zero-value")).Allowed {
		t.Fatal("zero-value store admitted without signed activation")
	}
	if _, ledger, err := handler.Store.Read(ctx); err != nil || len(ledger.Entries) != 0 {
		t.Fatalf("gated admission retained a reservation: %+v %v", ledger, err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	production := NewHandler(Store{Client: handler.Store.Client, Namespace: "private", Authority: "milo", Projects: []string{"provider"}, AllowInactiveForTests: true}, fake.NewClientBuilder().WithScheme(scheme).Build(), &rest.Config{Host: "https://synthetic.invalid"}, scheme)
	if production.Store.AllowInactiveForTests {
		t.Fatal("production wiring kept the test-only activation opt-out")
	}
}
