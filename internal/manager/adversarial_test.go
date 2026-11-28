package manager

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

func TestInitialDependencyAddRetriesFirstListFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid"}}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "claim-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "class"}}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, claim).
		WithIndex(&fleetv1.Machine{}, IndexClaimClassName, func(object client.Object) []string {
			return []string{object.(*fleetv1.Machine).Spec.MachineClassRef.Name}
		}).Build()
	reader := &failFirstListReader{Reader: base, remaining: 1}
	project := testProject("milo", "project")
	mappers := Mappers{Reader: reader, Project: project}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap(mappers.Route(RouteClass)).Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: class, IsInInitialList: true}, queue)
	route, shutdown := queue.Get()
	if shutdown {
		t.Fatal("initial Add did not enqueue route work")
	}
	queue.Done(route)
	queue.ShutDown()
	called := 0
	verifier := ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return nil })
	reconciler := RouteReconciler{Project: project, Verifier: verifier, Mappers: mappers, Handler: HandlerFunc(func(context.Context, ProjectRequest) Outcome { called++; return Outcome{} })}
	if _, err := reconciler.Reconcile(context.Background(), route); err == nil || called != 0 {
		t.Fatalf("first route err=%v called=%d", err, called)
	}
	if _, err := reconciler.Reconcile(context.Background(), route); err != nil || called != 1 {
		t.Fatalf("recovered route err=%v called=%d", err, called)
	}
}

func TestExternalReplayCommitsBatchCursorAfterEveryPersist(t *testing.T) {
	events := []ExternalEvent{
		{Source: "iam", Sequence: "event-a", RecordID: "decision-a", AuthorityID: "milo", ProjectUID: "project"},
		{Source: "iam", Sequence: "event-b", RecordID: "decision-b", AuthorityID: "milo", ProjectUID: "project"},
	}
	source := &fakeReplaySource{events: events, nextCursor: "checkpoint-91", failAfter: 1}
	cursors := &fakeCursors{}
	inbox := &fakeInbox{}
	runner := ExternalRunner{Source: source, Cursors: cursors, Inbox: inbox}
	if err := runner.ReplayOnce(context.Background(), "iam"); err == nil {
		t.Fatal("crash after first persist did not fail")
	}
	if cursors.cursor != "" || len(inbox.persisted) != 1 {
		t.Fatalf("cursor=%q persisted=%d", cursors.cursor, len(inbox.persisted))
	}
	source.failAfter = 2
	if err := runner.ReplayOnce(context.Background(), "iam"); err == nil {
		t.Fatal("crash after second persist did not fail")
	}
	if cursors.cursor != "" || len(inbox.persisted) != 2 {
		t.Fatalf("cursor=%q persisted=%d", cursors.cursor, len(inbox.persisted))
	}
	source.failAfter = 0
	if err := runner.ReplayOnce(context.Background(), "iam"); err != nil {
		t.Fatal(err)
	}
	if cursors.cursor != source.nextCursor || cursors.cursor == events[1].Sequence || len(inbox.persisted) != 2 || inbox.calls != 5 {
		t.Fatalf("cursor=%q persisted=%d calls=%d", cursors.cursor, len(inbox.persisted), inbox.calls)
	}
}

func TestExternalReplayCommitsEmptyBatchCursor(t *testing.T) {
	runner := ExternalRunner{Source: &fakeReplaySource{nextCursor: "checkpoint-empty"}, Cursors: &fakeCursors{}, Inbox: &fakeInbox{}}
	if err := runner.ReplayOnce(context.Background(), "iam"); err != nil {
		t.Fatal(err)
	}
	if runner.Cursors.(*fakeCursors).cursor != "checkpoint-empty" {
		t.Fatalf("cursor=%q", runner.Cursors.(*fakeCursors).cursor)
	}
}

func TestExternalWatchUsesExplicitPostEventCursor(t *testing.T) {
	event := ExternalEvent{Source: "iam", Sequence: "event-7", RecordID: "decision", AuthorityID: "milo", ProjectUID: "project"}
	cursors := &fakeCursors{}
	runner := ExternalRunner{Source: &fakeReplaySource{watch: []WatchedEvent{{Event: event, NextCursor: "checkpoint-44"}}}, Cursors: cursors, Inbox: &fakeInbox{}}
	if err := runner.Watch(context.Background(), "iam"); err != nil {
		t.Fatal(err)
	}
	if cursors.cursor != "checkpoint-44" || cursors.cursor == event.Sequence {
		t.Fatalf("cursor=%q sequence=%q", cursors.cursor, event.Sequence)
	}
}

