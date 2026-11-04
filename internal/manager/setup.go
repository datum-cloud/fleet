package manager

import (
	"context"
	"time"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
)

type Handlers struct {
	Claim                 Handler
	Installation          Handler
	ClaimProjector        Handler
	InstallationProjector Handler
}

type SetupOptions struct {
	Project          controller.ProjectIdentity
	PrivateNamespace string
	Handlers         Handlers
	ProjectVerifier  ProjectVerifier
	Clock            clock.WithDelayedExecution
}

// CacheOptions returns the mandatory event-only cache configuration. A nil
// SyncPeriod would silently enable controller-runtime's ten-hour resync.
func CacheOptions(privateNamespace string) cache.Options {
	zero := time.Duration(0)
	return cache.Options{
		SyncPeriod: &zero,
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Secret{}: {SyncPeriod: &zero, Namespaces: map[string]cache.Config{privateNamespace: {}}},
		},
	}
}

func Setup(ctx context.Context, mgr ctrlmanager.Manager, options SetupOptions) error {
	if mgr == nil || !options.Project.Valid() || options.PrivateNamespace == "" || options.ProjectVerifier == nil || options.Handlers.Claim == nil || options.Handlers.Installation == nil || options.Handlers.ClaimProjector == nil || options.Handlers.InstallationProjector == nil {
		return ErrEventWiringUnavailable
	}
	if err := RegisterIndexes(ctx, mgr.GetFieldIndexer(), options.PrivateNamespace); err != nil {
		return err
	}
	deadlineClock := options.Clock
	if deadlineClock == nil {
		deadlineClock = clock.RealClock{}
	}
	claimDeadlineEvents := make(chan event.TypedGenericEvent[ProjectRequest], 1024)
	installationDeadlineEvents := make(chan event.TypedGenericEvent[ProjectRequest], 1024)
	managerCtx, stopDeadlineEvents := context.WithCancel(context.Background())
	if err := mgr.Add(ctrlmanager.RunnableFunc(func(ctx context.Context) error {
		defer stopDeadlineEvents()
		<-ctx.Done()
		return nil
	})); err != nil {
		stopDeadlineEvents()
		return err
	}
	claimDeadlines := NewDeadlineScheduler(deadlineClock, deadlineEmitter(managerCtx, claimDeadlineEvents))
	installationDeadlines := NewDeadlineScheduler(deadlineClock, deadlineEmitter(managerCtx, installationDeadlineEvents))
	if err := mgr.Add(claimDeadlines); err != nil {
		return err
	}
	if err := mgr.Add(installationDeadlines); err != nil {
		return err
	}
	claimOwner := NewLifecycleOwner(options.Handlers.Claim, options.Handlers.ClaimProjector, claimDeadlines)
	installationOwner := NewLifecycleOwner(options.Handlers.Installation, options.Handlers.InstallationProjector, installationDeadlines)
	mappers := Mappers{Reader: mgr.GetCache(), Project: options.Project, PrivateNamespace: options.PrivateNamespace}
	entitlement := &unstructured.Unstructured{}
	entitlement.SetGroupVersionKind(schema.GroupVersionKind{Group: "services.miloapis.com", Version: "v1alpha1", Kind: "ServiceEntitlement"})
	claimMap := typedMap(mappers.RequestFor)
	claim := builder.TypedControllerManagedBy[ProjectRequest](mgr).
		Named("fleet-machineclaim-events").
		Watches(&fleetv1.MachineClaim{}, claimMap, builder.WithPredicates(PrimaryPredicate())).
		Watches(&corev1.Secret{}, typedMap(mappers.RequestForPrivate), builder.WithPredicates(PrivateRecordPredicate())).
		WatchesRawSource(deadlineSource(claimDeadlineEvents))
	if err := claim.Complete(ProjectReconciler{Project: options.Project, Handler: claimOwner, Verifier: options.ProjectVerifier}); err != nil {
		return err
	}
	claimRoutes := builder.TypedControllerManagedBy[RouteRequest](mgr).
		Named("fleet-machineclaim-routes").
		Watches(&fleetv1.MachineClass{}, typedRouteMap(mappers.Route(RouteClass, "")), builder.WithPredicates(DependencyPredicate())).
		Watches(&fleetv1.MachineProfile{}, typedRouteMap(mappers.Route(RouteProfile, "")), builder.WithPredicates(DependencyPredicate())).
		Watches(&fleetv1.Installation{}, typedRouteMap(mappers.Route(RouteInstallation, "")), builder.WithPredicates(PrimaryPredicate())).
		Watches(&fleetv1.Machine{}, typedRouteMap(mappers.Route(RouteMachine, "")), builder.WithPredicates(DependencyPredicate())).
		Watches(&corev1.Namespace{}, typedRouteMap(mappers.Route(RoutePrivateUID, IndexPrivateNamespaceUID)), builder.WithPredicates(DependencyPredicate())).
		Watches(&networkingv1.Network{}, typedRouteMap(mappers.Route(RouteNetwork, "")), builder.WithPredicates(DependencyPredicate())).
		Watches(&networkingv1.NetworkInterfaceClaim{}, typedRouteMap(mappers.Route(RoutePrivateUID, IndexPrivateNICClaimUID)), builder.WithPredicates(DependencyPredicate())).
		Watches(&networkingv1.NetworkInterface{}, typedRouteMap(mappers.Route(RoutePrivateUID, IndexPrivateInterfaceUID)), builder.WithPredicates(DependencyPredicate())).
		Watches(&quotav1alpha1.ResourceClaim{}, typedRouteMap(mappers.Route(RouteQuota, "")), builder.WithPredicates(DependencyPredicate())).
		Watches(entitlement, typedRouteMap(mappers.Route(RouteEntitlement, "")), builder.WithPredicates(DependencyPredicate()))
	if err := claimRoutes.Complete(RouteReconciler{Project: options.Project, Verifier: options.ProjectVerifier, Mappers: mappers, Handler: claimOwner}); err != nil {
		return err
	}
	installation := builder.TypedControllerManagedBy[ProjectRequest](mgr).
		Named("fleet-installation-events").
		Watches(&fleetv1.Installation{}, typedMap(mappers.RequestFor), builder.WithPredicates(PrimaryPredicate())).
		Watches(&corev1.Secret{}, typedMap(mappers.RequestForPrivateInstallation), builder.WithPredicates(PrivateRecordPredicate())).
		WatchesRawSource(deadlineSource(installationDeadlineEvents))
	if err := installation.Complete(ProjectReconciler{Project: options.Project, Handler: installationOwner, Verifier: options.ProjectVerifier}); err != nil {
		return err
	}
	installationRoutes := builder.TypedControllerManagedBy[RouteRequest](mgr).
		Named("fleet-installation-routes").
		Watches(&fleetv1.MachineClaim{}, typedRouteMap(mappers.Route(RouteClaim, "")), builder.WithPredicates(PrimaryPredicate()))
	return installationRoutes.Complete(RouteReconciler{Project: options.Project, Verifier: options.ProjectVerifier, Mappers: mappers, Handler: installationOwner})
}

