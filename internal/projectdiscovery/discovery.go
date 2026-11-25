package projectdiscovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
	consumerprovider "go.miloapis.com/service-catalog/pkg/multicluster-runtime/consumer"
)

// ErrReadOnly is returned for any write the discovery clients attempt.
// Discovery is observational: the manager never changes Services or
// ServiceConsumers.
var ErrReadOnly = errors.New("fleet-manager discovery clients are read-only")

// ProjectCacheOptions is applied to every engaged project cache. Like the
// fleet-manager's routed project cache it is event-only: a nil SyncPeriod
// would silently enable controller-runtime's ten-hour resync.
func ProjectCacheOptions(options *cluster.Options) {
	zero := time.Duration(0)
	options.Cache.SyncPeriod = &zero
	options.Cache.DefaultTransform = cache.TransformStripManagedFields()
}

// skipControllerNameValidation lets tests build more than one provider
// manager per process; controller-runtime's name registry is global.
var skipControllerNameValidation *bool

// ConsumerDiscovery engages exactly the projects with an Active metal
// ServiceConsumer in the provider project, using the service catalog's
// multicluster-runtime consumer provider.
type ConsumerDiscovery struct {
	// Provider is the multicluster provider to pass to NewManager.
	Provider *consumerprovider.Provider
	// ProviderManager watches ServiceConsumers in the provider project. It is
	// not leader-elected: every replica must engage projects. Start it next
	// to the multicluster manager.
	ProviderManager manager.Manager

	rootClient client.Reader
	// consumers reads ServiceConsumers in the provider project (the provider
	// manager's cache).
	consumers       client.Reader
	providerProject string
	serviceNames    map[string]struct{}

	mu          sync.Mutex
	objectNames map[string]struct{}
}

// NewConsumerDiscovery wires the consumer provider. discoveryConfig reaches
// the root Milo API (Service list); projectConfig is the project kubeconfig
// template re-addressed to the provider project and, by the provider, to each
// engaged consumer project.
//
// The provider declares no ManagedResources, Teardowns, Suspends, or Resumes.
// When a project leaves the consumer set the manager stops reconciling it and
// nothing else happens: no Fleet record is deleted, no custody is released,
// no status is rewritten, and no ServiceConsumer finalizer gates deletion.
// Retained custody for a departed project is left as it is and resumes if
// the project is engaged again.
//
// tune, if given, adjusts the provider-project manager's options (tests use
// it to relax controller-name validation).
func NewConsumerDiscovery(discoveryConfig, projectConfig *rest.Config, csp ConsumerScopedProjectionConfig, scheme *runtime.Scheme, tune ...func(*manager.Options)) (*ConsumerDiscovery, error) {
	if discoveryConfig == nil || projectConfig == nil || scheme == nil {
		return nil, errors.New("consumer discovery requires discovery and project configs and a scheme")
	}
	if err := csp.Validate(); err != nil {
		return nil, err
	}
	providerConfig, err := consumerprovider.ProjectRestConfig(projectConfig, csp.ProviderProject)
	if err != nil {
		return nil, fmt.Errorf("build provider project config: %w", err)
	}
	root, err := client.New(discoveryConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build root Milo client: %w", err)
	}
	rootReader := readOnlyClient{Client: root}
	names := make(map[string]struct{}, len(csp.ServiceNames))
	for _, name := range csp.ServiceNames {
		names[name] = struct{}{}
	}
	d := &ConsumerDiscovery{rootClient: rootReader, providerProject: csp.ProviderProject, serviceNames: names}
	options := manager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Cache:                  cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
		NewClient:              readOnlyNewClient,
		// The consumer provider watches every ServiceConsumer in the producer
		// project, for every service it produces, and filters by service only
		// inside its reconcile, which recomputes everything and ignores the
		// key. Each unrelated ServiceConsumer is then its own queue key and,
		// when an engagement fails, its own retry chain. ServiceConsumers carry
		// no service label and the API declares no selectable fields, so the
		// events are filtered here instead.
		NewCache: func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
			c, err := cache.New(config, opts)
			if err != nil {
				return nil, err
			}
			return &consumerEventCache{Cache: c, relevant: d.relevantConsumer}, nil
		},
		Controller: config.Controller{SkipNameValidation: skipControllerNameValidation},
	}
	for _, apply := range tune {
		apply(&options)
	}
	providerMgr, err := manager.New(providerConfig, options)
	if err != nil {
		return nil, fmt.Errorf("build provider project manager: %w", err)
	}
	provider, err := consumerprovider.New(providerMgr, consumerprovider.Options{
		RootClient:     rootReader,
		Scheme:         scheme,
		ServiceNames:   append([]string(nil), csp.ServiceNames...),
		ClusterOptions: []cluster.Option{func(o *cluster.Options) { o.Scheme = scheme }, ProjectCacheOptions},
	})
	if err != nil {
		return nil, fmt.Errorf("build consumer provider: %w", err)
	}
	d.Provider, d.ProviderManager, d.consumers = provider, providerMgr, providerMgr.GetClient()
	return d, nil
}