func TestDurableInboxDrainRetriesUntilDurablyProcessedAndAcked(t *testing.T) {
	event := ExternalEvent{Source: "iam", Sequence: "event", RecordID: "decision", AuthorityID: "milo", ProjectUID: "project"}
	inbox := &fakeInbox{}
	runner := ExternalRunner{Inbox: inbox}
	if err := runner.persist(context.Background(), "iam", event); err != nil {
		t.Fatal(err)
	}
	fail := true
	consumeCalls := 0
	consume := func(context.Context, ExternalEvent) error {
		consumeCalls++
		if fail {
			return errors.New("durable handler failed")
		}
		return nil
	}
	if err := runner.Drain(context.Background(), "iam", consume); err == nil || len(inbox.acked) != 0 {
		t.Fatalf("first drain err=%v acked=%d", err, len(inbox.acked))
	}
	fail = false
	if err := runner.Drain(context.Background(), "iam", consume); err != nil {
		t.Fatal(err)
	}
	if consumeCalls != 2 || len(inbox.acked) != 1 {
		t.Fatalf("consumeCalls=%d acked=%d", consumeCalls, len(inbox.acked))
	}
	if err := runner.Drain(context.Background(), "iam", consume); err != nil || consumeCalls != 2 {
		t.Fatalf("acked work replayed: err=%v calls=%d", err, consumeCalls)
	}
}

func TestDeadlineCancelWaitsForInFlightGeneration(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Now())
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "uid"}
	emitStarted, releaseEmit := make(chan struct{}), make(chan struct{})
	scheduler := NewDeadlineScheduler(clock, func(ProjectRequest) { close(emitStarted); <-releaseEmit })
	scheduler.Schedule(request, clock.Now().Add(time.Second))
	stepDone := make(chan struct{})
	go func() { clock.Step(time.Second); close(stepDone) }()
	<-emitStarted
	cancelDone := make(chan struct{})
	go func() { scheduler.Cancel(request); close(cancelDone) }()
	select {
	case <-cancelDone:
		t.Fatal("Cancel returned while the old generation could still emit")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseEmit)
	<-stepDone
	<-cancelDone
}

func TestImmediateDeadlineEmissionSerializesCancel(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Now())
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "uid"}
	emitStarted, releaseEmit := make(chan struct{}), make(chan struct{})
	scheduler := NewDeadlineScheduler(clock, func(ProjectRequest) { close(emitStarted); <-releaseEmit })
	scheduleDone := make(chan struct{})
	go func() { scheduler.Schedule(request, clock.Now()); close(scheduleDone) }()
	<-emitStarted
	cancelDone := make(chan struct{})
	go func() { scheduler.Cancel(request); close(cancelDone) }()
	select {
	case <-cancelDone:
		t.Fatal("Cancel returned while immediate generation could still emit")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseEmit)
	<-scheduleDone
	<-cancelDone
}

func TestDeadlineStopDrainsAndPreventsPostStopEmission(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Now())
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "uid"}
	emitted := make(chan ProjectRequest, 1)
	scheduler := NewDeadlineScheduler(fakeClock, func(value ProjectRequest) { emitted <- value })
	if err := scheduler.Schedule(request, fakeClock.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	scheduler.Stop()
	if len(scheduler.pending) != 0 {
		t.Fatalf("stopped scheduler retained %d deadlines", len(scheduler.pending))
	}
	fakeClock.Step(time.Hour)
	select {
	case value := <-emitted:
		t.Fatalf("post-stop emission: %#v", value)
	default:
	}
	if err := scheduler.Schedule(request, fakeClock.Now().Add(time.Second)); !errors.Is(err, ErrEventWiringUnavailable) {
		t.Fatalf("post-stop Schedule error = %v", err)
	}
	if err := scheduler.Cancel(request); !errors.Is(err, ErrEventWiringUnavailable) {
		t.Fatalf("post-stop Cancel error = %v", err)
	}
}

