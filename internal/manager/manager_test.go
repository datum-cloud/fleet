package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/milo"
	fleetnetwork "go.datum.net/fleet/internal/network"
)

func testProject(authority string, uid types.UID) controller.ProjectIdentity {
	return controller.ProjectIdentity{AuthorityID: authority, RouteName: "route", UID: uid}
}

func TestProjectRequestSeparatesIndependentProjectLifetimes(t *testing.T) {
	left := ProjectRequest{AuthorityID: "milo-a", ProjectUID: "project-1", Namespace: "default", Name: "claim", UID: "same-uid"}
	right := ProjectRequest{AuthorityID: "milo-a", ProjectUID: "project-2", Namespace: "default", Name: "claim", UID: "same-uid"}
	otherAuthority := left
	otherAuthority.AuthorityID = "milo-b"
	if !left.Valid() || left == right || left == otherAuthority {
		t.Fatalf("project requests were conflated: %#v %#v %#v", left, right, otherAuthority)
	}
}

func TestObjectMapperKeepsSameNameAndUIDInConfiguredProject(t *testing.T) {
	claim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "same-uid"}}
	left := (Mappers{Project: testProject("milo", "project-1")}).RequestFor(context.Background(), claim)
	right := (Mappers{Project: testProject("milo", "project-2")}).RequestFor(context.Background(), claim)
	otherAuthority := (Mappers{Project: testProject("other", "project-1")}).RequestFor(context.Background(), claim)
	if len(left) != 1 || len(right) != 1 || len(otherAuthority) != 1 || left[0] == right[0] || left[0] == otherAuthority[0] {
		t.Fatalf("mapped lifetimes were conflated: %v %v %v", left, right, otherAuthority)
	}
}

func TestCacheDisablesSyntheticResync(t *testing.T) {
	options := CacheOptions("private")
	if options.SyncPeriod == nil || *options.SyncPeriod != 0 {
		t.Fatalf("SyncPeriod = %#v, want nonnil zero", options.SyncPeriod)
	}
	var secretOptions *time.Duration
	for object, byObject := range options.ByObject {
		if _, ok := object.(*corev1.Secret); ok {
			secretOptions = byObject.SyncPeriod
		}
	}
	if secretOptions == nil || *secretOptions != 0 {
		t.Fatalf("Secret SyncPeriod = %#v, want nonnil zero", secretOptions)
	}
}

func TestPrimaryPredicateIgnoresStatusProjection(t *testing.T) {
	oldClaim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "claim-uid", Generation: 3}}
	newClaim := oldClaim.DeepCopyObject().(*fleetv1.MachineClaim)
	newClaim.Status.Phase = "Ready"
	if PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("status-only projector update re-enqueued coordinator")
	}
	newClaim.Generation++
	if !PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("generation change was filtered")
	}
	newClaim = oldClaim.DeepCopyObject().(*fleetv1.MachineClaim)
	newClaim.Labels = map[string]string{"fleet.datumapis.com/test": "changed"}
	if !PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("label change was filtered")
	}
	newClaim = oldClaim.DeepCopyObject().(*fleetv1.MachineClaim)
	newClaim.Annotations = map[string]string{"fleet.datumapis.com/test": "changed"}
	if !PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("annotation change was filtered")
	}

	oldMachine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "machine-uid", Generation: 2}}
	newMachine := oldMachine.DeepCopyObject().(*fleetv1.Machine)
	newMachine.Status.Hardware.CPU.Cores = 24
	if !DependencyPredicate().Update(event.UpdateEvent{ObjectOld: oldMachine, ObjectNew: newMachine}) {
		t.Fatal("provider dependency status change was filtered")
	}
}