// Synced reports whether the provider project's ServiceConsumer cache has
// completed its initial list.
func (d *ConsumerDiscovery) Synced(ctx context.Context) error {
	if !d.ProviderManager.GetCache().WaitForCacheSync(ctx) {
		return errors.New("provider project ServiceConsumer cache has not completed its initial sync")
	}
	return nil
}

// Expected lists the projects with an Active, not-deleting ServiceConsumer for
// the metal service, using the same membership rule as the consumer provider.
func (d *ConsumerDiscovery) Expected(ctx context.Context) ([]multicluster.ClusterName, error) {
	objectNames, err := d.serviceObjectNames(ctx)
	if err != nil {
		return nil, err
	}
	var consumers servicesv1alpha1.ServiceConsumerList
	if err := d.consumers.List(ctx, &consumers); err != nil {
		return nil, fmt.Errorf("list ServiceConsumers: %w", err)
	}
	return ActiveConsumerProjects(objectNames, consumers.Items), nil
}

// ActiveConsumerProjects applies the consumer provider's engagement rule: a
// project is engaged while it has at least one Active, not-deleting
// ServiceConsumer whose serviceRef names one of serviceObjectNames.
func ActiveConsumerProjects(serviceObjectNames map[string]struct{}, consumers []servicesv1alpha1.ServiceConsumer) []multicluster.ClusterName {
	active := map[multicluster.ClusterName]struct{}{}
	for index := range consumers {
		consumer := &consumers[index]
		if _, ok := serviceObjectNames[consumer.Spec.ServiceRef.Name]; !ok {
			continue
		}
		if !consumer.DeletionTimestamp.IsZero() || consumer.Status.Phase != servicesv1alpha1.ConsumerPhaseActive || consumer.Spec.ConsumerProjectRef.Name == "" {
			continue
		}
		active[multicluster.ClusterName(consumer.Spec.ConsumerProjectRef.Name)] = struct{}{}
	}
	names := make([]multicluster.ClusterName, 0, len(active))
	for name := range active {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// ErrDiscoveryMisconfigured reports configuration that would make discovery
// engage nothing: no Service for the configured name, or a provider project
// that is not the Service's producer. Readiness keeps failing with it.
var ErrDiscoveryMisconfigured = errors.New("project discovery is misconfigured")

// serviceObjectNames resolves canonical service names to Service object
// names (ServiceConsumer.spec.serviceRef names the object). Each matching
// Service must be produced by the configured provider project: ServiceConsumers
// live in the producer project, so any other provider project would engage
// nothing while appearing healthy. The mapping is cached only once it is
// valid; until then every readiness probe re-reads and fails.
func (d *ConsumerDiscovery) serviceObjectNames(ctx context.Context) (map[string]struct{}, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.objectNames != nil {
		return d.objectNames, nil
	}
	var services servicesv1alpha1.ServiceList
	if err := d.rootClient.List(ctx, &services); err != nil {
		return nil, fmt.Errorf("list Services: %w", err)
	}
	names := map[string]struct{}{}
	for index := range services.Items {
		service := &services.Items[index]
		if _, ok := d.serviceNames[service.Spec.ServiceName]; !ok {
			continue
		}
		if producer := service.Spec.Owner.ProducerProjectRef.Name; producer != d.providerProject {
			return nil, fmt.Errorf("%w: Service %q (%s) is produced by project %q, but consumerScopedProjection.providerProject is %q", ErrDiscoveryMisconfigured, service.Name, service.Spec.ServiceName, producer, d.providerProject)
		}
		names[service.Name] = struct{}{}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: no Service is registered for the configured service names", ErrDiscoveryMisconfigured)
	}
	d.objectNames = names
	return names, nil
}

func readOnlyNewClient(config *rest.Config, options client.Options) (client.Client, error) {
	c, err := client.New(config, options)
	if err != nil {
		return nil, err
	}
	return readOnlyClient{Client: c}, nil
}

// readOnlyClient refuses every write. The single exception is the consumer
// provider's ServiceConsumer Paused-condition bookkeeping: with no Suspend or
// Resume hooks the library would still patch Paused=True ("this provider's
// resources for the consumer are paused") when the platform suspends a
// project. Fleet's lifecycle does not implement suspension, so writing that
// confirmation would be a false claim. The patch is dropped instead: the
// condition stays unconfirmed (unknown) rather than converted into a pause.
// Returning an error would instead abort the provider's reconcile before it
// disengages departed projects.
type readOnlyClient struct{ client.Client }

func (readOnlyClient) Create(context.Context, client.Object, ...client.CreateOption) error {
	return ErrReadOnly
}
func (readOnlyClient) Update(context.Context, client.Object, ...client.UpdateOption) error {
	return ErrReadOnly
}
func (readOnlyClient) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return ErrReadOnly
}
func (readOnlyClient) Apply(context.Context, runtime.ApplyConfiguration, ...client.ApplyOption) error {
	return ErrReadOnly
}
func (readOnlyClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return ErrReadOnly
}
func (readOnlyClient) DeleteAllOf(context.Context, client.Object, ...client.DeleteAllOfOption) error {
	return ErrReadOnly
}
func (c readOnlyClient) Status() client.SubResourceWriter { return readOnlySubResource{name: "status"} }
func (c readOnlyClient) SubResource(name string) client.SubResourceClient {
	return readOnlySubResource{reader: c.Client.SubResource(name), name: name}
}

type readOnlySubResource struct {
	reader client.SubResourceReader
	name   string
}

func (s readOnlySubResource) Get(ctx context.Context, obj, subResource client.Object, opts ...client.SubResourceGetOption) error {
	if s.reader == nil {
		return ErrReadOnly
	}
	return s.reader.Get(ctx, obj, subResource, opts...)
}
func (readOnlySubResource) Create(context.Context, client.Object, client.Object, ...client.SubResourceCreateOption) error {
	return ErrReadOnly
}
func (readOnlySubResource) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return ErrReadOnly
}
func (s readOnlySubResource) Patch(ctx context.Context, obj client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	if consumer, ok := obj.(*servicesv1alpha1.ServiceConsumer); ok && s.name == "status" {
		log.FromContext(ctx).Info("not confirming ServiceConsumer suspension: Fleet does not implement suspension", "serviceConsumer", consumer.Name)
		return nil
	}
	return ErrReadOnly
}
func (readOnlySubResource) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return ErrReadOnly
}