func TestDeadlineManagerContextStopsAndDrainsScheduler(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Now())
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "uid"}
	scheduler := NewDeadlineScheduler(fakeClock, func(ProjectRequest) { t.Error("stopped manager emitted deadline") })
	if err := scheduler.Schedule(request, fakeClock.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Start(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !scheduler.stopped || len(scheduler.pending) != 0 {
		t.Fatalf("scheduler stopped=%t pending=%d", scheduler.stopped, len(scheduler.pending))
	}
	fakeClock.Step(time.Hour)
}

func TestDeadlineStopWaitsForInFlightEmission(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Now())
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "uid"}
	emitStarted, releaseEmit := make(chan struct{}), make(chan struct{})
	scheduler := NewDeadlineScheduler(fakeClock, func(ProjectRequest) { close(emitStarted); <-releaseEmit })
	if err := scheduler.Schedule(request, fakeClock.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stepDone := make(chan struct{})
	go func() { fakeClock.Step(time.Second); close(stepDone) }()
	<-emitStarted
	stopDone := make(chan struct{})
	go func() { scheduler.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
		t.Fatal("Stop returned while emission was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseEmit)
	<-stepDone
	<-stopDone
	if len(scheduler.pending) != 0 {
		t.Fatalf("Stop retained %d deadlines", len(scheduler.pending))
	}
}

func TestProjectVerifierBlocksHandlers(t *testing.T) {
	project := testProject("milo", "project")
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "uid"}
	called := false
	reconciler := ProjectReconciler{Project: project, Verifier: ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return errors.New("stale project") }), Handler: HandlerFunc(func(context.Context, ProjectRequest) Outcome { called = true; return Outcome{} })}
	if _, err := reconciler.Reconcile(context.Background(), request); err == nil || called {
		t.Fatalf("err=%v called=%t", err, called)
	}
	reconciler.Verifier = ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return ErrEventWiringUnavailable })
	if result, err := reconciler.Reconcile(context.Background(), request); !errors.Is(err, ErrEventWiringUnavailable) || result.Requeue || result.RequeueAfter != 0 || called {
		t.Fatalf("unwired verifier result=%#v err=%v called=%t", result, err, called)
	}
}

func TestRouteVerifierUnavailableRetriesExactEvent(t *testing.T) {
	project := testProject("milo", "project")
	route := RouteRequest{AuthorityID: "milo", ProjectUID: project.UID, Kind: RouteClass, Name: "class", UID: "class-uid"}
	reconciler := RouteReconciler{Project: project, Verifier: ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return ErrEventWiringUnavailable }), Handler: HandlerFunc(func(context.Context, ProjectRequest) Outcome { t.Fatal("handler ran without wiring"); return Outcome{} })}
	if result, err := reconciler.Reconcile(context.Background(), route); !errors.Is(err, ErrEventWiringUnavailable) || result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("unwired route result=%#v err=%v", result, err)
	}
}

func TestInstallationClaimIndexIncludesNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	left := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "install", UID: "left-install"}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: "claim"}}}
	right := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "right", Name: "install", UID: "right-install"}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: "claim"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(left, right).
		WithIndex(&fleetv1.Installation{}, IndexInstallationClaim, func(object client.Object) []string {
			installation := object.(*fleetv1.Installation)
			return []string{installation.Namespace + "/" + installation.Spec.MachineRef.Name}
		}).Build()
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "claim", UID: "claim-uid"}}
	requests, err := (Mappers{Reader: kube, Project: testProject("milo", "project")}).installationsForClaimE(context.Background(), claim)
	if err != nil || len(requests) != 1 || requests[0].UID != left.UID {
		t.Fatalf("requests=%v err=%v", requests, err)
	}
}

func TestInstallationEventRoutesOnlySameNamespaceInitialReference(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	left := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "claim", UID: "left-claim"}, Spec: fleetv1.MachineSpec{InitialInstallationRef: &fleetv1.LocalReference{Name: "bootstrap"}}}
	right := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "right", Name: "claim", UID: "right-claim"}, Spec: fleetv1.MachineSpec{InitialInstallationRef: &fleetv1.LocalReference{Name: "bootstrap"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(left, right).
		WithIndex(&fleetv1.Machine{}, IndexClaimInstallation, func(object client.Object) []string {
			claim := object.(*fleetv1.Machine)
			return []string{claim.Namespace + "/" + claim.Spec.InitialInstallationRef.Name}
		}).Build()
	project := testProject("milo", "project")
	mapper := Mappers{Reader: kube, Project: project}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "bootstrap", UID: "installation"}}
	routes := mapper.Route(RouteInstallation)(context.Background(), installation)
	requests, err := mapper.ResolveRoute(context.Background(), routes[0])
	if err != nil || len(requests) != 1 || requests[0].UID != left.UID {
		t.Fatalf("requests=%v err=%v", requests, err)
	}
}

