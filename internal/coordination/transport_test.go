package coordination

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func fixture() (Intent, Status, time.Time) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	i := Intent{Cell: "cell-a", Request: simulator.Request{ID: "op", Kind: "cleanup", AssetID: "device", BindingID: "allocation", Epoch: 2, FenceToken: "synthetic-test-fence"}}
	digest, _ := simulator.RequestDigest(i.Request)
	r := simulator.OperationResult{ID: "op", Kind: "cleanup", InputDigest: digest, Phase: "Succeeded", Result: "clean", CreatedAt: now.Add(-time.Second), CompletedAt: &now}
	return i, Status{Reflected: []Reflected{{ClusterName: i.Cell, Observation: Observation{Cell: i.Cell, IntentDigest: Digest(i), ObservedAt: now, Result: r}}}}, now
}
func TestReflectionRejectsUncertainAndConflictingResults(t *testing.T) {
	cases := map[string]func(*Intent, *Status, time.Time){
		"wrong federation cell": func(_ *Intent, s *Status, _ time.Time) { s.Reflected[0].ClusterName = "cell-b" },
		"wrong reported cell":   func(_ *Intent, s *Status, _ time.Time) { s.Reflected[0].Observation.Cell = "cell-b" },
		"stale":                 func(_ *Intent, s *Status, n time.Time) { s.Reflected[0].Observation.ObservedAt = n.Add(-time.Hour) },
		"future":                func(_ *Intent, s *Status, n time.Time) { s.Reflected[0].Observation.ObservedAt = n.Add(time.Second) },
		"wrong epoch":           func(i *Intent, _ *Status, _ time.Time) { i.Request.Epoch++ },
		"wrong allocation":      func(i *Intent, _ *Status, _ time.Time) { i.Request.BindingID = "other" },
		"wrong fence":           func(i *Intent, _ *Status, _ time.Time) { i.Request.FenceToken = "other" },
		"wrong inputs":          func(_ *Intent, s *Status, _ time.Time) { s.Reflected[0].Observation.Result.InputDigest = "other" },
		"wrong operation":       func(_ *Intent, s *Status, _ time.Time) { s.Reflected[0].Observation.Result.ID = "other" },
		"unknown":               func(_ *Intent, s *Status, _ time.Time) { s.Reflected[0].Observation.Result.Phase = "Unknown" },
		"unclean":               func(_ *Intent, s *Status, _ time.Time) { s.Reflected[0].Observation.Result.Result = "unknown" },
		"duplicate":             func(_ *Intent, s *Status, _ time.Time) { s.Reflected = append(s.Reflected, s.Reflected[0]) },
		"missing":               func(_ *Intent, s *Status, _ time.Time) { s.Reflected = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			i, s, n := fixture()
			change(&i, &s, n)
			if _, err := ValidateReflection(i, s, n, time.Minute); !errors.Is(err, executor.ErrOutcomeUnknown) {
				t.Fatalf("accepted invalid reflection: %v", err)
			}
		})
	}
	i, s, n := fixture()
	if _, err := ValidateReflection(i, s, n, time.Minute); err != nil {
		t.Fatal(err)
	}
}

type effect struct {
	result simulator.OperationResult
	calls  int
	fail   bool
}

func (e *effect) Lookup(context.Context, string) (simulator.OperationResult, bool, error) {
	return e.result, e.calls > 0, nil
}
func (e *effect) Execute(_ context.Context, r simulator.Request) (simulator.OperationResult, error) {
	e.calls++
	if e.fail {
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	return e.result, nil
}
func TestHubCellRestartAndNoFakeFederation(t *testing.T) {
	ctx := context.Background()
	i, s, now := fixture()
	hubAPI := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{Resource: Kind + "List"})
	cellAPI := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{Resource: Kind + "List"})
	h := &Hub{API: hubAPI, Namespace: "private", Cell: i.Cell, MaxAge: time.Minute, Now: func() time.Time { return now }}
	if _, err := h.Execute(ctx, i.Request); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	obj, err := hubAPI.Resource(Resource).Namespace("private").Get(ctx, Name(i.Request.ID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cellAPI.Resource(Resource).Namespace("private").Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	e := &effect{result: s.Reflected[0].Observation.Result}
	for range 2 {
		c := &Cell{API: cellAPI, Namespace: "private", Name: i.Cell, Executor: e, Now: func() time.Time { return now }}
		if err = reconcile(c, ctx, obj.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	if e.calls != 1 {
		t.Fatalf("duplicate effects: %d", e.calls)
	}
	// This unit test deliberately does not simulate federation or claim its success.
	if _, _, err = h.Lookup(ctx, i.Request.ID); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("cell-local success leaked to hub: %v", err)
	}
	changed := i.Request
	changed.Epoch++
	if _, err = h.Execute(ctx, changed); !errors.Is(err, executor.ErrConflict) {
		t.Fatalf("accepted changed authority: %v", err)
	}
}
func TestCellRejectsWrongCellBeforeEffects(t *testing.T) {
	i, s, n := fixture()
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), object("private", i))
	e := &effect{result: s.Reflected[0].Observation.Result}
	c := Cell{API: api, Namespace: "private", Name: "wrong", Executor: e, Now: func() time.Time { return n }}
	if err := reconcile(&c, context.Background(), Name(i.Request.ID)); !errors.Is(err, executor.ErrFenced) || e.calls != 0 {
		t.Fatalf("wrong-cell effect: %v, calls %d", err, e.calls)
	}
}
