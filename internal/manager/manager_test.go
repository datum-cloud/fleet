package manager

import (
	"context"
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

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
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
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "same-uid"}}
	left := (Mappers{Project: testProject("milo", "project-1")}).RequestFor(context.Background(), claim)
	right := (Mappers{Project: testProject("milo", "project-2")}).RequestFor(context.Background(), claim)
	otherAuthority := (Mappers{Project: testProject("other", "project-1")}).RequestFor(context.Background(), claim)
	if len(left) != 1 || len(right) != 1 || len(otherAuthority) != 1 || left[0] == right[0] || left[0] == otherAuthority[0] {
		t.Fatalf("mapped lifetimes were conflated: %v %v %v", left, right, otherAuthority)
	}
}

func TestCacheDisablesSyntheticResync(t *testing.T) {
	project := ProjectCacheOptions()
	if project.SyncPeriod == nil || *project.SyncPeriod != 0 {
		t.Fatalf("project SyncPeriod = %#v, want nonnil zero", project.SyncPeriod)
	}
}

func TestPrimaryPredicateIgnoresStatusProjection(t *testing.T) {
	oldClaim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "claim-uid", Generation: 3}}
	newClaim := oldClaim.DeepCopyObject().(*fleetv1.Machine)
	newClaim.Status.Phase = "Ready"
	if PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("status-only projector update re-enqueued coordinator")
	}
	newClaim.Generation++
	if !PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("generation change was filtered")
	}
	newClaim = oldClaim.DeepCopyObject().(*fleetv1.Machine)
	newClaim.Labels = map[string]string{"fleet.datumapis.com/test": "changed"}
	if !PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("label change was filtered")
	}
	newClaim = oldClaim.DeepCopyObject().(*fleetv1.Machine)
	newClaim.Annotations = map[string]string{"fleet.datumapis.com/test": "changed"}
	if !PrimaryPredicate().Update(event.UpdateEvent{ObjectOld: oldClaim, ObjectNew: newClaim}) {
		t.Fatal("annotation change was filtered")
	}

	oldMachine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "machine-uid", Generation: 2}}
	newMachine := oldMachine.DeepCopyObject().(*fleetv1.MachineCapacity)
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
	if routes := (Mappers{Project: testProject("milo", "project")}).Route(RouteClass)(context.Background(), class); len(routes) != 0 {
		t.Fatalf("UID-less dependency entered queue: %v", routes)
	}
}

func TestRouteRejectsRecreatedDependencyLifetime(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "new-class-uid"}}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "claim-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "class"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, claim).
		WithIndex(&fleetv1.Machine{}, IndexClaimClassName, func(object client.Object) []string {
			return []string{object.(*fleetv1.Machine).Spec.MachineClassRef.Name}
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
	project := testProject("milo", "project")
	oldMachine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "old-machine", Labels: map[string]string{"pool": "old"}}}
	newMachine := &fleetv1.MachineCapacity{ObjectMeta: *oldMachine.ObjectMeta.DeepCopy()}
	newMachine.UID = "new-machine"
	newMachine.Labels = map[string]string{"pool": "new"}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "old", UID: "old-class"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "old"}}}}}}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim", UID: "claim-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "old"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newMachine, class, claim).
		WithIndex(&fleetv1.MachineClass{}, IndexClassMachineNS, func(object client.Object) []string {
			return []string{object.(*fleetv1.MachineClass).Spec.Capacity.MachineCapacities.Namespace}
		}).
		WithIndex(&fleetv1.Machine{}, IndexClaimClassName, func(object client.Object) []string {
			return []string{object.(*fleetv1.Machine).Spec.MachineClassRef.Name}
		}).Build()
	mapper := Mappers{Reader: kube, Project: project}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap(mapper.Route(RouteMachineCapacity)).Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: oldMachine}, queue)
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
	oldMachine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "machine-uid", Labels: map[string]string{"pool": "blue"}}}
	newMachine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "machine", UID: "machine-uid", Labels: map[string]string{"pool": "red"}}}
	blue := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "blue", UID: "blue-class"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "blue"}}}}}}
	red := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "red", UID: "red-class"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "red"}}}}}}
	blueClaim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "blue", UID: "blue-claim"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "blue"}}}
	redClaim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "red", UID: "red-claim"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "red"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newMachine, blue, red, blueClaim, redClaim).
		WithIndex(&fleetv1.MachineClass{}, IndexClassMachineNS, func(object client.Object) []string {
			return []string{object.(*fleetv1.MachineClass).Spec.Capacity.MachineCapacities.Namespace}
		}).
		WithIndex(&fleetv1.Machine{}, IndexClaimClassName, func(object client.Object) []string {
			return []string{object.(*fleetv1.Machine).Spec.MachineClassRef.Name}
		}).Build()
	mapper := Mappers{Reader: kube, Project: project}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap(mapper.Route(RouteMachineCapacity)).Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldMachine, ObjectNew: newMachine}, queue)
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