func TestNoPollingASTAndManagerCacheConfiguration(t *testing.T) {
	fset := token.NewFileSet()
	for _, root := range []string{".", "../../cmd/fleet-manager"} {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, entry.Name())
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.CallExpr:
					if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
						name := selector.Sel.Name
						if name == "Tick" || name == "After" || name == "NewTimer" || name == "NewTicker" || strings.Contains(name, "Poll") || strings.Contains(name, "Until") {
							t.Errorf("%s contains forbidden timer/poll call %s", path, name)
						}
						if name == "NewManager" && !callHasCacheOption(value) {
							t.Errorf("%s constructs a manager without explicit Cache options", path)
						}
						if name == "New" {
							if pkg, ok := selector.X.(*ast.Ident); ok && (pkg.Name == "cache" || pkg.Name == "manager") {
								t.Errorf("%s bypasses the reviewed manager/cache constructor through %s.New", path, pkg.Name)
							}
						}
					}
				case *ast.CompositeLit:
					for _, element := range value.Elts {
						pair, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := pair.Key.(*ast.Ident)
						if !ok {
							continue
						}
						if key.Name == "RequeueAfter" {
							t.Errorf("%s sets RequeueAfter", path)
						}
						if key.Name == "Requeue" {
							if truth, ok := pair.Value.(*ast.Ident); ok && truth.Name == "true" {
								t.Errorf("%s sets Requeue true", path)
							}
						}
					}
				}
				return true
			})
		}
	}
}

func callHasCacheOption(call *ast.CallExpr) bool {
	// ctrl.NewManager takes options second; projectdiscovery.NewManager takes
	// them third. Either way the options literal must set Cache explicitly.
	for _, arg := range call.Args[min(1, len(call.Args)):] {
		literal, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			if ok && key.Name == "Cache" {
				return true
			}
		}
	}
	return false
}

type failFirstListReader struct {
	client.Reader
	mu        sync.Mutex
	remaining int
}

func (r *failFirstListReader) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	r.mu.Lock()
	if r.remaining > 0 {
		r.remaining--
		r.mu.Unlock()
		return errors.New("injected list failure")
	}
	r.mu.Unlock()
	return r.Reader.List(ctx, list, options...)
}

type fakeReplaySource struct {
	events     []ExternalEvent
	watch      []WatchedEvent
	nextCursor string
	failAfter  int
}

func (s *fakeReplaySource) Replay(_ context.Context, _ string, fn func(ExternalEvent) error) (string, error) {
	for index, event := range s.events {
		if err := fn(event); err != nil {
			return "", err
		}
		if s.failAfter == index+1 {
			return "", errors.New("injected crash after persist")
		}
	}
	return s.nextCursor, nil
}
func (s *fakeReplaySource) Watch(_ context.Context, _ string, fn func(WatchedEvent) error) error {
	for _, delivery := range s.watch {
		if err := fn(delivery); err != nil {
			return err
		}
	}
	return nil
}

type fakeCursors struct{ cursor string }

func (s *fakeCursors) Load(context.Context, string) (string, error) { return s.cursor, nil }
func (s *fakeCursors) Commit(_ context.Context, _ string, cursor string) error {
	s.cursor = cursor
	return nil
}

type fakeInbox struct {
	persisted map[string]ExternalEvent
	acked     map[string]bool
	calls     int
}

func (i *fakeInbox) Persist(_ context.Context, event ExternalEvent) (InboxReceipt, error) {
	if i.persisted == nil {
		i.persisted = map[string]ExternalEvent{}
	}
	i.calls++
	key := event.Source + "/" + event.Sequence
	if previous, ok := i.persisted[key]; ok && previous != event {
		return InboxReceipt{}, errors.New("inbox conflict")
	}
	i.persisted[key] = event
	return InboxReceipt{Source: event.Source, Sequence: event.Sequence, RecordID: event.RecordID, WorkID: key}, nil
}

func (i *fakeInbox) Pending(_ context.Context, source string) ([]InboxWork, error) {
	var result []InboxWork
	for key, event := range i.persisted {
		if event.Source == source && !i.acked[key] {
			result = append(result, InboxWork{Event: event, Receipt: InboxReceipt{Source: event.Source, Sequence: event.Sequence, RecordID: event.RecordID, WorkID: key}})
		}
	}
	return result, nil
}

func (i *fakeInbox) Ack(_ context.Context, receipt InboxReceipt) error {
	if i.acked == nil {
		i.acked = map[string]bool{}
	}
	if _, ok := i.persisted[receipt.WorkID]; !ok {
		return errors.New("unknown inbox work")
	}
	i.acked[receipt.WorkID] = true
	return nil
}