func TestRouteRequestRequiresExactDependencyUID(t *testing.T) {
	request := RouteRequest{AuthorityID: "milo", ProjectUID: "project", Kind: RouteClass, Name: "class"}
	if request.Valid() {
		t.Fatal("route without dependency UID validated")
	}
	request.UID = "class-uid"
	if !request.Valid() {
		t.Fatal("exact dependency route was rejected")
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class"}}
	if routes := (Mappers{Project: testProject("milo", "project")}).Route(RouteClass, "")(context.Background(), class); len(routes) != 0 {
		t.Fatalf("UID-less dependency entered queue: %v", routes)
	}
}

func TestRouteRejectsRecreatedDependencyLifetime(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "new-class-uid"}}
	claim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "claim-uid"}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "class"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, claim).
		WithIndex(&fleetv1.MachineClaim{}, IndexClaimClassName, func(object client.Object) []string {
			return []string{object.(*fleetv1.MachineClaim).Spec.MachineClassRef.Name}
		}).Build()
	mapper := Mappers{Reader: kube, Project: testProject("milo", "project")}
	stale := RouteRequest{AuthorityID: "milo", ProjectUID: "project", Kind: RouteClass, Name: class.Name, UID: "old-class-uid"}
	requests, err := mapper.ResolveRoute(context.Background(), stale)
	if err != nil || len(requests) != 0 {
		t.Fatalf("stale dependency routed requests=%v err=%v", requests, err)
	}
	stale.UID = class.UID
	requests, err = mapper.ResolveRoute(context.Background(), stale)
	if err != nil || len(requests) != 1 || requests[0].UID != claim.UID {
		t.Fatalf("current dependency requests=%v err=%v", requests, err)
	}
}

func TestMachineDeleteSnapshotRoutesOldLifetimeAfterRecreation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := testProject("milo", "project")
	oldMachine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "old-machine", Labels: map[string]string{"pool": "old"}}}
	newMachine := &fleetv1.Machine{ObjectMeta: *oldMachine.ObjectMeta.DeepCopy()}
	newMachine.UID = "new-machine"
	newMachine.Labels = map[string]string{"pool": "new"}
	secret := privateSecret(t, project, "default", "claim", "claim-uid", "old-machine")
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newMachine, secret).
		WithIndex(&fleetv1.MachineClass{}, IndexClassMachineNS, func(client.Object) []string { return nil }).
		WithIndex(&corev1.Secret{}, IndexPrivateMachineUID, func(object client.Object) []string { return []string{"old-machine"} }).Build()
	mapper := Mappers{Reader: kube, Project: project, PrivateNamespace: "private"}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap(mapper.Route(RouteMachine, "")).Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: oldMachine}, queue)
	route, shutdown := queue.Get()
	if shutdown {
		t.Fatal("delete snapshot was not queued")
	}
	queue.Done(route)
	queue.ShutDown()
	if !route.Deleted || route.UID != oldMachine.UID {
		t.Fatalf("delete route lost lifetime snapshot: %#v", route)
	}
	requests, err := mapper.ResolveRoute(context.Background(), route)
	if err != nil || len(requests) != 1 || requests[0].UID != "claim-uid" {
		t.Fatalf("old machine deletion requests=%v err=%v", requests, err)
	}
	route.Deleted = false
	requests, err = mapper.ResolveRoute(context.Background(), route)
	if err != nil || len(requests) != 0 {
		t.Fatalf("non-delete stale machine requests=%v err=%v", requests, err)
	}
}

func TestMachineUpdateRoutesOldAndNewSelectorDependents(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := testProject("milo", "project")
	oldMachine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "machine-uid", Labels: map[string]string{"pool": "blue"}}}
	newMachine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "machine-uid", Labels: map[string]string{"pool": "red"}}}
	blue := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "blue", UID: "blue-class"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{Machines: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "blue"}}}}}}
	red := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "red", UID: "red-class"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{Machines: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "red"}}}}}}
	blueClaim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "blue", UID: "blue-claim"}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "blue"}}}
	redClaim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "red", UID: "red-claim"}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "red"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newMachine, blue, red, blueClaim, redClaim).
		WithIndex(&fleetv1.MachineClass{}, IndexClassMachineNS, func(object client.Object) []string {
			return []string{object.(*fleetv1.MachineClass).Spec.Capacity.Machines.Namespace}
		}).
		WithIndex(&fleetv1.MachineClaim{}, IndexClaimClassName, func(object client.Object) []string {
			return []string{object.(*fleetv1.MachineClaim).Spec.MachineClassRef.Name}
		}).
		WithIndex(&corev1.Secret{}, IndexPrivateMachineUID, func(client.Object) []string { return nil }).Build()
	mapper := Mappers{Reader: kube, Project: project, PrivateNamespace: "private"}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap(mapper.Route(RouteMachine, "")).Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldMachine, ObjectNew: newMachine}, queue)
	if queue.Len() != 2 {
		t.Fatalf("machine update queued %d routing snapshots, want 2", queue.Len())
	}
	routed := map[types.UID]bool{}
	for queue.Len() > 0 {
		route, shutdown := queue.Get()
		if shutdown {
			t.Fatal("route queue shut down")
		}
		requests, err := mapper.ResolveRoute(context.Background(), route)
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range requests {
			routed[request.UID] = true
		}
		queue.Done(route)
	}
	queue.ShutDown()
	if !routed[blueClaim.UID] || !routed[redClaim.UID] || len(routed) != 2 {
		t.Fatalf("machine selector update routed %v", routed)
	}
}

