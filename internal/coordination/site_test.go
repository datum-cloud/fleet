package coordination

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

// reconcile composes the two site roles: the worker Job executes, then the
// observer publishes from the provider journal.
func reconcile(c *Cell, ctx context.Context, name string) error {
	if err := c.Execute(ctx, name); err != nil {
		return err
	}
	return c.Publish(ctx, name)
}

func TestWorkerExecuteNeverWritesStatus(t *testing.T) {
	i, s, now := fixture()
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), object("private", i))
	e := &effect{result: s.Reflected[0].Observation.Result}
	c := &Cell{API: api, Namespace: "private", Name: i.Cell, Executor: e, Now: func() time.Time { return now }}
	if err := c.Execute(context.Background(), Name(i.Request.ID)); err != nil || e.calls != 1 {
		t.Fatalf("execute: %v calls %d", err, e.calls)
	}
	for _, a := range api.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("worker used %s on %s", a.GetVerb(), a.GetSubresource())
		}
	}
}

func TestObserverPublishNeverExecutes(t *testing.T) {
	ctx := context.Background()
	i, s, now := fixture()
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), object("private", i))
	provider := &journaled{retained: map[string]simulator.Request{i.Request.ID: i.Request}, done: map[string]simulator.OperationResult{}}
	c := &Cell{API: api, Namespace: "private", Name: i.Cell, Executor: provider, Now: func() time.Time { return now }}
	if err := c.Publish(ctx, Name(i.Request.ID)); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("unfinished journal must stay unknown: %v", err)
	}
	empty := &journaled{retained: map[string]simulator.Request{}, done: map[string]simulator.OperationResult{}}
	c.Executor = empty
	if err := c.Publish(ctx, Name(i.Request.ID)); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("absent journal record must stay unknown: %v", err)
	}
	if provider.starts+len(provider.resumes)+empty.starts+len(empty.resumes) != 0 {
		t.Fatal("observer executed an effect")
	}
	obj, _ := api.Resource(Resource).Namespace("private").Get(ctx, Name(i.Request.ID), metav1.GetOptions{})
	if observed(t, obj) {
		t.Fatal("unknown outcome published")
	}
	// A terminal but mismatched journal result is not published either.
	wrong := s.Reflected[0].Observation.Result
	wrong.InputDigest = "0"
	c.Executor = &journaled{retained: map[string]simulator.Request{}, done: map[string]simulator.OperationResult{i.Request.ID: wrong}}
	if err := c.Publish(ctx, Name(i.Request.ID)); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("mismatched terminal result accepted: %v", err)
	}
	c.Executor = &journaled{retained: map[string]simulator.Request{}, done: map[string]simulator.OperationResult{i.Request.ID: s.Reflected[0].Observation.Result}}
	if err := c.Publish(ctx, Name(i.Request.ID)); err != nil {
		t.Fatal(err)
	}
	obj, _ = api.Resource(Resource).Namespace("private").Get(ctx, Name(i.Request.ID), metav1.GetOptions{})
	if !observed(t, obj) {
		t.Fatal("terminal journal result was not published")
	}
}

// A re-delivered copy under another name, a wrong-cell intent, or an invalid
// intent never reaches the executor.
func TestDeliveryFaultsFenceBeforeEffects(t *testing.T) {
	i, s, now := fixture()
	duplicate := object("private", i)
	duplicate.SetName("copy-of-" + Name(i.Request.ID))
	wrongCell := i
	wrongCell.Cell = "other-cell"
	wrongObj := object("private", wrongCell)
	wrongObj.SetLabels(map[string]string{CellLabel: i.Cell})
	invalid := object("private", i)
	invalid.SetName("op-invalid")
	_ = unstructured.SetNestedField(invalid.Object, "{", "spec", "requestJSON")
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), duplicate, invalid)
	_, _ = api.Resource(Resource).Namespace("private").Create(context.Background(), wrongObj, metav1.CreateOptions{})
	for name, want := range map[string]error{duplicate.GetName(): executor.ErrFenced, Name(i.Request.ID): executor.ErrFenced, "op-invalid": executor.ErrInvalid} {
		e := &effect{result: s.Reflected[0].Observation.Result}
		c := &Cell{API: api, Namespace: "private", Name: i.Cell, Executor: e, Now: func() time.Time { return now }}
		if err := c.Execute(context.Background(), name); !errors.Is(err, want) || e.calls != 0 {
			t.Fatalf("%s: %v calls %d", name, err, e.calls)
		}
		if err := c.Publish(context.Background(), name); !errors.Is(err, want) {
			t.Fatalf("%s publish: %v", name, err)
		}
	}
}

func TestObserveListsOnlyThisCell(t *testing.T) {
	i, s, now := fixture()
	other := i
	other.Cell = "other-cell"
	other.Request.ID = "other-op"
	api := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{Resource: Kind + "List"}, object("private", i), object("private", other))
	done := map[string]simulator.OperationResult{i.Request.ID: s.Reflected[0].Observation.Result}
	c := &Cell{API: api, Namespace: "private", Name: i.Cell, Executor: &journaled{retained: map[string]simulator.Request{}, done: done}, Now: func() time.Time { return now }}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var errs []error
	_ = c.Observe(ctx, time.Hour, func(_ string, err error) { errs = append(errs, err) })
	if len(errs) != 0 {
		t.Fatalf("unexpected observer errors: %v", errs)
	}
	mine, _ := api.Resource(Resource).Namespace("private").Get(context.Background(), Name(i.Request.ID), metav1.GetOptions{})
	theirs, _ := api.Resource(Resource).Namespace("private").Get(context.Background(), Name(other.Request.ID), metav1.GetOptions{})
	if !observed(t, mine) || observed(t, theirs) {
		t.Fatal("observer published outside its cell selector")
	}
}
