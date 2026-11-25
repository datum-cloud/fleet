package manager

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"go.datum.net/fleet/internal/controller"
)

type engagementAware func(context.Context, multicluster.ClusterName, cluster.Cluster) error

func (aware engagementAware) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	return aware(ctx, name, cl)
}

func awaitEngagement[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("engagement barrier timed out")
		var zero T
		return zero
	}
}

func awaitDrainStarted(t *testing.T, previous *ProjectRuntime, engaged <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-engaged:
			t.Fatalf("engagement returned before the previous runtime drained: %v", err)
		default:
		}
		if !previous.reconcileMu.TryRLock() {
			return
		}
		previous.reconcileMu.RUnlock()
		runtime.Gosched()
	}
	t.Fatal("engagement never started draining the previous runtime")
}

func TestProjectReengagementDrainsReconciliation(t *testing.T) {
	for _, routed := range []bool{false, true} {
		name := "direct"
		if routed {
			name = "dependency route"
		}
		for _, cancelled := range []bool{false, true} {
			variant := "/replacement"
			if cancelled {
				variant = "/cancelled replacement"
			}
			t.Run(name+variant, func(t *testing.T) {
				identity := controller.ProjectIdentity{AuthorityID: testAuthority, RouteName: "east", UID: "east-project"}
				consumer := newConsumer(t, "east", mcMachine("m", "machine", "general"))
				started, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				var builds, calls, engagements atomic.Int32
				projects := newProjects(
					func(context.Context, multicluster.ClusterName) (controller.ProjectIdentity, error) {
						return identity, nil
					},
					func(controller.ProjectIdentity) ProjectVerifier {
						return ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return nil })
					},
					func(binding ProjectBinding, reader client.Reader, verifier ProjectVerifier) (*ProjectRuntime, error) {
						first := builds.Add(1) == 1
						handler := HandlerFunc(func(context.Context, ProjectRequest) Outcome {
							calls.Add(1)
							if first {
								close(started)
								<-release
							}
							return Outcome{}
						})
						return &ProjectRuntime{Binding: binding, Reader: reader, Verifier: verifier,
							Mappers: Mappers{Reader: consumer.Client, Project: identity, ClusterName: "east"},
							Machine: NewLifecycleOwner(handler, BlockedHandler, nil)}, nil
					},
				)
				projects.bindAware(engagementAware(func(context.Context, multicluster.ClusterName, cluster.Cluster) error {
					engagements.Add(1)
					return nil
				}))
				oldContext, cancelOld := context.WithCancel(context.Background())
				defer cancelOld()
				if err := projects.Engage(oldContext, "east", consumer.NewCluster()); err != nil {
					t.Fatal(err)
				}
				previous := projects.Get("east")
				selectOwner := func(current *ProjectRuntime) *LifecycleOwner { return current.Machine }
				reconcile := func() error {
					if routed {
						_, err := (ClusterRouteReconciler{Projects: projects, Owner: selectOwner}).Reconcile(context.Background(), RouteRequest{ClusterName: "east", AuthorityID: identity.AuthorityID, ProjectUID: identity.UID, Kind: RouteProfile, Name: "general", UID: "profile", Deleted: true})
						return err
					}
					_, err := (ClusterProjectReconciler{Projects: projects, Owner: selectOwner}).Reconcile(context.Background(), ProjectRequest{ClusterName: "east", AuthorityID: identity.AuthorityID, ProjectUID: identity.UID, Namespace: "default", Name: "m", UID: "machine"})
					return err
				}
				done := make(chan error, 1)
				go func() { done <- reconcile() }()
				awaitEngagement(t, started)
				newContext, cancelNew := context.WithCancel(context.Background())
				defer cancelNew()
				engaged := make(chan error, 1)
				go func() { engaged <- projects.Engage(newContext, "east", consumer.NewCluster()) }()
				awaitDrainStarted(t, previous, engaged)
				observed := make(chan bool, 1)
				go func() {
					observed <- projects.Get("east") == previous && projects.ForProject(identity.AuthorityID, identity.UID) == previous
				}()
				if !awaitEngagement(t, observed) || engagements.Load() != 1 {
					t.Fatal("replacement was published or engaged before draining")
				}
				if cancelled {
					cancelNew()
					if err := awaitEngagement(t, engaged); !errors.Is(err, context.Canceled) {
						t.Fatalf("cancelled replacement did not return cancellation: %v", err)
					}
					if engagements.Load() != 1 || calls.Load() != 1 {
						t.Fatal("cancelled replacement admitted work")
					}
				} else {
					cancelOld()
				}
				unblock()
				if err := awaitEngagement(t, done); err != nil {
					t.Fatal(err)
				}
				if cancelled {
					deadline := time.Now().Add(5 * time.Second)
					for projects.Get("east") == previous && time.Now().Before(deadline) {
						runtime.Gosched()
					}
					if projects.Get("east") != nil || projects.ForProject(identity.AuthorityID, identity.UID) != nil {
						t.Fatal("cancelled replacement left a retired runtime registered")
					}
					retryContext, cancelRetry := context.WithCancel(context.Background())
					defer cancelRetry()
					if err := projects.Engage(retryContext, "east", consumer.NewCluster()); err != nil {
						t.Fatal(err)
					}
				} else if err := awaitEngagement(t, engaged); err != nil {
					t.Fatal(err)
				}
				successor := projects.Get("east")
				projects.unbind("east", previous)
				if successor == nil || successor == previous || projects.Get("east") != successor || projects.ForProject(identity.AuthorityID, identity.UID) != successor {
					t.Fatal("stale departure removed successor ownership")
				}
				if previous.acquire() {
					previous.reconcileMu.RUnlock()
					t.Fatal("stale runtime pointer admitted work")
				}
				if err := reconcile(); err != nil || calls.Load() != 2 {
					t.Fatalf("successor reconcile: error=%v calls=%d", err, calls.Load())
				}
			})
		}
	}
}