func TestQuotaUpdateRoutesOldAndNewClaimLabels(t *testing.T) {
	project := testProject("milo", "project")
	oldQuota := &quotav1alpha1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "quota", UID: "quota-uid", Labels: map[string]string{"fleet.datumapis.com/machine-claim-uid": "old-claim"}}}
	newQuota := &quotav1alpha1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "quota", UID: "quota-uid", Labels: map[string]string{"fleet.datumapis.com/machine-claim-uid": "new-claim"}}}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap((Mappers{Project: project}).Route(RouteQuota, "")).Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldQuota, ObjectNew: newQuota}, queue)
	if queue.Len() != 2 {
		t.Fatalf("quota update queued %d routing snapshots, want 2", queue.Len())
	}
	keys := map[string]bool{}
	for queue.Len() > 0 {
		route, shutdown := queue.Get()
		if shutdown {
			t.Fatal("route queue shut down")
		}
		keys[route.Key] = true
		queue.Done(route)
	}
	queue.ShutDown()
	if !keys["old-claim"] || !keys["new-claim"] || len(keys) != 2 {
		t.Fatalf("quota label update retained keys %v", keys)
	}
	overlap := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap((Mappers{Project: project}).Route(RouteQuota, "")).Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldQuota, ObjectNew: oldQuota}, overlap)
	if overlap.Len() != 1 {
		t.Fatalf("overlapping quota snapshots queued %d routes, want 1", overlap.Len())
	}
	overlap.ShutDown()
}

func TestPrivateUIDDeleteSnapshotRoutesOldLifetimeAfterRecreation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := testProject("milo", "project")
	oldNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "old-namespace"}}
	newNamespace := oldNamespace.DeepCopy()
	newNamespace.UID = "new-namespace"
	secret := privateSecret(t, project, "default", "claim", "claim-uid", "")
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newNamespace, secret).
		WithIndex(&corev1.Secret{}, IndexPrivateNamespaceUID, func(object client.Object) []string { return []string{"old-namespace"} }).Build()
	mapper := Mappers{Reader: kube, Project: project, PrivateNamespace: "private"}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap(mapper.Route(RoutePrivateUID, IndexPrivateNamespaceUID)).Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: oldNamespace}, queue)
	route, shutdown := queue.Get()
	if shutdown {
		t.Fatal("delete snapshot was not queued")
	}
	queue.Done(route)
	queue.ShutDown()
	if !route.Deleted || route.Key != "old-namespace" {
		t.Fatalf("private UID delete lost snapshot: %#v", route)
	}
	requests, err := mapper.ResolveRoute(context.Background(), route)
	if err != nil || len(requests) != 1 || requests[0].UID != "claim-uid" {
		t.Fatalf("old namespace deletion requests=%v err=%v", requests, err)
	}
	route.Deleted = false
	requests, err = mapper.ResolveRoute(context.Background(), route)
	if err != nil || len(requests) != 0 {
		t.Fatalf("non-delete stale namespace requests=%v err=%v", requests, err)
	}
}

func TestPrivateRecordPredicateQuarantinesEnvelopeSubstitution(t *testing.T) {
	valid := privateSecret(t, testProject("milo", "project"), "default", "claim", "claim-uid", "")
	for name, mutate := range map[string]func(*corev1.Secret){
		"label": func(secret *corev1.Secret) { secret.Labels["fleet.datumapis.com/private-record"] = "other" },
		"type":  func(secret *corev1.Secret) { secret.Type = corev1.SecretTypeOpaque },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := valid.DeepCopy()
			mutate(invalid)
			if !PrivateRecordPredicate().Update(event.UpdateEvent{ObjectOld: valid, ObjectNew: invalid}) {
				t.Fatal("valid-to-quarantined envelope transition was ignored")
			}
			if requests := (Mappers{Project: testProject("milo", "project"), PrivateNamespace: "private"}).RequestForPrivate(context.Background(), invalid); len(requests) != 0 {
				t.Fatalf("substituted envelope routed: %v", requests)
			}
		})
	}
}

