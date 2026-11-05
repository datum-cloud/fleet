package manager

import (
	"context"
	"encoding/json"
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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/milo"
)

func TestInitialDependencyAddRetriesFirstListFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid"}}
	claim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "claim-uid"}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "class"}}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, claim).
		WithIndex(&fleetv1.MachineClaim{}, IndexClaimClassName, func(object client.Object) []string {
			return []string{object.(*fleetv1.MachineClaim).Spec.MachineClassRef.Name}
		}).Build()
	reader := &failFirstListReader{Reader: base, remaining: 1}
	project := testProject("milo", "project")
	mappers := Mappers{Reader: reader, Project: project, PrivateNamespace: "private"}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap(mappers.Route(RouteClass, "")).Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: class, IsInInitialList: true}, queue)
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

func TestPrivateSecretRoutingRejectsForgedIdentities(t *testing.T) {
	project := testProject("milo", "project")
	valid := privateSecret(t, project, "default", "claim", "claim-uid", "machine-uid")
	mapper := Mappers{Project: project, PrivateNamespace: "private"}
	mutations := []*corev1.Secret{valid.DeepCopy(), valid.DeepCopy(), valid.DeepCopy(), valid.DeepCopy(), valid.DeepCopy(), valid.DeepCopy(), valid.DeepCopy(), valid.DeepCopy()}
	mutations[0].Namespace = "wrong"
	mutations[1].Type = corev1.SecretTypeOpaque
	mutations[2].Labels["fleet.datumapis.com/private-record"] = "other"
	mutations[3].Name = "user-chosen"
	mutateCustody := func(secret *corev1.Secret, mutate func(*controller.ClaimCustodyRecord)) {
		var record controller.ClaimCustodyRecord
		if err := json.Unmarshal(secret.Data["record.json"], &record); err != nil {
			t.Fatal(err)
		}
		mutate(&record)
		secret.Data["record.json"], _ = json.Marshal(record)
	}
	mutateCustody(mutations[4], func(record *controller.ClaimCustodyRecord) { record.SchemaVersion = 99 })
	mutateCustody(mutations[5], func(record *controller.ClaimCustodyRecord) { record.Scope.ProjectUID = "other-project" })
	mutateCustody(mutations[6], func(record *controller.ClaimCustodyRecord) { record.ConsumerProject.UID = "other-project" })
	mutateCustody(mutations[7], func(record *controller.ClaimCustodyRecord) { record.ClaimNamespace = "" })
	for i, secret := range mutations {
		if requests := mapper.RequestForPrivate(context.Background(), secret); len(requests) != 0 {
			t.Fatalf("forgery %d amplified to %v", i, requests)
		}
		if values := privateMetadataIndexer("private", func(route controller.PrivateRecordRoute) []string { return one(string(route.ClaimUID)) })(secret); len(values) != 0 {
			t.Fatalf("forgery %d entered reverse index: %v", i, values)
		}
	}
	if requests := mapper.RequestForPrivate(context.Background(), valid); len(requests) != 1 {
		t.Fatalf("valid private record routes %v", requests)
	}
}

func TestPrivateIndexesIgnoreUnknownNestedRoutingLookalikes(t *testing.T) {
	project := testProject("milo", "project")
	secret := privateSecret(t, project, "default", "claim", "consumer-claim", "machine")
	var raw map[string]any
	if err := json.Unmarshal(secret.Data["record.json"], &raw); err != nil {
		t.Fatal(err)
	}
	raw["unknown"] = map[string]any{"machineUID": "injected-machine", "networkUID": "injected-network", "networkInterfaceUID": "injected-interface", "claimUID": "injected-nic"}
	secret.Data["record.json"], _ = json.Marshal(raw)
	indexes := map[string]client.IndexerFunc{
		"machine":   privateMetadataIndexer("private", func(route controller.PrivateRecordRoute) []string { return uidStrings(route.MachineUIDs) }),
		"network":   privateMetadataIndexer("private", func(route controller.PrivateRecordRoute) []string { return uidStrings(route.NetworkUIDs) }),
		"nic":       privateMetadataIndexer("private", func(route controller.PrivateRecordRoute) []string { return uidStrings(route.NICClaimUIDs) }),
		"interface": privateMetadataIndexer("private", func(route controller.PrivateRecordRoute) []string { return uidStrings(route.InterfaceUIDs) }),
	}
	for name, index := range indexes {
		if values := index(secret); len(values) != 0 {
			t.Fatalf("unknown nested field entered %s index: %v", name, values)
		}
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
	if result, err := reconciler.Reconcile(context.Background(), request); err != nil || result.Requeue || result.RequeueAfter != 0 || called {
		t.Fatalf("unwired verifier result=%#v err=%v called=%t", result, err, called)
	}
}

func TestEntitlementRouteRequiresExactName(t *testing.T) {
	mapper := Mappers{Project: testProject("milo", "project")}
	wrong := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other", UID: "uid"}}
	if routes := mapper.Route(RouteEntitlement, "")(context.Background(), wrong); len(routes) != 0 {
		t.Fatalf("wrong entitlement routed: %v", routes)
	}
}

func TestInstallationClaimIndexIncludesNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	left := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "install", UID: "left-install"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: "claim"}}}
	right := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "right", Name: "install", UID: "right-install"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: "claim"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(left, right).
		WithIndex(&fleetv1.Installation{}, IndexInstallationClaim, func(object client.Object) []string {
			installation := object.(*fleetv1.Installation)
			return []string{installation.Namespace + "/" + installation.Spec.ClaimRef.Name}
		}).Build()
	claim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "claim", UID: "claim-uid"}}
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
	left := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "claim", UID: "left-claim"}, Spec: fleetv1.MachineClaimSpec{InitialInstallationRef: &fleetv1.LocalReference{Name: "bootstrap"}}}
	right := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "right", Name: "claim", UID: "right-claim"}, Spec: fleetv1.MachineClaimSpec{InitialInstallationRef: &fleetv1.LocalReference{Name: "bootstrap"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(left, right).
		WithIndex(&fleetv1.MachineClaim{}, IndexClaimInstallation, func(object client.Object) []string {
			claim := object.(*fleetv1.MachineClaim)
			return []string{claim.Namespace + "/" + claim.Spec.InitialInstallationRef.Name}
		}).Build()
	project := testProject("milo", "project")
	mapper := Mappers{Reader: kube, Project: project}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "left", Name: "bootstrap", UID: "installation"}}
	routes := mapper.Route(RouteInstallation, "")(context.Background(), installation)
	requests, err := mapper.ResolveRoute(context.Background(), routes[0])
	if err != nil || len(requests) != 1 || requests[0].UID != left.UID {
		t.Fatalf("requests=%v err=%v", requests, err)
	}
}