func TestQuotaUpdateRoutesOldAndNewMachineLabels(t *testing.T) {
	project := testProject("milo", "project")
	oldQuota := &quotav1alpha1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "quota", UID: "quota-uid", Labels: map[string]string{"fleet.datumapis.com/machine-uid": "old-machine"}}}
	newQuota := &quotav1alpha1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "quota", UID: "quota-uid", Labels: map[string]string{"fleet.datumapis.com/machine-uid": "new-machine"}}}
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap((Mappers{Project: project}).Route(RouteQuota)).Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldQuota, ObjectNew: newQuota}, queue)
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
	if !keys["old-machine"] || !keys["new-machine"] || len(keys) != 2 {
		t.Fatalf("quota label update retained keys %v", keys)
	}
	overlap := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[RouteRequest]())
	typedRouteMap((Mappers{Project: project}).Route(RouteQuota)).Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldQuota, ObjectNew: oldQuota}, overlap)
	if overlap.Len() != 1 {
		t.Fatalf("overlapping quota snapshots queued %d routes, want 1", overlap.Len())
	}
	overlap.ShutDown()
}

// A quota claim wakes only the Machine its machine-uid label names; a claim
// without the label routes nowhere.
func TestQuotaRouteResolvesOnlyItsLabelledMachine(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := quotav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := testProject("milo", "project")
	owner := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "owner", UID: "owner-uid"}}
	other := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "other", UID: "other-uid"}}
	quota := &quotav1alpha1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "quota", UID: "quota-uid", Labels: map[string]string{"fleet.datumapis.com/machine-uid": "owner-uid"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, other, quota).
		WithIndex(&fleetv1.Machine{}, IndexClaimUID, func(object client.Object) []string { return []string{string(object.GetUID())} }).Build()
	mapper := Mappers{Reader: kube, Project: project}
	routes := mapper.Route(RouteQuota)(context.Background(), quota)
	if len(routes) != 1 {
		t.Fatalf("quota routes = %v", routes)
	}
	requests, err := mapper.ResolveRoute(context.Background(), routes[0])
	if err != nil || len(requests) != 1 || requests[0].UID != owner.UID {
		t.Fatalf("quota requests=%v err=%v", requests, err)
	}
	unlabelled := quota.DeepCopy()
	unlabelled.Labels = nil
	if routes := mapper.Route(RouteQuota)(context.Background(), unlabelled); len(routes) != 0 {
		t.Fatalf("unlabelled quota routed: %v", routes)
	}
}

// An interface claim wakes only the Machine its machine-uid label names, so
// NSO's status reaches Machine network status; a claim without the label
// routes nowhere.
func TestInterfaceClaimRouteResolvesOnlyItsLabelledMachine(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := testProject("milo", "project")
	owner := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "owner", UID: "owner-uid"}}
	other := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "other", UID: "other-uid"}}
	claim := &networkingv1.NetworkInterfaceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "fleet-claim", UID: "claim-uid", Labels: map[string]string{"fleet.datumapis.com/machine-uid": "owner-uid"}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, other, claim).
		WithIndex(&fleetv1.Machine{}, IndexClaimUID, func(object client.Object) []string { return []string{string(object.GetUID())} }).Build()
	mapper := Mappers{Reader: kube, Project: project}
	routes := mapper.Route(RouteInterfaceClaim)(context.Background(), claim)
	if len(routes) != 1 {
		t.Fatalf("claim routes = %v", routes)
	}
	requests, err := mapper.ResolveRoute(context.Background(), routes[0])
	if err != nil || len(requests) != 1 || requests[0].UID != owner.UID {
		t.Fatalf("claim requests=%v err=%v", requests, err)
	}
	unlabelled := claim.DeepCopy()
	unlabelled.Labels = nil
	if routes := mapper.Route(RouteInterfaceClaim)(context.Background(), unlabelled); len(routes) != 0 {
		t.Fatalf("unlabelled claim routed: %v", routes)
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

func TestMachineMapperUsesExactIndexesAndOnlyMatchingClasses(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := testProject("milo", "project-uid")
	machine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "m1", UID: "machine-uid", Labels: map[string]string{"pool": "blue"}}}
	blue := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "blue", UID: "class-blue"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "blue"}}}}}}
	red := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "red", UID: "class-red"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "provider", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "red"}}}}}}
	blueClaim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "blue-claim", UID: "blue-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "blue"}}}
	redClaim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "red-claim", UID: "red-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "red"}}}
	base := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(machine, blue, red, blueClaim, redClaim).
		WithIndex(&fleetv1.MachineClass{}, IndexClassMachineNS, func(o client.Object) []string {
			return []string{o.(*fleetv1.MachineClass).Spec.Capacity.MachineCapacities.Namespace}
		}).
		WithIndex(&fleetv1.Machine{}, IndexClaimClassName, func(o client.Object) []string { return []string{o.(*fleetv1.Machine).Spec.MachineClassRef.Name} }).Build()
	reader := &indexedReader{Reader: base}
	requests, err := (Mappers{Reader: reader, Project: project}).claimsForMachineE(context.Background(), machine)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Name != blueClaim.Name {
		t.Fatalf("machine routes = %v", requests)
	}
	if reader.unqualified != 0 || reader.lists != 2 {
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
