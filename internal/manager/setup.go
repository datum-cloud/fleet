package manager

import (
	"context"
	"fmt"
	"time"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

type Handlers struct {
	Machine               Handler
	Installation          Handler
	MachineProjector      Handler
	InstallationProjector Handler
}

// SetupOptions configures the manager's controllers. Projects are not part of
// the options: they arrive through discovery, one ProjectRuntime each.
type SetupOptions struct {
	PrivateNamespace string
	PrivateCluster   ctrlcluster.Cluster
	// HandlersFor builds each engaged project's handlers. Projects with no
	// production or synthetic integration get blocked lifecycle handlers and
	// the requested-hardware projector.
	HandlersFor HandlerFactory
	// ProviderProject, ProviderCluster, and ProviderVerifier configure the
	// synthetic cross-project staging provider. ProviderConsumer is the one
	// engaged consumer project whose Machines provider events wake.
	ProviderProject  controller.ProjectIdentity
	ProviderCluster  ctrlcluster.Cluster
	ProviderVerifier ProjectVerifier
	ProviderConsumer multicluster.ClusterName
	Clock            clock.WithDelayedExecution
}

// ProjectCacheOptions returns the mandatory event-only routed-project cache
// configuration. A nil SyncPeriod would silently enable controller-runtime's
// ten-hour resync.
func ProjectCacheOptions() cache.Options {
	zero := time.Duration(0)
	return cache.Options{SyncPeriod: &zero}
}

// PrivateCacheOptions confines the host cache to the service-owned namespace.
// RBAC remains the authorization boundary; this scope prevents accidental host
// API watches and keeps private records out of the routed project cache.
func PrivateCacheOptions(privateNamespace string) cache.Options {
	zero := time.Duration(0)
	return cache.Options{SyncPeriod: &zero, DefaultNamespaces: map[string]cache.Config{privateNamespace: {}}}
}

// NewProjects returns the engaged-project registry. resolve turns a cluster
// name into its Milo project identity; verifierFor returns the per-project
// verifier run before every reconcile. Pass its Aware method to
// projectdiscovery.NewManager, then call Setup.
func NewProjects(resolve ProjectResolver, verifierFor func(controller.ProjectIdentity) ProjectVerifier) *Projects {
	return newProjects(resolve, verifierFor, func(ProjectBinding, client.Reader, ProjectVerifier) (*ProjectRuntime, error) {
		return nil, ErrEventWiringUnavailable
	})
}

// Aware binds the registry to the multicluster manager and returns it as the
// Aware that discovery engages through.
func (p *Projects) Aware(mgr mcmanager.Manager) multicluster.Aware {
	p.bindAware(mgr)
	return p
}

var providerClustersOnly = []mcbuilder.EngageOptions{mcbuilder.WithEngageWithLocalCluster(false), mcbuilder.WithEngageWithProviderClusters(true)}

// Setup wires every Fleet controller over the engaged projects. Each request
// carries its cluster name and is reconciled only through that project's
// runtime, with that project's identity, client, and verifier.
func Setup(ctx context.Context, mgr mcmanager.Manager, projects *Projects, options SetupOptions) error {
	if mgr == nil || projects == nil || projects.resolve == nil || projects.verifierFor == nil || options.PrivateCluster == nil || options.PrivateNamespace == "" || options.HandlersFor == nil {
		return ErrEventWiringUnavailable
	}
	if err := RegisterProjectIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return err
	}
	if err := RegisterPrivateIndexes(ctx, options.PrivateCluster.GetFieldIndexer(), options.PrivateNamespace); err != nil {
		return err
	}
	local := mgr.GetLocalManager()
	if err := local.Add(options.PrivateCluster); err != nil {
		return err
	}
	if options.ProviderCluster != nil {
		if !options.ProviderProject.Valid() || options.ProviderVerifier == nil || options.ProviderConsumer == "" {
			return ErrEventWiringUnavailable
		}
		if err := local.Add(options.ProviderCluster); err != nil {
			return err
		}
	}
	deadlineClock := options.Clock
	if deadlineClock == nil {
		deadlineClock = clock.RealClock{}
	}
	claimDeadlineEvents := make(chan event.TypedGenericEvent[ProjectRequest], 1024)
	installationDeadlineEvents := make(chan event.TypedGenericEvent[ProjectRequest], 1024)
	managerCtx, stopDeadlineEvents := context.WithCancel(context.Background())
	if err := local.Add(ctrlmanager.RunnableFunc(func(ctx context.Context) error {
		defer stopDeadlineEvents()
		<-ctx.Done()
		return nil
	})); err != nil {
		stopDeadlineEvents()
		return err
	}
	claimDeadlines := NewDeadlineScheduler(deadlineClock, deadlineEmitter(managerCtx, claimDeadlineEvents))
	installationDeadlines := NewDeadlineScheduler(deadlineClock, deadlineEmitter(managerCtx, installationDeadlineEvents))
	if err := local.Add(claimDeadlines); err != nil {
		return err
	}
	if err := local.Add(installationDeadlines); err != nil {
		return err
	}
	privateReader := options.PrivateCluster.GetCache()
	projects.mu.Lock()
	projects.build = func(binding ProjectBinding, reader client.Reader, verifier ProjectVerifier) (*ProjectRuntime, error) {
		if reader == nil || verifier == nil || binding.Client == nil {
			return nil, ErrEventWiringUnavailable
		}
		if options.ProviderCluster != nil && binding.Project.SameLifetime(options.ProviderProject) {
			return nil, fmt.Errorf("%w: the synthetic provider project cannot also be a consumer", ErrEventWiringUnavailable)
		}
		handlers, err := options.HandlersFor(binding)
		if err != nil {
			return nil, err
		}
		if handlers.Machine == nil || handlers.Installation == nil || handlers.MachineProjector == nil || handlers.InstallationProjector == nil {
			return nil, ErrEventWiringUnavailable
		}
		return &ProjectRuntime{
			Binding:      binding,
			Reader:       reader,
			Verifier:     verifier,
			Mappers:      Mappers{Reader: reader, PrivateReader: privateReader, Project: binding.Project, PrivateNamespace: options.PrivateNamespace, ClusterName: binding.ClusterName},
			Machine:      NewLifecycleOwner(handlers.Machine, handlers.MachineProjector, claimDeadlines),
			Installation: NewLifecycleOwner(handlers.Installation, handlers.InstallationProjector, installationDeadlines),
		}, nil
	}
	projects.mu.Unlock()

	machineOwner := func(r *ProjectRuntime) *LifecycleOwner { return r.Machine }
	installationOwner := func(r *ProjectRuntime) *LifecycleOwner { return r.Installation }
	entitlement := &unstructured.Unstructured{}
	entitlement.SetGroupVersionKind(schema.GroupVersionKind{Group: "services.miloapis.com", Version: "v1alpha1", Kind: "ServiceEntitlement"})
	privateSecrets := options.PrivateCluster.GetCache()

	claim := mcbuilder.TypedControllerManagedBy[ProjectRequest](mgr).
		Named("fleet-machine-events").
		Watches(&fleetv1.Machine{}, projectHandler(projects, func(m Mappers) func(context.Context, client.Object) []ProjectRequest { return m.RequestFor }), watchOptions(PrimaryPredicate())...).
		WatchesRawSource(source.TypedKind(privateSecrets, client.Object(&corev1.Secret{}), typedMap(privateHandler(projects, options.PrivateNamespace, func(m Mappers) func(context.Context, client.Object) []ProjectRequest { return m.RequestForPrivate })), PrivateRecordPredicate())).
		WatchesRawSource(deadlineSource(claimDeadlineEvents))
	if err := claim.Complete(ClusterProjectReconciler{Projects: projects, Owner: machineOwner}); err != nil {
		return err
	}
	claimRoutes := mcbuilder.TypedControllerManagedBy[RouteRequest](mgr).
		Named("fleet-machine-routes").
		Watches(&fleetv1.MachineClass{}, routeHandler(projects, RouteClass, ""), watchOptions(DependencyPredicate())...).
		Watches(&fleetv1.MachineProfile{}, routeHandler(projects, RouteProfile, ""), watchOptions(DependencyPredicate())...).
		Watches(&fleetv1.Installation{}, routeHandler(projects, RouteInstallation, ""), watchOptions(PrimaryPredicate())...).
		Watches(&fleetv1.MachineCapacity{}, routeHandler(projects, RouteMachineCapacity, ""), watchOptions(DependencyPredicate())...).
		Watches(&corev1.Namespace{}, routeHandler(projects, RoutePrivateUID, IndexPrivateNamespaceUID), watchOptions(DependencyPredicate())...).
		Watches(&networkingv1.Network{}, routeHandler(projects, RouteNetwork, ""), watchOptions(DependencyPredicate())...).
		Watches(&networkingv1.NetworkInterfaceClaim{}, routeHandler(projects, RoutePrivateUID, IndexPrivateNICClaimUID), watchOptions(DependencyPredicate())...).
		Watches(&networkingv1.NetworkInterface{}, routeHandler(projects, RoutePrivateUID, IndexPrivateInterfaceUID), watchOptions(DependencyPredicate())...).
		Watches(&quotav1alpha1.ResourceClaim{}, routeHandler(projects, RouteQuota, ""), watchOptions(DependencyPredicate())...).
		Watches(entitlement, routeHandler(projects, RouteEntitlement, ""), watchOptions(DependencyPredicate())...).
		Watches(&locationsv1alpha1.Location{}, routeHandler(projects, RouteLocation, ""), watchOptions(DependencyPredicate())...).
		Watches(&servicesv1alpha1.ServiceAvailability{}, routeHandler(projects, RouteAvailability, ""), watchOptions(DependencyPredicate())...)
	if err := claimRoutes.Complete(ClusterRouteReconciler{Projects: projects, Owner: machineOwner}); err != nil {
		return err
	}
	if options.ProviderCluster != nil {
		providerEvents := mcbuilder.TypedControllerManagedBy[ProviderEventRequest](mgr).
			Named("fleet-provider-machinecapacity-events").
			WatchesRawSource(source.TypedKind(options.ProviderCluster.GetCache(), client.Object(&fleetv1.MachineCapacity{}), typedProviderEventMap(options.ProviderProject), DependencyPredicate()))
		if err := providerEvents.Complete(ClusterProviderEventReconciler{Projects: projects, Consumer: options.ProviderConsumer, Provider: options.ProviderProject, ProviderVerifier: options.ProviderVerifier}); err != nil {
			return err
		}
	}
	installation := mcbuilder.TypedControllerManagedBy[ProjectRequest](mgr).
		Named("fleet-installation-events").
		Watches(&fleetv1.Installation{}, projectHandler(projects, func(m Mappers) func(context.Context, client.Object) []ProjectRequest { return m.RequestFor }), watchOptions(PrimaryPredicate())...).
		WatchesRawSource(source.TypedKind(privateSecrets, client.Object(&corev1.Secret{}), typedMap(privateHandler(projects, options.PrivateNamespace, func(m Mappers) func(context.Context, client.Object) []ProjectRequest {
			return m.RequestForPrivateInstallation
		})), PrivateRecordPredicate())).
		WatchesRawSource(deadlineSource(installationDeadlineEvents))
	if err := installation.Complete(ClusterProjectReconciler{Projects: projects, Owner: installationOwner}); err != nil {
		return err
	}
	installationRoutes := mcbuilder.TypedControllerManagedBy[RouteRequest](mgr).
		Named("fleet-installation-routes").
		Watches(&fleetv1.Machine{}, routeHandler(projects, RouteMachine, ""), watchOptions(PrimaryPredicate())...)
	return installationRoutes.Complete(ClusterRouteReconciler{Projects: projects, Owner: installationOwner})
}

