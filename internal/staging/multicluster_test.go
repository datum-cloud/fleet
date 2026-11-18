package staging

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/multiclustertest"
	"go.datum.net/fleet/internal/projectdiscovery"
	"go.datum.net/fleet/internal/simulator"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

type safeExecutor struct {
	mu      sync.Mutex
	results map[string]simulator.OperationResult
}

func (e *safeExecutor) Execute(_ context.Context, request simulator.Request) (simulator.OperationResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now().UTC()
	result := simulator.OperationResult{ID: request.ID, Kind: request.Kind, Phase: "Succeeded", Result: "clean", CreatedAt: now, CompletedAt: &now}
	e.results[request.ID] = result
	return result, nil
}

func (e *safeExecutor) Lookup(_ context.Context, id string) (simulator.OperationResult, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	result, found := e.results[id]
	return result, found, nil
}

func stagingScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, fleetv1.AddToScheme, networkingv1.AddToScheme, locationsv1alpha1.AddToScheme, quotav1alpha1.AddToScheme, resourcemanagerv1alpha1.AddToScheme, servicesv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func indexes(t *testing.T, register func(context.Context, client.FieldIndexer) error) []multiclustertest.Index {
	t.Helper()
	collector := &multiclustertest.IndexCollector{}
	if err := register(context.Background(), collector); err != nil {
		t.Fatal(err)
	}
	return collector.Indexes
}

func stagingProject(t *testing.T, name string, objects ...client.Object) *multiclustertest.Project {
	t.Helper()
	return multiclustertest.NewProject(t, name, multiclustertest.ProjectOptions{
		Scheme:             stagingScheme(t),
		Objects:            objects,
		StatusSubresources: []client.Object{&fleetv1.Machine{}, &fleetv1.Installation{}, &fleetv1.MachineClass{}, &fleetv1.MachineProfile{}, &fleetv1.MachineCapacity{}},
		Indexes:            indexes(t, manager.RegisterProjectIndexes),
	})
}

