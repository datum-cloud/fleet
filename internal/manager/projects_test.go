package manager

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"go.datum.net/fleet/internal/controller"
)

func TestProjectDepartureDrainsReconciliation(t *testing.T) {
	for _, path := range []string{"direct", "dependency route", "provider event"} {
		t.Run(path, func(t *testing.T) {
			identity := controller.ProjectIdentity{AuthorityID: testAuthority, RouteName: "east", UID: "east-project"}
			consumer := newConsumer(t, "east", mcMachine("m", "machine", "general"))
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			owner := NewLifecycleOwner(HandlerFunc(func(context.Context, ProjectRequest) Outcome {
				if calls.Add(1) == 1 {
					close(started)
					<-release
				}
				return Outcome{}
			}), BlockedHandler, nil)
			runtime := &ProjectRuntime{
				Binding:  ProjectBinding{ClusterName: "east", Project: identity},
				Reader:   consumer.Client,
				Verifier: ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return nil }),
				Mappers:  Mappers{Reader: consumer.Client, Project: identity, ClusterName: "east"},
				Machine:  owner,
			}
			projects := newProjects(nil, nil, nil)
			projects.byCluster["east"] = runtime
			projects.byProject[projectKey{identity.AuthorityID, identity.UID}] = multicluster.ClusterName("east")
			selectOwner := func(runtime *ProjectRuntime) *LifecycleOwner { return runtime.Machine }
			reconcile := func() error {
				switch path {
				case "dependency route":
					_, err := (ClusterRouteReconciler{Projects: projects, Owner: selectOwner}).Reconcile(context.Background(), RouteRequest{ClusterName: "east", AuthorityID: identity.AuthorityID, ProjectUID: identity.UID, Kind: RouteProfile, Name: "general", UID: "profile", Deleted: true})
					return err
				case "provider event":
					provider := controller.ProjectIdentity{AuthorityID: testAuthority, RouteName: "datum-cloud", UID: "provider-project"}
					_, err := (ClusterProviderEventReconciler{Projects: projects, Consumer: "east", Provider: provider, ProviderVerifier: runtime.Verifier}).Reconcile(context.Background(), ProviderEventRequest{AuthorityID: provider.AuthorityID, ProjectUID: provider.UID, Namespace: "default", Name: "capacity", UID: "capacity-uid"})
					return err
				}
				_, err := (ClusterProjectReconciler{Projects: projects, Owner: selectOwner}).Reconcile(context.Background(), ProjectRequest{ClusterName: "east", AuthorityID: identity.AuthorityID, ProjectUID: identity.UID, Namespace: "default", Name: "m", UID: "machine"})
				return err
			}
			done := make(chan error, 1)
			go func() { done <- reconcile() }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not start")
			}
			departed := make(chan struct{})
			go func() { projects.unbind("east", runtime); close(departed) }()
			select {
			case <-departed:
				t.Fatal("departure completed while a handler could still write")
			case <-time.After(50 * time.Millisecond):
			}
			if projects.Get("east") != runtime {
				t.Fatal("runtime removed before reconciliation drained")
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reconciliation did not drain")
			}
			select {
			case <-departed:
			case <-time.After(5 * time.Second):
				t.Fatal("departure did not finish")
			}
			if projects.Get("east") != nil || projects.ForProject(identity.AuthorityID, identity.UID) != nil {
				t.Fatal("departed runtime still registered")
			}
			if err := reconcile(); err != nil || calls.Load() != 1 {
				t.Fatalf("post-departure reconcile: error=%v calls=%d", err, calls.Load())
			}
			if runtime.acquire() {
				runtime.reconcileMu.RUnlock()
				t.Fatal("a previously resolved runtime admitted work after departure")
			}
		})
	}
}