func TestProjectReengagementRevalidatesRegistry(t *testing.T) {
	for _, name := range []string{"concurrent successor", "concurrent project owner", "cancelled after drain"} {
		conflict := name == "concurrent project owner"
		cancelled := name == "cancelled after drain"
		t.Run(name, func(t *testing.T) {
			identity := controller.ProjectIdentity{AuthorityID: testAuthority, RouteName: "east", UID: "new-project"}
			oldIdentity := identity
			oldIdentity.UID = "old-project"
			previous := &ProjectRuntime{Binding: ProjectBinding{ClusterName: "east", Project: oldIdentity}}
			projects := newProjects(
				func(context.Context, multicluster.ClusterName) (controller.ProjectIdentity, error) {
					return identity, nil
				},
				func(controller.ProjectIdentity) ProjectVerifier { return nil },
				func(binding ProjectBinding, reader client.Reader, verifier ProjectVerifier) (*ProjectRuntime, error) {
					return &ProjectRuntime{Binding: binding}, nil
				},
			)
			projects.bindAware(engagementAware(func(context.Context, multicluster.ClusterName, cluster.Cluster) error { return nil }))
			projects.byCluster["east"] = previous
			oldKey := projectKey{oldIdentity.AuthorityID, oldIdentity.UID}
			key := projectKey{identity.AuthorityID, identity.UID}
			projects.byProject[oldKey] = "east"
			previous.acquire()
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(previous.reconcileMu.RUnlock) }
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			consumer := newConsumer(t, "east")
			engaged := make(chan error, 1)
			go func() { engaged <- projects.Engage(ctx, "east", consumer.NewCluster()) }()
			awaitDrainStarted(t, previous, engaged)
			competing := &ProjectRuntime{Binding: ProjectBinding{ClusterName: "east", Project: identity}}
			competing.acquire()
			var competingReleaseOnce sync.Once
			releaseCompeting := func() { competingReleaseOnce.Do(competing.reconcileMu.RUnlock) }
			defer releaseCompeting()
			projects.mu.Lock()
			release()
			<-previous.drain()
			if cancelled {
				cancel()
			} else if conflict {
				competing.Binding.ClusterName = "west"
				competing.Binding.Project.RouteName = "west"
				projects.byCluster["west"] = competing
				projects.byProject[key] = "west"
			} else {
				projects.byCluster["east"] = competing
				delete(projects.byProject, oldKey)
				projects.byProject[key] = "east"
			}
			projects.mu.Unlock()
			if cancelled {
				if err := awaitEngagement(t, engaged); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled drained replacement returned: %v", err)
				}
				projects.unbind("east", previous)
				if projects.Get("east") != nil || projects.ForProject(identity.AuthorityID, identity.UID) != nil {
					t.Fatal("cancelled drained replacement was published")
				}
			} else if conflict {
				if err := awaitEngagement(t, engaged); !errors.Is(err, ErrProjectConflict) {
					t.Fatalf("concurrent project owner was overwritten: %v", err)
				}
				if projects.ForProject(identity.AuthorityID, identity.UID) != competing {
					t.Fatal("conflicting project ownership changed")
				}
			} else {
				awaitDrainStarted(t, competing, engaged)
				if projects.Get("east") != competing {
					t.Fatal("concurrent successor replaced without draining")
				}
				releaseCompeting()
				if err := awaitEngagement(t, engaged); err != nil {
					t.Fatal(err)
				}
				current := projects.Get("east")
				projects.unbind("east", previous)
				projects.unbind("east", competing)
				if current == competing || current == previous || projects.Get("east") != current || projects.ForProject(identity.AuthorityID, identity.UID) != current || projects.ForProject(oldIdentity.AuthorityID, oldIdentity.UID) != nil {
					t.Fatal("stale unbind corrupted replacement registries")
				}
			}
		})
	}
}

