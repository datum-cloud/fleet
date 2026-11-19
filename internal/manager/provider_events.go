package manager

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

// ProviderEventRequest retains the exact provider MachineCapacity lifetime so a
// provider-side availability transition can wake consumer Machines without a
// polling resync.
type ProviderEventRequest struct {
	ClusterName multicluster.ClusterName
	AuthorityID string
	ProjectUID  types.UID
	Namespace   string
	Name        string
	UID         types.UID
}

func (r ProviderEventRequest) Cluster() multicluster.ClusterName { return r.ClusterName }

func (r ProviderEventRequest) WithCluster(name multicluster.ClusterName) ProviderEventRequest {
	r.ClusterName = name
	return r
}

func (r ProviderEventRequest) String() string {
	return fmt.Sprintf("provider://%s/%s/%s/%s", r.ProjectUID, r.Namespace, r.Name, r.UID)
}

func (r ProviderEventRequest) valid() bool {
	return r.AuthorityID != "" && r.ProjectUID != "" && r.Namespace != "" && r.Name != "" && r.UID != ""
}

func typedProviderEventMap(project controller.ProjectIdentity) handler.TypedEventHandler[client.Object, ProviderEventRequest] {
	mapObject := func(object client.Object) []ProviderEventRequest {
		if object == nil || !project.Valid() || object.GetNamespace() == "" || object.GetName() == "" || object.GetUID() == "" {
			return nil
		}
		return []ProviderEventRequest{{AuthorityID: project.AuthorityID, ProjectUID: project.UID, Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}}
	}
	enqueue := func(requests []ProviderEventRequest, queue workqueue.TypedRateLimitingInterface[ProviderEventRequest]) {
		for _, request := range requests {
			queue.Add(request)
		}
	}
	return handler.TypedFuncs[client.Object, ProviderEventRequest]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[ProviderEventRequest]) {
			enqueue(mapObject(e.Object), q)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[ProviderEventRequest]) {
			seen := map[ProviderEventRequest]struct{}{}
			for _, request := range append(mapObject(e.ObjectOld), mapObject(e.ObjectNew)...) {
				seen[request] = struct{}{}
			}
			for request := range seen {
				q.Add(request)
			}
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[ProviderEventRequest]) {
			enqueue(mapObject(e.Object), q)
		},
		GenericFunc: func(_ context.Context, e event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[ProviderEventRequest]) {
			enqueue(mapObject(e.Object), q)
		},
	}
}

type ProviderEventReconciler struct {
	// ConsumerCluster is the engaged consumer project cluster stamped on the
	// requests this reconciler produces.
	ConsumerCluster  multicluster.ClusterName
	Consumer         controller.ProjectIdentity
	Provider         controller.ProjectIdentity
	ConsumerVerifier ProjectVerifier
	ProviderVerifier ProjectVerifier
	Reader           client.Reader
	Handler          Handler
}

func (r ProviderEventReconciler) Reconcile(ctx context.Context, request ProviderEventRequest) (reconcile.Result, error) {
	if !request.valid() || request.AuthorityID != r.Provider.AuthorityID || request.ProjectUID != r.Provider.UID {
		return reconcile.Result{}, nil
	}
	if r.Reader == nil || r.Handler == nil || r.ConsumerVerifier == nil || r.ProviderVerifier == nil || !r.Consumer.Valid() || !r.Provider.Valid() {
		return reconcile.Result{}, ErrEventWiringUnavailable
	}
	if err := r.ConsumerVerifier.VerifyProject(ctx, r.Consumer); err != nil {
		return reconcile.Result{}, err
	}
	if err := r.ProviderVerifier.VerifyProject(ctx, r.Provider); err != nil {
		return reconcile.Result{}, err
	}
	var machines fleetv1.MachineList
	if err := r.Reader.List(ctx, &machines); err != nil {
		return reconcile.Result{}, err
	}
	for i := range machines.Items {
		machine := &machines.Items[i]
		if machine.UID == "" {
			continue
		}
		outcome := r.Handler.ReconcileEvent(ctx, ProjectRequest{ClusterName: r.ConsumerCluster, AuthorityID: r.Consumer.AuthorityID, ProjectUID: r.Consumer.UID, Namespace: machine.Namespace, Name: machine.Name, UID: machine.UID})
		if outcome.RetryError != nil {
			return reconcile.Result{}, outcome.RetryError
		}
	}
	return reconcile.Result{}, nil
}

// ClusterProviderEventReconciler wakes the one configured consumer project's
// Machines on a provider MachineCapacity event, through that project's
// runtime. When the consumer is not engaged the event is dropped; its
// engagement replays its Machines.
type ClusterProviderEventReconciler struct {
	Projects         *Projects
	Consumer         multicluster.ClusterName
	Provider         controller.ProjectIdentity
	ProviderVerifier ProjectVerifier
}

func (r ClusterProviderEventReconciler) Reconcile(ctx context.Context, request ProviderEventRequest) (reconcile.Result, error) {
	if r.Projects == nil {
		return reconcile.Result{}, ErrEventWiringUnavailable
	}
	runtime := r.Projects.Get(r.Consumer)
	if runtime == nil {
		return reconcile.Result{}, nil
	}
	return ProviderEventReconciler{ConsumerCluster: r.Consumer, Consumer: runtime.Binding.Project, Provider: r.Provider, ConsumerVerifier: runtime.Verifier, ProviderVerifier: r.ProviderVerifier, Reader: runtime.Reader, Handler: runtime.Machine}.Reconcile(ctx, request)
}