func TestDeadlineSchedulerEmitsOnceAndReplacesDeadline(t *testing.T) {
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	clock := clocktesting.NewFakeClock(start)
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "claim-uid"}
	var mu sync.Mutex
	var emitted []ProjectRequest
	scheduler := NewDeadlineScheduler(clock, func(value ProjectRequest) { mu.Lock(); emitted = append(emitted, value); mu.Unlock() })
	scheduler.Schedule(request, start.Add(time.Minute))
	scheduler.Schedule(request, start.Add(2*time.Minute))
	clock.Step(time.Minute)
	mu.Lock()
	if len(emitted) != 0 {
		t.Fatalf("early emissions = %v", emitted)
	}
	mu.Unlock()
	clock.Step(time.Minute)
	mu.Lock()
	if len(emitted) != 1 || emitted[0] != request {
		t.Fatalf("emissions = %v", emitted)
	}
	mu.Unlock()
	clock.Step(24 * time.Hour)
	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 {
		t.Fatalf("deadline repeated: %v", emitted)
	}
}

func TestFreshSchedulerInitialAddRebuildsDeadline(t *testing.T) {
	start := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "claim-uid"}
	for restart := 0; restart < 2; restart++ {
		fakeClock := clocktesting.NewFakeClock(start)
		emitted := make(chan ProjectRequest, 1)
		scheduler := NewDeadlineScheduler(fakeClock, func(value ProjectRequest) { emitted <- value })
		owner := NewLifecycleOwner(HandlerFunc(func(context.Context, ProjectRequest) Outcome {
			return Outcome{Deadline: ScheduleDeadline(start.Add(time.Minute))}
		}), HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} }), scheduler)
		if outcome := owner.ReconcileEvent(context.Background(), request); outcome.RetryError != nil {
			t.Fatalf("restart %d initial Add: %v", restart, outcome.RetryError)
		}
		fakeClock.Step(time.Minute)
		select {
		case got := <-emitted:
			if got != request {
				t.Fatalf("restart %d emitted %#v", restart, got)
			}
		default:
			t.Fatalf("restart %d did not rebuild deadline", restart)
		}
	}
}

func TestPrivateInitialAddRoutesRecoveryWithoutProcessState(t *testing.T) {
	project := testProject("milo", "project-uid")
	secret := privateSecret(t, project, "default", "claim", "claim-uid", "machine-uid")
	for restart := 0; restart < 2; restart++ {
		mapper := Mappers{Project: project, PrivateNamespace: "private"}
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[ProjectRequest]())
		typedMap(mapper.RequestForPrivate).Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: secret, IsInInitialList: true}, queue)
		item, shutdown := queue.Get()
		if shutdown || item.UID != "claim-uid" || item.ProjectUID != project.UID {
			t.Fatalf("restart %d initial Add routes %#v, shutdown=%t", restart, item, shutdown)
		}
		queue.Done(item)
		queue.ShutDown()
	}
}

func TestMachineMapperUsesExactIndexesAndOnlyMatchingClasses(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := testProject("milo", "project-uid")
	machine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "m1", UID: "machine-uid", Labels: map[string]string{"pool": "blue"}}}
	blue := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "blue", UID: "class-blue"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{Machines: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "blue"}}}}}}
	red := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "red", UID: "class-red"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{Machines: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "red"}}}}}}
	blueClaim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "blue-claim", UID: "blue-uid"}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "blue"}}}
	redClaim := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "red-claim", UID: "red-uid"}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: "red"}}}
	base := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(machine, blue, red, blueClaim, redClaim).
		WithIndex(&fleetv1.MachineClass{}, IndexClassMachineNS, func(o client.Object) []string {
			return []string{o.(*fleetv1.MachineClass).Spec.Capacity.Machines.Namespace}
		}).
		WithIndex(&fleetv1.MachineClaim{}, IndexClaimClassName, func(o client.Object) []string { return []string{o.(*fleetv1.MachineClaim).Spec.MachineClassRef.Name} }).
		WithIndex(&corev1.Secret{}, IndexPrivateMachineUID, privateMetadataIndexer("private", func(route controller.PrivateRecordRoute) []string { return uidStrings(route.MachineUIDs) })).Build()
	reader := &indexedReader{Reader: base}
	requests, err := (Mappers{Reader: reader, Project: project, PrivateNamespace: "private"}).claimsForMachineE(context.Background(), machine)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Name != blueClaim.Name {
		t.Fatalf("machine routes = %v", requests)
	}
	if reader.unqualified != 0 || reader.lists != 3 {
		t.Fatalf("List calls=%d unqualified=%d", reader.lists, reader.unqualified)
	}
}

