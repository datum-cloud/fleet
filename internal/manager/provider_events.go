package manager

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

// ProviderEventRequest retains the exact provider Machine lifetime so a
// provider-side availability transition can wake consumer claims without a
// polling resync.
type ProviderEventRequest struct {
	AuthorityID string
	ProjectUID  types.UID
	Namespace   string
	Name        string
	UID         types.UID
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
	var claims fleetv1.MachineClaimList
	if err := r.Reader.List(ctx, &claims); err != nil {
		return reconcile.Result{}, err
	}
	for i := range claims.Items {
		claim := &claims.Items[i]
		if claim.UID == "" {
			continue
		}
		outcome := r.Handler.ReconcileEvent(ctx, ProjectRequest{AuthorityID: r.Consumer.AuthorityID, ProjectUID: r.Consumer.UID, Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID})
		if outcome.RetryError != nil {
			return reconcile.Result{}, outcome.RetryError
		}
	}
	return reconcile.Result{}, nil
}
