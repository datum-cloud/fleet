package manager

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"go.datum.net/fleet/internal/controller"
)

func awaitEngagementWaiter(t *testing.T, projects *Projects, name multicluster.ClusterName) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		projects.mu.RLock()
		gate := projects.engaging[name]
		waiting := gate != nil && gate.users > 1
		projects.mu.RUnlock()
		if waiting {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("replacement never reached engagement gate")
}

func setupProjects(t *testing.T) *Projects {
	t.Helper()
	return newProjects(
		func(_ context.Context, name multicluster.ClusterName) (controller.ProjectIdentity, error) {
			return controller.ProjectIdentity{AuthorityID: testAuthority, RouteName: string(name), UID: types.UID(name)}, nil
		},
		func(controller.ProjectIdentity) ProjectVerifier {
			return ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return nil })
		},
		func(binding ProjectBinding, reader client.Reader, verifier ProjectVerifier) (*ProjectRuntime, error) {
			return &ProjectRuntime{Binding: binding, Reader: reader, Verifier: verifier}, nil
		},
	)
}

func TestProjectEngagementSerializesDownstreamSetupAndCleanup(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancellation"} {
		for _, route := range []string{"direct", "dependency"} {
			t.Run(outcome+"/"+route, func(t *testing.T) {
				consumer := newConsumer(t, "east", mcMachine("m", "machine", "general"))
				first, second := consumer.NewCluster(), newConsumer(t, "east").NewCluster()
				setupEntered, setupRelease := make(chan struct{}), make(chan struct{})
				setupApplied := make(chan struct{})
				handlerEntered, handlerRelease := make(chan struct{}), make(chan struct{})
				var setupOnce, handlerOnce sync.Once
				releaseSetup := func() { setupOnce.Do(func() { close(setupRelease) }) }
				releaseHandler := func() { handlerOnce.Do(func() { close(handlerRelease) }) }
				defer releaseSetup()
				defer releaseHandler()
				projects := setupProjects(t)
				var builds atomic.Int32
				projects.build = func(binding ProjectBinding, reader client.Reader, verifier ProjectVerifier) (*ProjectRuntime, error) {
					initial := builds.Add(1) == 1
					handler := HandlerFunc(func(context.Context, ProjectRequest) Outcome {
						if initial {
							close(handlerEntered)
							<-handlerRelease
						}
						return Outcome{}
					})
					return &ProjectRuntime{Binding: binding, Reader: reader, Verifier: verifier,
						Mappers: Mappers{Reader: consumer.Client, Project: binding.Project, ClusterName: binding.ClusterName},
						Machine: NewLifecycleOwner(handler, BlockedHandler, nil)}, nil
				}
				setupError := errors.New("partial downstream setup failed")
				var mutex sync.Mutex
				var downstream cluster.Cluster
				var firstContext context.Context
				projects.bindAware(engagementAware(func(ctx context.Context, name multicluster.ClusterName, current cluster.Cluster) error {
					if name != "east" {
						return nil
					}
					if current == first {
						firstContext = ctx
						close(setupEntered)
						<-setupRelease
					}
					mutex.Lock()
					downstream = current
					mutex.Unlock()
					if current == first {
						close(setupApplied)
					}
					if current == first && outcome == "failure" {
						return setupError
					}
					return nil
				}))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				oldContext, cancelOld := context.WithCancel(ctx)
				defer cancelOld()
				firstDone, secondDone := make(chan error, 1), make(chan error, 1)
				go func() { firstDone <- projects.Engage(oldContext, "east", first) }()
				awaitEngagement(t, setupEntered)
				previous := projects.Get("east")
				request := ProjectRequest{ClusterName: "east", AuthorityID: testAuthority, ProjectUID: "east", Namespace: "default", Name: "m", UID: "machine"}
				selectOwner := func(current *ProjectRuntime) *LifecycleOwner { return current.Machine }
				reconciled := make(chan error, 1)
				go func() {
					if route == "dependency" {
						_, err := (ClusterRouteReconciler{Projects: projects, Owner: selectOwner}).Reconcile(ctx, RouteRequest{ClusterName: "east", AuthorityID: testAuthority, ProjectUID: "east", Kind: RouteProfile, Name: "general", UID: "profile", Deleted: true})
						reconciled <- err
					} else {
						_, err := (ClusterProjectReconciler{Projects: projects, Owner: selectOwner}).Reconcile(ctx, request)
						reconciled <- err
					}
				}()
				awaitEngagement(t, handlerEntered)
				go func() { secondDone <- projects.Engage(ctx, "east", second) }()
				awaitEngagementWaiter(t, projects, "east")
				otherDone := make(chan error, 1)
				other := newConsumer(t, "west").NewCluster()
				go func() { otherDone <- projects.Engage(ctx, "west", other) }()
				if err := awaitEngagement(t, otherDone); err != nil {
					t.Fatal(err)
				}
				if projects.Get("east") != previous || projects.ForProject(testAuthority, "east") != previous {
					t.Fatal("replacement published while downstream setup was unfinished")
				}
				if outcome == "cancellation" {
					cancelOld()
					awaitDrainStarted(t, previous, secondDone)
				}
				releaseSetup()
				awaitEngagement(t, setupApplied)
				awaitDrainStarted(t, previous, secondDone)
				mutex.Lock()
				observed := downstream
				mutex.Unlock()
				if observed != first || projects.Get("east") != previous {
					t.Fatal("successor setup overtook predecessor drain")
				}
				releaseHandler()
				if err := awaitEngagement(t, reconciled); err != nil {
					t.Fatal(err)
				}
				err := awaitEngagement(t, firstDone)
				if outcome == "success" && err != nil || outcome == "failure" && !errors.Is(err, setupError) || outcome == "cancellation" && !errors.Is(err, context.Canceled) {
					t.Fatalf("unexpected first engagement result: %v", err)
				}
				if err := awaitEngagement(t, secondDone); err != nil {
					t.Fatal(err)
				}
				successor := projects.Get("east")
				projects.unbind("east", previous)
				mutex.Lock()
				observed = downstream
				mutex.Unlock()
				if successor == nil || successor == previous || projects.Get("east") != successor || projects.ForProject(testAuthority, "east") != successor || observed != second || firstContext.Err() == nil {
					t.Fatal("registry and downstream ownership diverged or stale cleanup removed successor")
				}
				if previous.acquire() {
					previous.reconcileMu.RUnlock()
					t.Fatal("superseded runtime admitted work")
				}
				if !successor.acquire() {
					t.Fatal("stale cleanup stopped successor")
				}
				successor.reconcileMu.RUnlock()
			})
		}
	}
}