func deadlineEmitter(ctx context.Context, events chan<- event.TypedGenericEvent[ProjectRequest]) func(ProjectRequest) {
	return func(request ProjectRequest) {
		select {
		case events <- event.TypedGenericEvent[ProjectRequest]{Object: request}:
		case <-ctx.Done():
		}
	}
}

func deadlineSource(events <-chan event.TypedGenericEvent[ProjectRequest]) source.TypedSource[ProjectRequest] {
	return source.TypedChannel[ProjectRequest, ProjectRequest](events, handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, request ProjectRequest) []ProjectRequest {
		if !request.Valid() {
			return nil
		}
		return []ProjectRequest{request}
	}))
}

func typedMap(fn func(context.Context, client.Object) []ProjectRequest) handler.TypedEventHandler[client.Object, ProjectRequest] {
	return handler.TypedEnqueueRequestsFromMapFunc(fn)
}

func typedRouteMap(fn func(context.Context, client.Object) []RouteRequest) handler.TypedEventHandler[client.Object, RouteRequest] {
	mapObject := func(ctx context.Context, object client.Object, deleted bool) []RouteRequest {
		requests := fn(ctx, object)
		for index := range requests {
			requests[index].Deleted = deleted
		}
		return requests
	}
	enqueue := func(requests []RouteRequest, queue workqueue.TypedRateLimitingInterface[RouteRequest]) {
		for _, request := range requests {
			queue.Add(request)
		}
	}
	return handler.TypedFuncs[client.Object, RouteRequest]{
		CreateFunc: func(ctx context.Context, event event.TypedCreateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[RouteRequest]) {
			enqueue(mapObject(ctx, event.Object, false), queue)
		},
		UpdateFunc: func(ctx context.Context, event event.TypedUpdateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[RouteRequest]) {
			unique := make(map[RouteRequest]struct{})
			for _, request := range append(mapObject(ctx, event.ObjectOld, false), mapObject(ctx, event.ObjectNew, false)...) {
				unique[request] = struct{}{}
			}
			for request := range unique {
				queue.Add(request)
			}
		},
		DeleteFunc: func(ctx context.Context, event event.TypedDeleteEvent[client.Object], queue workqueue.TypedRateLimitingInterface[RouteRequest]) {
			enqueue(mapObject(ctx, event.Object, true), queue)
		},
		GenericFunc: func(ctx context.Context, event event.TypedGenericEvent[client.Object], queue workqueue.TypedRateLimitingInterface[RouteRequest]) {
			enqueue(mapObject(ctx, event.Object, false), queue)
		},
	}
}