func TestConcurrentProjectReengagementAndDeparture(t *testing.T) {
	identity := controller.ProjectIdentity{AuthorityID: testAuthority, RouteName: "east", UID: "east-project"}
	previous := &ProjectRuntime{Binding: ProjectBinding{ClusterName: "east", Project: identity}}
	const contenders = 8
	built := make(chan *ProjectRuntime, contenders)
	projects := newProjects(
		func(context.Context, multicluster.ClusterName) (controller.ProjectIdentity, error) {
			return identity, nil
		},
		func(controller.ProjectIdentity) ProjectVerifier { return nil },
		func(binding ProjectBinding, reader client.Reader, verifier ProjectVerifier) (*ProjectRuntime, error) {
			candidate := &ProjectRuntime{Binding: binding}
			built <- candidate
			return candidate, nil
		},
	)
	projects.bindAware(engagementAware(func(context.Context, multicluster.ClusterName, cluster.Cluster) error { return nil }))
	projects.byCluster["east"] = previous
	projects.byProject[projectKey{identity.AuthorityID, identity.UID}] = "east"
	previous.acquire()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(previous.reconcileMu.RUnlock) }
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer := newConsumer(t, "east")
	engaged := make(chan error, contenders)
	for range contenders {
		go func() { engaged <- projects.Engage(ctx, "east", consumer.NewCluster()) }()
	}
	candidates := make([]*ProjectRuntime, 0, contenders)
	for range contenders {
		candidates = append(candidates, awaitEngagement(t, built))
	}
	awaitDrainStarted(t, previous, engaged)
	departed := make(chan struct{})
	go func() { projects.unbind("east", previous); close(departed) }()
	release()
	for range contenders {
		if err := awaitEngagement(t, engaged); err != nil {
			t.Fatal(err)
		}
	}
	awaitEngagement(t, departed)
	current := projects.Get("east")
	if current == nil || current == previous || projects.ForProject(identity.AuthorityID, identity.UID) != current {
		t.Fatal("concurrent engagements lost registry ownership")
	}
	for _, candidate := range candidates {
		if candidate == current {
			continue
		}
		if candidate.acquire() {
			candidate.reconcileMu.RUnlock()
			t.Fatal("superseded candidate still admits reconciliation")
		}
		projects.unbind("east", candidate)
	}
	if projects.Get("east") != current || !current.acquire() {
		t.Fatal("stale departure stopped the final successor")
	}
	current.reconcileMu.RUnlock()
}