// relevantConsumer reports whether a ServiceConsumer event can change the
// engaged set: only consumers of the configured Service do. When the Service
// cannot be resolved yet the event is kept; the consumer provider still
// filters authoritatively, so this only ever removes redundant work.
func (d *ConsumerDiscovery) relevantConsumer(object any) bool {
	if tombstone, ok := object.(toolscache.DeletedFinalStateUnknown); ok {
		object = tombstone.Obj
	}
	consumer, ok := object.(*servicesv1alpha1.ServiceConsumer)
	if !ok {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), readinessProbeBudget)
	defer cancel()
	names, err := d.serviceObjectNames(ctx)
	if err != nil {
		return true
	}
	_, relevant := names[consumer.Spec.ServiceRef.Name]
	return relevant
}

// consumerEventCache delivers ServiceConsumer events only when relevant
// reports true. Reads, lists, and indexes are unchanged.
type consumerEventCache struct {
	cache.Cache
	relevant func(any) bool
}

func (c *consumerEventCache) GetInformer(ctx context.Context, object client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	informer, err := c.Cache.GetInformer(ctx, object, opts...)
	if err != nil {
		return nil, err
	}
	if _, ok := object.(*servicesv1alpha1.ServiceConsumer); !ok {
		return informer, nil
	}
	return filteredInformer{Informer: informer, relevant: c.relevant}, nil
}

func (c *consumerEventCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind, opts ...cache.InformerGetOption) (cache.Informer, error) {
	informer, err := c.Cache.GetInformerForKind(ctx, gvk, opts...)
	if err != nil {
		return nil, err
	}
	if gvk != servicesv1alpha1.GroupVersion.WithKind("ServiceConsumer") {
		return informer, nil
	}
	return filteredInformer{Informer: informer, relevant: c.relevant}, nil
}

// filteredInformer wraps every registered handler so irrelevant objects are
// never delivered. A relevant object that becomes irrelevant (or the reverse)
// is delivered as a delete (or add), so the engaged set is still recomputed.
type filteredInformer struct {
	cache.Informer
	relevant func(any) bool
}

func (i filteredInformer) wrap(handler toolscache.ResourceEventHandler) toolscache.ResourceEventHandler {
	return toolscache.FilteringResourceEventHandler{FilterFunc: i.relevant, Handler: handler}
}

func (i filteredInformer) AddEventHandler(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.Informer.AddEventHandler(i.wrap(handler))
}

func (i filteredInformer) AddEventHandlerWithResyncPeriod(handler toolscache.ResourceEventHandler, period time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.Informer.AddEventHandlerWithResyncPeriod(i.wrap(handler), period)
}

func (i filteredInformer) AddEventHandlerWithOptions(handler toolscache.ResourceEventHandler, options toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.Informer.AddEventHandlerWithOptions(i.wrap(handler), options)
}