func TestProjectReconcilerNeverTimeRequeuesPendingWork(t *testing.T) {
	project := testProject("milo", "project")
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "claim-uid"}
	called := 0
	verifier := ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return nil })
	reconciler := ProjectReconciler{Project: project, Verifier: verifier, Handler: HandlerFunc(func(context.Context, ProjectRequest) Outcome { called++; return Outcome{} })}
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil || result.RequeueAfter != 0 || result.Requeue || called != 1 {
		t.Fatalf("result=%#v err=%v called=%d", result, err, called)
	}
	retryErr := errors.New("local write failed")
	reconciler.Handler = HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{RetryError: retryErr} })
	result, err = reconciler.Reconcile(context.Background(), request)
	if !errors.Is(err, retryErr) || result.RequeueAfter != 0 || result.Requeue {
		t.Fatalf("retry result=%#v err=%v", result, err)
	}
}

func TestReconcilersFailClosedWithoutRequiredWiring(t *testing.T) {
	project := testProject("milo", "project")
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "claim-uid"}
	verifier := ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return nil })
	for name, reconciler := range map[string]ProjectReconciler{
		"handler":  {Project: project, Verifier: verifier},
		"verifier": {Project: project, Handler: HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} })},
	} {
		if _, err := reconciler.Reconcile(context.Background(), request); !errors.Is(err, ErrEventWiringUnavailable) {
			t.Errorf("%s wiring error=%v", name, err)
		}
	}
	route := RouteRequest{AuthorityID: "milo", ProjectUID: "project", Kind: RouteClass, Namespace: "default", Name: "class", UID: "class-uid"}
	for name, reconciler := range map[string]RouteReconciler{
		"handler":  {Project: project, Verifier: verifier},
		"verifier": {Project: project, Handler: HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} })},
	} {
		if _, err := reconciler.Reconcile(context.Background(), route); !errors.Is(err, ErrEventWiringUnavailable) {
			t.Errorf("%s route wiring error=%v", name, err)
		}
	}
}

func TestManagerSourcesContainNoPollingLoopsOrTimeRequeues(t *testing.T) {
	for _, root := range []string{".", "../../cmd/fleet-manager"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, forbidden := range []string{"RequeueAfter:", "time.NewTicker(", "wait.Poll", "wait.Until", "SyncPeriod: nil"} {
				if strings.Contains(string(data), forbidden) {
					t.Errorf("%s contains forbidden polling construct %q", path, forbidden)
				}
			}
			if strings.HasSuffix(path, "mappers.go") {
				for number, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, ".List(ctx") && !strings.Contains(line, "MatchingFields") {
						t.Errorf("%s:%d has an unindexed List: %s", path, number+1, line)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

type indexedReader struct {
	client.Reader
	lists       int
	unqualified int
}

func (r *indexedReader) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	r.lists++
	settings := &client.ListOptions{LabelSelector: labels.Everything(), FieldSelector: fields.Everything()}
	settings.ApplyOptions(options)
	if settings.FieldSelector == nil || settings.FieldSelector.Empty() {
		r.unqualified++
	}
	return r.Reader.List(ctx, list, options...)
}

func privateSecret(t *testing.T, project controller.ProjectIdentity, namespace, name string, claimUID, machineUID types.UID) *corev1.Secret {
	t.Helper()
	_ = machineUID
	scope := controller.ProjectClaimScope(project)
	record := controller.ClaimCustodyRecord{SchemaVersion: 1, State: controller.CustodyUnbound, ClaimNamespace: namespace, ClaimName: name, ClaimUID: claimUID, NamespaceUID: "namespace-uid", Scope: scope, Admission: controller.AdmissionAuthority{PrincipalUID: "principal", DecisionID: "decision"}, ConsumerProject: project, ProviderProject: testProject("milo", "provider-project"), Authority: controller.AuthorityRef{Provider: "milo", RecordName: "authority", RecordID: "record", ContractDigest: "digest"}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(scope.AuthorityID + "/" + string(scope.ProjectUID) + "/" + string(claimUID)))
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "private", Name: "fleet-claim-" + hex.EncodeToString(sum[:])[:24], UID: "secret-uid", Labels: map[string]string{"fleet.datumapis.com/private-record": "custody", "fleet.datumapis.com/claim-uid": string(claimUID)}}, Type: corev1.SecretType("fleet.datumapis.com/private-lifecycle"), Data: map[string][]byte{"record.json": data}}
}