func watchOptions(predicates ...predicate.Predicate) []mcbuilder.WatchesOption {
	options := []mcbuilder.WatchesOption{mcbuilder.WithPredicates(predicates...)}
	for _, engage := range providerClustersOnly {
		options = append(options, engage)
	}
	return options
}

// projectHandler maps an object in one engaged project through that
// project's mappers. A project that is no longer engaged maps to nothing.
func projectHandler(projects *Projects, mapper func(Mappers) func(context.Context, client.Object) []ProjectRequest) mchandler.TypedEventHandlerFunc[client.Object, ProjectRequest] {
	return func(name multicluster.ClusterName, _ ctrlcluster.Cluster) handler.TypedEventHandler[client.Object, ProjectRequest] {
		return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []ProjectRequest {
			runtime := projects.Get(name)
			if runtime == nil {
				return nil
			}
			return mapper(runtime.Mappers)(ctx, object)
		})
	}
}

func routeHandler(projects *Projects, kind RouteKind, field string) mchandler.TypedEventHandlerFunc[client.Object, RouteRequest] {
	return func(name multicluster.ClusterName, _ ctrlcluster.Cluster) handler.TypedEventHandler[client.Object, RouteRequest] {
		return typedRouteMap(func(ctx context.Context, object client.Object) []RouteRequest {
			runtime := projects.Get(name)
			if runtime == nil {
				return nil
			}
			return runtime.Mappers.Route(kind, field)(ctx, object)
		})
	}
}

// privateHandler routes a private record by the exact consumer project
// lifetime it records. It reaches only the runtime engaged for that lifetime;
// records of a project that is not engaged map to nothing.
func privateHandler(projects *Projects, privateNamespace string, mapper func(Mappers) func(context.Context, client.Object) []ProjectRequest) func(context.Context, client.Object) []ProjectRequest {
	return func(ctx context.Context, object client.Object) []ProjectRequest {
		secret, ok := object.(*corev1.Secret)
		if !ok {
			return nil
		}
		route, ok := routePrivate(secret, privateNamespace)
		if !ok {
			return nil
		}
		runtime := projects.ForProject(route.ConsumerProject.AuthorityID, route.ConsumerProject.UID)
		if runtime == nil {
			return nil
		}
		return mapper(runtime.Mappers)(ctx, object)
	}
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
