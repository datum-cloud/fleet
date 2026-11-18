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
	"k8s.io/client-go/dynamic/fake"
)

// journaled models a provider that retains each operation before effects:
// Lookup of an unfinished operation is Unknown, and Execute of a retained ID
// resumes it (digest-checked) rather than starting another effect.
type journaled struct {
	retained  map[string]simulator.Request
	done      map[string]simulator.OperationResult
	lookupErr error
	starts    int
	resumes   []simulator.Request
	finish    simulator.OperationResult
	complete  bool
}

func (j *journaled) Lookup(_ context.Context, id string) (simulator.OperationResult, bool, error) {
	if j.lookupErr != nil {
		return simulator.OperationResult{}, false, j.lookupErr
	}
	if r, ok := j.done[id]; ok {
		return r, true, nil
	}
	if _, ok := j.retained[id]; ok {
		return simulator.OperationResult{}, false, executor.ErrOutcomeUnknown
	}
	return simulator.OperationResult{}, false, nil
}
func (j *journaled) Execute(_ context.Context, r simulator.Request) (simulator.OperationResult, error) {
	if old, ok := j.retained[r.ID]; ok {
		a, _ := simulator.RequestDigest(old)
		b, _ := simulator.RequestDigest(r)
		if a != b {
			return simulator.OperationResult{}, executor.ErrConflict
		}
		j.resumes = append(j.resumes, r)
	} else {
		j.retained[r.ID] = r
		j.starts++
	}
	if !j.complete {
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	j.done[r.ID] = j.finish
	return j.finish, nil
}

func observed(t *testing.T, obj *unstructured.Unstructured) bool {
	t.Helper()
	_, found, err := unstructured.NestedMap(obj.Object, "status", "observation")
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestAdversarialCellRestartResumesUnfinishedOperationOnce(t *testing.T) {
	ctx := context.Background()
	i, s, now := fixture()
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), object("private", i))
	provider := &journaled{retained: map[string]simulator.Request{}, done: map[string]simulator.OperationResult{}, finish: s.Reflected[0].Observation.Result}
	name := Name(i.Request.ID)
	newCell := func() *Cell {
		return &Cell{API: api, Namespace: "private", Name: i.Cell, Executor: provider, Now: func() time.Time { return now }}
	}
	// First delivery starts the operation; its outcome is lost.
	if err := reconcile(newCell(), ctx, name); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("lost outcome must stay unknown: %v", err)
	}
	// A restarted cell sees Lookup Unknown and resumes; still unfinished.
	if err := reconcile(newCell(), ctx, name); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("unfinished operation must stay unknown: %v", err)
	}
	obj, err := api.Resource(Resource).Namespace("private").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if observed(t, obj) {
		t.Fatal("unknown outcome was published as an observation")
	}
	provider.complete = true
	if err := reconcile(newCell(), ctx, name); err != nil {
		t.Fatalf("restart did not recover the retained operation: %v", err)
	}
	if err := reconcile(newCell(), ctx, name); err != nil {
		t.Fatal(err)
	}
	if provider.starts != 1 || len(provider.resumes) != 2 {
		t.Fatalf("want one start and two exact resumes, got %d starts %d resumes", provider.starts, len(provider.resumes))
	}
	for _, r := range provider.resumes {
		if Digest(Intent{Cell: i.Cell, Request: r}) != Digest(i) {
			t.Fatal("resume did not re-deliver the exact retained request")
		}
	}
	obj, err = api.Resource(Resource).Namespace("private").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !observed(t, obj) {
		t.Fatal("terminal observation missing after recovery")
	}
}

func TestAdversarialCellLookupFailuresNeverExecute(t *testing.T) {
	for name, lookupErr := range map[string]error{
		"conflict":  executor.ErrConflict,
		"fenced":    executor.ErrFenced,
		"transport": errors.New("journal unreadable"),
	} {
		t.Run(name, func(t *testing.T) {
			i, s, now := fixture()
			api := fake.NewSimpleDynamicClient(runtime.NewScheme(), object("private", i))
			provider := &journaled{retained: map[string]simulator.Request{}, done: map[string]simulator.OperationResult{}, finish: s.Reflected[0].Observation.Result, complete: true, lookupErr: lookupErr}
			c := &Cell{API: api, Namespace: "private", Name: i.Cell, Executor: provider, Now: func() time.Time { return now }}
			if err := reconcile(c, context.Background(), Name(i.Request.ID)); !errors.Is(err, lookupErr) {
				t.Fatalf("lookup failure not reported: %v", err)
			}
			if provider.starts != 0 || len(provider.resumes) != 0 {
				t.Fatal("lookup failure led to an effect")
			}
		})
	}
}

// foundUnknown reports a retained but unverifiable terminal record. Resuming
// would only replay the same bytes, so the cell must report it, not execute.
type foundUnknown struct{ calls int }

func (f *foundUnknown) Lookup(context.Context, string) (simulator.OperationResult, bool, error) {
	return simulator.OperationResult{}, true, executor.ErrOutcomeUnknown
}
func (f *foundUnknown) Execute(context.Context, simulator.Request) (simulator.OperationResult, error) {
	f.calls++
	return simulator.OperationResult{}, nil
}

func TestAdversarialCellFoundUnknownIsNotSuccess(t *testing.T) {
	i, _, now := fixture()
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), object("private", i))
	e := &foundUnknown{}
	c := &Cell{API: api, Namespace: "private", Name: i.Cell, Executor: e, Now: func() time.Time { return now }}
	if err := reconcile(c, context.Background(), Name(i.Request.ID)); !errors.Is(err, executor.ErrOutcomeUnknown) || e.calls != 0 {
		t.Fatalf("found-unknown result: %v, executes %d", err, e.calls)
	}
}