// TestStagingAllocatesOnlyForThePublicationConsumer runs synthetic
// cross-project staging through the multi-project manager: discovery engages
// the publication consumer and another project with identically named
// Machines. The publisher writes only into the consumer, only the consumer's
// Machine is allocated, and the other project gets requested-hardware
// resolution and nothing else.
func TestStagingAllocatesOnlyForThePublicationConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	scheme := stagingScheme(t)
	const authority = "staging"
	profileName, className := "general-purpose-24c-128g", "central-metal"
	sourceProfile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: profileName, UID: "source-profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 24}, Memory: fleetv1.MemoryRequirement{Capacity: "128Gi"}}}
	sourceClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: className, UID: "source-class-uid", Generation: 1}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: profileName}}}, Access: &fleetv1.AccessPolicy{}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}}}}}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "central-metal-01", UID: "capacity-uid", Labels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "central-metal-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-reuse"}, ReclaimPolicy: "Retain"}}
	claim := func() *fleetv1.Machine {
		return &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "m", UID: "shared-claim-uid", Generation: 1}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: className}, MachineProfileRef: fleetv1.LocalReference{Name: profileName}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"}}
	}
	// The provider project is also an Active metal consumer with its own
	// Machine of the same name; it must engage with requested hardware only.
	var capacityStatusWrites atomic.Int64
	countCapacity := func(object client.Object) {
		if _, ok := object.(*fleetv1.MachineCapacity); ok {
			capacityStatusWrites.Add(1)
		}
	}
	provider := multiclustertest.NewProject(t, "datum-cloud", multiclustertest.ProjectOptions{
		Scheme:             stagingScheme(t),
		Objects:            []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "provider-ns"}}, sourceClass, sourceProfile, capacity, claim()},
		StatusSubresources: []client.Object{&fleetv1.Machine{}, &fleetv1.Installation{}, &fleetv1.MachineClass{}, &fleetv1.MachineProfile{}, &fleetv1.MachineCapacity{}},
		Indexes:            indexes(t, manager.RegisterProjectIndexes),
		Funcs: interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
				countCapacity(o)
				return c.SubResource(sub).Update(ctx, o, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				countCapacity(o)
				return c.SubResource(sub).Patch(ctx, o, p, opts...)
			},
		},
	})
	consumer := stagingProject(t, "consumer", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "consumer-ns"}}, claim())
	// The other project names the same class and profile; its own profile
	// resolves, but nothing publishes the class there and staging is not
	// installed for it.
	other := stagingProject(t, "other", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "other-ns"}}, claim(), sourceProfile.DeepCopyObject().(*fleetv1.MachineProfile))
	private := multiclustertest.NewProject(t, "host", multiclustertest.ProjectOptions{Scheme: scheme, Indexes: indexes(t, func(ctx context.Context, indexer client.FieldIndexer) error {
		return manager.RegisterPrivateIndexes(ctx, indexer, "fleet-system-private")
	})})
	root := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&resourcemanagerv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "datum-cloud", UID: "provider-project"}},
		&resourcemanagerv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "consumer", UID: "consumer-project"}},
		&resourcemanagerv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "other", UID: "other-project"}},
	).Build()
	identity := func(name string) controller.ProjectIdentity {
		project, err := manager.ResolveProjectIdentity(ctx, root, authority, name)
		if err != nil {
			t.Fatal(err)
		}
		return project
	}
	verifierFor := func(expected controller.ProjectIdentity) manager.ProjectVerifier {
		return manager.ProjectVerifierFunc(func(ctx context.Context, project controller.ProjectIdentity) error {
			if current, err := manager.ResolveProjectIdentity(ctx, root, authority, expected.RouteName); err != nil || current != expected || project != expected {
				return manager.ErrProjectIdentityUnavailable
			}
			return nil
		})
	}
	consumerProject, providerProject := identity("consumer"), identity("datum-cloud")

	executor := &safeExecutor{results: map[string]simulator.OperationResult{}}
	coordinator, err := controller.NewCoordinator(private.Client, "fleet-system-private", executor, controller.LocalDigestImageResolver{}, nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	discovery := multiclustertest.NewProvider()
	projects := manager.NewProjects(func(ctx context.Context, name multicluster.ClusterName) (controller.ProjectIdentity, error) {
		return manager.ResolveProjectIdentity(ctx, root, authority, string(name))
	}, verifierFor)
	mgr, _, err := projectdiscovery.NewManager(&rest.Config{Host: "https://host.example.test"}, discovery, ctrlmanager.Options{
		Scheme: scheme, Cache: manager.ProjectCacheOptions(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	}, projects.Aware)
	if err != nil {
		t.Fatal(err)
	}
	providerCluster := provider.NewCluster()
	template := Handler{Coordinator: coordinator, Location: "us-central-1", Provider: providerCluster.GetClient(), ProviderProject: providerProject, Now: time.Now}
	if err := SetupPublisher(mgr, projects, providerCluster, PublisherOptions{ConsumerProject: consumerProject, ProviderProject: providerProject, ClassName: className, ProfileName: profileName}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Setup(ctx, mgr, projects, manager.SetupOptions{
		PrivateNamespace: "fleet-system-private",
		PrivateCluster:   private.NewCluster(),
		HandlersFor:      ConsumerHandlers(manager.DefaultHandlers, consumerProject, template),
		ProviderProject:  providerProject,
		ProviderCluster:  providerCluster,
		ProviderVerifier: verifierFor(providerProject),
		ProviderConsumer: "consumer",
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	discovery.Engage(t, ctx, other)
	discovery.Engage(t, ctx, consumer)
	discovery.Engage(t, ctx, provider)

	get := func(p *multiclustertest.Project) *fleetv1.Machine {
		var m fleetv1.Machine
		if err := p.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "m"}, &m); err != nil {
			t.Fatal(err)
		}
		return &m
	}
	multiclustertest.Eventually(t, "the publication consumer's Machine is allocated", func() bool { return get(consumer).Status.Phase == "Ready" })
	if hw := get(consumer).Status.RequestedHardware; hw == nil || hw.CPU.Cores != 24 {
		t.Fatalf("consumer requested hardware = %#v", hw)
	}
	var allocated fleetv1.MachineCapacity
	if err := provider.Client.Get(ctx, client.ObjectKeyFromObject(capacity), &allocated); err != nil {
		t.Fatal(err)
	}
	if c := apiMeta.FindStatusCondition(allocated.Status.Conditions, "Available"); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("provider capacity not allocated: %#v", allocated.Status.Conditions)
	}
	multiclustertest.Eventually(t, "the other project resolves requested hardware", func() bool {
		hw := get(other).Status.RequestedHardware
		return hw != nil && hw.CPU.Cores == 24
	})
	multiclustertest.Consistently(t, "the other project gets nothing but requested hardware", func() bool {
		m := get(other)
		var classes fleetv1.MachineClassList
		if err := other.Client.List(ctx, &classes); err != nil {
			return false
		}
		return m.Status.Phase == "" && len(m.Finalizers) == 0 && len(m.Status.Conditions) == 1 && len(classes.Items) == 0 && other.Writes() == 1
	})
	multiclustertest.Eventually(t, "the provider project resolves requested hardware", func() bool {
		hw := get(provider).Status.RequestedHardware
		return hw != nil && hw.CPU.Cores == 24
	})
	writes := capacityStatusWrites.Load()
	multiclustertest.Consistently(t, "the provider project gets nothing but requested hardware and its capacity stays allocated once", func() bool {
		m := get(provider)
		var current fleetv1.MachineCapacity
		if err := provider.Client.Get(ctx, client.ObjectKeyFromObject(capacity), &current); err != nil {
			return false
		}
		c := apiMeta.FindStatusCondition(current.Status.Conditions, "Available")
		return m.Status.Phase == "" && len(m.Finalizers) == 0 && len(m.Status.Conditions) == 1 &&
			c != nil && c.Status == metav1.ConditionFalse && capacityStatusWrites.Load() == writes && get(consumer).Status.Phase == "Ready"
	})
	// Every private record belongs to the consumer project.
	var secrets corev1.SecretList
	if err := private.Client.List(ctx, &secrets, client.InNamespace("fleet-system-private")); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) == 0 {
		t.Fatal("staging retained no private custody")
	}
	for i := range secrets.Items {
		route, err := controller.DecodePrivateLifecycleRoute(&secrets.Items[i])
		if err != nil {
			continue
		}
		if route.ConsumerProject.UID != "" && route.ConsumerProject.UID != types.UID("consumer-project") {
			t.Fatalf("private record %s belongs to %q", secrets.Items[i].Name, route.ConsumerProject.UID)
		}
	}
}