func privateInstallationSecret(t *testing.T, project controller.ProjectIdentity, namespace, name string, installationUID, claimUID types.UID) *corev1.Secret {
	t.Helper()
	scope := controller.ProjectClaimScope(project)
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	record := controller.InstallationRecord{SchemaVersion: 2, State: controller.OperationAdmitted, OperationID: "install-operation", InstallationNamespace: namespace, InstallationName: name, InstallationUID: installationUID, NamespaceUID: "namespace-uid", ClaimNamespace: namespace, ClaimName: "claim", ClaimUID: claimUID, ClaimScope: scope, ConsumerProject: project, AssetID: "provider/asset", BindingID: "binding", Epoch: 1, Predecessor: "None", ResultRevision: "ir-result", Image: controller.ResolvedImage{Name: "registry/image", Digest: digest, CatalogAuthority: "catalog", CatalogRevision: "r1", ImageDecisionID: "image-decision"}, ImageName: "registry/image@" + digest, Admission: controller.InstallationAdmissionAuthority{PrincipalUID: "principal", DecisionID: "install-decision", CatalogAuthority: "catalog", CatalogRevision: "r1", ImageDecisionID: "image-decision"}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(scope.AuthorityID + "/" + string(scope.ProjectUID) + "/" + string(claimUID) + "/None"))
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "private", Name: "fleet-install-" + hex.EncodeToString(sum[:])[:24], UID: "installation-secret-uid", Labels: map[string]string{"fleet.datumapis.com/private-record": "installation", "fleet.datumapis.com/claim-uid": string(claimUID)}}, Type: corev1.SecretType("fleet.datumapis.com/private-lifecycle"), Data: map[string][]byte{"record.json": data}}
}

func privateMiloAuthoritySecret(t *testing.T, project controller.ProjectIdentity, namespace, name string, claimUID types.UID) *corev1.Secret {
	t.Helper()
	accepted := fleetv1.MachineProfileSpec{}
	requirementsDigest := testDigest(t, accepted)
	request := milo.SourceUseRequest{ConsumerProject: project, ProviderProject: testProject("milo", "provider-project"), NamespaceUID: "namespace-uid", ClaimUID: claimUID, LocalClassUID: "local-class", SourceClassUID: "source-class", SourceClassRevision: "r1", SourceClassDigest: "class-digest", LocalProfileUID: "local-profile", ProfileKind: milo.ProfileCustom, RequirementsDigest: requirementsDigest, PublicationRevision: "publication-r1", Location: fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}}
	receipt := milo.SourceUseReceipt{RequestDigest: testDigest(t, request), PrincipalUID: "principal", DecisionID: "source-decision", PublicationRevision: request.PublicationRevision, FenceToken: "fence"}
	contract := struct {
		Request milo.SourceUseRequest `json:"request"`
		Receipt milo.SourceUseReceipt `json:"receipt"`
	}{request, receipt}
	record := milo.AuthorityRecord{SchemaVersion: 1, RecordID: "authority-record", State: milo.StatePrepared, ClaimNamespace: namespace, ClaimName: name, ClaimUID: claimUID, SourceRequest: request, SourceReceipt: receipt, ContractDigest: testDigest(t, contract), AcceptedRequirements: accepted}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(project.AuthorityID + "/" + string(project.UID) + "/" + string(claimUID)))
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "private", Name: "fleet-milo-" + hex.EncodeToString(sum[:])[:24], UID: "milo-secret-uid", Labels: map[string]string{"fleet.datumapis.com/private-record": "milo-authority", "fleet.datumapis.com/claim-uid": string(claimUID)}}, Type: corev1.SecretType("fleet.datumapis.com/private-milo-authority"), Data: map[string][]byte{"record.json": data}}
}

func testDigest(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
