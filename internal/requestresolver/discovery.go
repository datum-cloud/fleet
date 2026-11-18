package requestresolver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
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

// ErrReadOnly is returned for any write the resolver's discovery clients
// attempt. Discovery is observational: the resolver never changes Services,
// ServiceConsumers, or anything else outside Machine status.
var ErrReadOnly = errors.New("request resolver discovery clients are read-only")

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

	rootClient   client.Reader
	serviceNames map[string]struct{}

	mu          sync.Mutex
	objectNames map[string]struct{}
}

// NewConsumerDiscovery wires the consumer provider. discoveryConfig reaches
// the root Milo API (Service list); projectConfig is the project kubeconfig
// template re-addressed to the provider project and, by the provider, to each
// engaged consumer project.
//
// The provider declares no ManagedResources, Teardowns, Suspends, or Resumes:
// the resolver creates nothing in consumer projects, so it never stamps a
// teardown finalizer, never deletes anything when a project leaves, and never
// gates a ServiceConsumer's deletion.
func NewConsumerDiscovery(discoveryConfig, projectConfig *rest.Config, csp ConsumerScopedProjectionConfig, scheme *runtime.Scheme) (*ConsumerDiscovery, error) {
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
	providerMgr, err := manager.New(providerConfig, manager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Cache:                  cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
		NewClient:              readOnlyNewClient,
		Controller:             config.Controller{SkipNameValidation: skipControllerNameValidation},
	})
	if err != nil {
		return nil, fmt.Errorf("build provider project manager: %w", err)
	}
	root, err := client.New(discoveryConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build root Milo client: %w", err)
	}
	rootReader := readOnlyClient{Client: root}
	provider, err := consumerprovider.New(providerMgr, consumerprovider.Options{
		RootClient:     rootReader,
		Scheme:         scheme,
		ServiceNames:   append([]string(nil), csp.ServiceNames...),
		ClusterOptions: []cluster.Option{func(o *cluster.Options) { o.Scheme = scheme }, ProjectCacheOptions},
	})
	if err != nil {
		return nil, fmt.Errorf("build consumer provider: %w", err)
	}
	names := make(map[string]struct{}, len(csp.ServiceNames))
	for _, name := range csp.ServiceNames {
		names[name] = struct{}{}
	}
	return &ConsumerDiscovery{Provider: provider, ProviderManager: providerMgr, rootClient: rootReader, serviceNames: names}, nil
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
	if err := d.ProviderManager.GetClient().List(ctx, &consumers); err != nil {
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

// serviceObjectNames resolves canonical service names to Service object
// names (ServiceConsumer.spec.serviceRef names the object). The mapping is
// cached after the first successful root read.
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
		if _, ok := d.serviceNames[services.Items[index].Spec.ServiceName]; ok {
			names[services.Items[index].Name] = struct{}{}
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no Service is registered for the configured service names")
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
// project. The resolver pauses nothing, and Fleet's lifecycle does not honour
// suspension through this process, so writing that confirmation would be a
// false claim. The patch is dropped instead: the condition stays unconfirmed
// (unknown) rather than converted into a pause. Returning an error would
// instead abort the provider's reconcile before it disengages departed
// projects.
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
		log.FromContext(ctx).Info("not confirming ServiceConsumer suspension: the request resolver pauses nothing", "serviceConsumer", consumer.Name)
		return nil
	}
	return ErrReadOnly
}
func (readOnlySubResource) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return ErrReadOnly
}