func TestPrivateInstallationInitialAddRecoversWithoutPublicInstallation(t *testing.T) {
	project := testProject("milo", "project-a")
	mapper := Mappers{Project: project, PrivateNamespace: "private"}
	owned := privateInstallationSecret(t, project, "default", "bootstrap", "shared-installation-uid", "claim-uid")
	want := ProjectRequest{AuthorityID: "milo", ProjectUID: "project-a", Namespace: "default", Name: "bootstrap", UID: "shared-installation-uid"}
	for restart := 0; restart < 2; restart++ {
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[ProjectRequest]())
		typedMap(mapper.RequestForPrivateInstallation).Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: owned, IsInInitialList: true}, queue)
		request, shutdown := queue.Get()
		if shutdown || request != want {
			t.Fatalf("restart %d request=%#v want=%#v shutdown=%t", restart, request, want, shutdown)
		}
		queue.Done(request)
		queue.ShutDown()
	}
	foreign := owned.DeepCopy()
	var foreignRecord controller.InstallationRecord
	if err := json.Unmarshal(foreign.Data["record.json"], &foreignRecord); err != nil {
		t.Fatal(err)
	}
	foreignRecord.ConsumerProject = testProject("milo", "project-b")
	foreignRecord.ClaimScope = controller.ProjectClaimScope(foreignRecord.ConsumerProject)
	foreign.Data["record.json"], _ = json.Marshal(foreignRecord)
	if requests := mapper.RequestForPrivateInstallation(context.Background(), foreign); len(requests) != 0 {
		t.Fatalf("equal UID from foreign project lifetime routed: %v", requests)
	}
}

func TestPendingEntitlementIndexAcceptsOnlyPreparedMiloAuthority(t *testing.T) {
	project := testProject("milo", "project")
	base := privateMiloAuthoritySecret(t, project, "default", "claim", "claim-uid")
	index := pendingEntitlementProjects("private")
	if values := index(base); len(values) != 1 || values[0] != "milo/project" {
		t.Fatalf("prepared Milo authority index=%v", values)
	}
	for name, mutate := range map[string]func(*corev1.Secret){
		"lifecycle": func(secret *corev1.Secret) {
			secret.Name = "fleet-asset-record"
			secret.Type = corev1.SecretType("fleet.datumapis.com/private-lifecycle")
			secret.Labels["fleet.datumapis.com/private-record"] = "asset"
		},
		"wrong-state": func(secret *corev1.Secret) {
			secret.Data["record.json"] = []byte(`{"state":"QuotaIntent","claimNamespace":"default","claimName":"claim","claimUID":"claim-uid","consumerProject":{"authorityID":"milo","uid":"project"}}`)
		},
		"has-entitlement": func(secret *corev1.Secret) {
			secret.Data["record.json"] = []byte(`{"state":"Prepared","claimNamespace":"default","claimName":"claim","claimUID":"claim-uid","consumerProject":{"authorityID":"milo","uid":"project"},"entitlement":{"uid":"entitlement"}}`)
		},
	} {
		secret := base.DeepCopy()
		mutate(secret)
		if values := index(secret); len(values) != 0 {
			t.Fatalf("%s amplified into pending project index: %v", name, values)
		}
	}
}

func TestMiloOuterClaimMismatchCannotRouteOrIndex(t *testing.T) {
	project := testProject("milo", "project")
	secret := privateMiloAuthoritySecret(t, project, "default", "claim", "claim-uid")
	var record milo.AuthorityRecord
	if err := json.Unmarshal(secret.Data["record.json"], &record); err != nil {
		t.Fatal(err)
	}
	record.ClaimUID = "substituted-outer-claim"
	secret.Data["record.json"], _ = json.Marshal(record)
	mapper := Mappers{Project: project, PrivateNamespace: "private"}
	if requests := mapper.RequestForPrivate(context.Background(), secret); len(requests) != 0 {
		t.Fatalf("mismatched authority record routed: %v", requests)
	}
	if values := privateMetadataIndexer("private", func(route controller.PrivateRecordRoute) []string { return one(string(route.ClaimUID)) })(secret); len(values) != 0 {
		t.Fatalf("mismatched authority record indexed: %v", values)
	}
	if values := pendingEntitlementProjects("private")(secret); len(values) != 0 {
		t.Fatalf("mismatched authority record entered pending index: %v", values)
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
	if len(call.Args) < 2 {
		return false
	}
	literal, ok := call.Args[1].(*ast.CompositeLit)
	if !ok {
		return false
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