func TestProjectEngagementCancelledWaiterDoesNotDisturbSetup(t *testing.T) {
	projects := setupProjects(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	projects.bindAware(engagementAware(func(context.Context, multicluster.ClusterName, cluster.Cluster) error {
		close(entered)
		<-release
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	consumer := newConsumer(t, "east")
	go func() { first <- projects.Engage(ctx, "east", consumer.NewCluster()) }()
	awaitEngagement(t, entered)
	previous := projects.Get("east")
	waiting, cancelWaiting := context.WithCancel(ctx)
	defer cancelWaiting()
	second := make(chan error, 1)
	go func() { second <- projects.Engage(waiting, "east", consumer.NewCluster()) }()
	awaitEngagementWaiter(t, projects, "east")
	cancelWaiting()
	if err := awaitEngagement(t, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation: %v", err)
	}
	if projects.Get("east") != previous || !previous.acquire() {
		t.Fatal("cancelled waiter changed predecessor ownership or stopped it")
	}
	previous.reconcileMu.RUnlock()
	unblock()
	if err := awaitEngagement(t, first); err != nil {
		t.Fatal(err)
	}
	projects.mu.RLock()
	defer projects.mu.RUnlock()
	if len(projects.engaging) != 0 {
		t.Fatal("completed and cancelled calls leaked their engagement gate")
	}
}

func TestProjectEngagementReentrantSetupFailsWithoutDeadlock(t *testing.T) {
	projects := setupProjects(t)
	consumer := newConsumer(t, "east")
	projects.bindAware(engagementAware(func(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
		if projects.Get(name) == nil {
			return errors.New("runtime was not published before setup")
		}
		if name == "east" {
			return projects.Engage(ctx, "west", cl)
		}
		return projects.Engage(ctx, "east", cl)
	}))
	done := make(chan error, 1)
	go func() { done <- projects.Engage(context.Background(), "east", consumer.NewCluster()) }()
	if err := awaitEngagement(t, done); !errors.Is(err, ErrProjectEngagementReentrant) {
		t.Fatalf("recursive setup: %v", err)
	}
	if projects.Get("east") != nil || projects.Get("west") != nil {
		t.Fatal("failed recursive setup retained registrations")
	}
}

func TestProjectEngagementDepartureRetainsLifetimeUntilSetupReturns(t *testing.T) {
	projects := setupProjects(t)
	projects.resolve = func(_ context.Context, name multicluster.ClusterName) (controller.ProjectIdentity, error) {
		return controller.ProjectIdentity{AuthorityID: testAuthority, RouteName: string(name), UID: "shared"}, nil
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	projects.bindAware(engagementAware(func(_ context.Context, name multicluster.ClusterName, _ cluster.Cluster) error {
		if name == "east" {
			close(entered)
			<-release
		}
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer := newConsumer(t, "east")
	first := make(chan error, 1)
	go func() { first <- projects.Engage(ctx, "east", consumer.NewCluster()) }()
	awaitEngagement(t, entered)
	previous := projects.Get("east")
	departed := make(chan struct{})
	go func() { projects.unbind("east", previous); close(departed) }()
	awaitEngagement(t, previous.drain())
	if projects.ForProject(testAuthority, "shared") != previous {
		t.Fatal("drain released lifetime ownership before downstream setup finished")
	}
	if err := projects.Engage(ctx, "west", consumer.NewCluster()); !errors.Is(err, ErrProjectConflict) {
		t.Fatalf("lifetime transfer during incomplete setup: %v", err)
	}
	unblock()
	if err := awaitEngagement(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("departing setup reported success: %v", err)
	}
	awaitEngagement(t, departed)
	if err := projects.Engage(ctx, "west", consumer.NewCluster()); err != nil {
		t.Fatal(err)
	}
	successor := projects.Get("west")
	projects.unbind("east", previous)
	if successor == nil || projects.ForProject(testAuthority, "shared") != successor {
		t.Fatal("stale cleanup removed the transferred lifetime")
	}
}

func TestProjectEngagementSameClusterReplacesDownstreamContext(t *testing.T) {
	projects := setupProjects(t)
	var downstreamContext context.Context
	var setups int
	projects.bindAware(engagementAware(func(ctx context.Context, _ multicluster.ClusterName, _ cluster.Cluster) error {
		if downstreamContext != nil && downstreamContext.Err() == nil {
			return nil
		}
		downstreamContext = ctx
		setups++
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := newConsumer(t, "east").NewCluster()
	if err := projects.Engage(ctx, "east", current); err != nil {
		t.Fatal(err)
	}
	previous := projects.Get("east")
	previousContext := downstreamContext
	if err := projects.Engage(ctx, "east", current); err != nil {
		t.Fatal(err)
	}
	projects.unbind("east", previous)
	if setups != 2 || previousContext.Err() == nil || downstreamContext.Err() != nil {
		t.Fatal("same-cluster replacement retained obsolete downstream lifetime")
	}
}

func TestProjectEngagementConcurrentReentrantRoutesCannotDeadlock(t *testing.T) {
	projects := setupProjects(t)
	entered := make(chan struct{}, 2)
	nestedResults := make(chan error, 2)
	recurse, release := make(chan struct{}), make(chan struct{})
	var recurseOnce, releaseOnce sync.Once
	startRecursion := func() { recurseOnce.Do(func() { close(recurse) }) }
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer startRecursion()
	defer unblock()
	projects.bindAware(engagementAware(func(ctx context.Context, name multicluster.ClusterName, current cluster.Cluster) error {
		entered <- struct{}{}
		<-recurse
		other := multicluster.ClusterName("west")
		if name == other {
			other = "east"
		}
		err := projects.Engage(ctx, other, current)
		nestedResults <- err
		<-release
		return err
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := newConsumer(t, "east").NewCluster()
	finished := make(chan error, 2)
	for _, name := range []multicluster.ClusterName{"east", "west"} {
		go func() { finished <- projects.Engage(ctx, name, current) }()
	}
	for range 2 {
		awaitEngagement(t, entered)
	}
	startRecursion()
	for range 2 {
		if err := awaitEngagement(t, nestedResults); !errors.Is(err, ErrProjectEngagementReentrant) {
			t.Fatalf("nested setup waited on another active route: %v", err)
		}
	}
	unblock()
	for range 2 {
		if err := awaitEngagement(t, finished); !errors.Is(err, ErrProjectEngagementReentrant) {
			t.Fatalf("recursive setup result: %v", err)
		}
	}
	if projects.Get("east") != nil || projects.Get("west") != nil {
		t.Fatal("failed recursive setup retained registrations")
	}
}
