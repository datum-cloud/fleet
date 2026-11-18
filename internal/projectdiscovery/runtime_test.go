package projectdiscovery

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	"sigs.k8s.io/yaml"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/multiclustertest"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

func probe(check healthz.Checker) error {
	return check(httptest.NewRequest("GET", "/readyz", nil))
}

type discoveryFunc struct {
	synced   error
	expected []multicluster.ClusterName
}

func (d discoveryFunc) Synced(context.Context) error { return d.synced }
func (d discoveryFunc) Expected(context.Context) ([]multicluster.ClusterName, error) {
	return d.expected, nil
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// startManager runs NewManager and the health checks over provider, with no
// controllers. The host config is never contacted.
func startManager(t *testing.T, provider multicluster.Provider, discovery Discovery, caches ...NamedCache) (mcmanager.Manager, *EngageTracker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	mgr, tracker, err := NewManager(&rest.Config{Host: "https://host.example.test"}, provider, manager.Options{
		Scheme:                 testScheme(t),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	}, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := AddHealthChecks(mgr, discovery, tracker, caches...); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return mgr, tracker
}

type syncedCache struct{ synced bool }

func (c *syncedCache) WaitForCacheSync(context.Context) bool { return c.synced }

func TestReadinessRequiresDiscoveryEngagementAndSyncedCaches(t *testing.T) {
	p := multiclustertest.NewProject(t, "ready", multiclustertest.ProjectOptions{Scheme: testScheme(t)})
	provider := multiclustertest.NewProvider()
	discovery := discoveryFunc{expected: []multicluster.ClusterName{"ready"}}
	private := &syncedCache{}
	mgr, tracker := startManager(t, provider, discovery, NamedCache{Name: "private-records", Cache: private})

	if err := probe(DiscoverySyncedCheck(discoveryFunc{synced: errors.New("not synced")})); err == nil {
		t.Fatal("unsynced discovery reported ready")
	}
	if err := probe(ProjectsEngagedCheck(discovery, tracker)); err == nil {
		t.Fatal("ready before the expected project engaged")
	}
	cl := provider.Engage(t, context.Background(), p)
	multiclustertest.Eventually(t, "engaged", func() bool { return probe(ProjectsEngagedCheck(discovery, tracker)) == nil })
	if err := probe(EngagedCachesSyncedCheck(tracker)); err != nil {
		t.Fatalf("synced caches reported unready: %v", err)
	}
	if err := probe(CacheSyncedCheck(NamedCache{Name: "private-records", Cache: private})); err == nil {
		t.Fatal("an unsynced private cache reported ready")
	}
	private.synced = true
	if err := probe(CacheSyncedCheck(NamedCache{Name: "private-records", Cache: private})); err != nil {
		t.Fatal(err)
	}

	cl.FakeCache.Synced = ptr.To(false)
	if err := probe(EngagedCachesSyncedCheck(tracker)); err == nil {
		t.Fatal("an unsynced engaged project cache reported ready")
	}
	cl.FakeCache.Synced = nil

	provider.Disengage("ready")
	multiclustertest.Eventually(t, "unready after an expected project disengages", func() bool {
		return probe(ProjectsEngagedCheck(discovery, tracker)) != nil
	})
	if err := probe(ProjectsEngagedCheck(discoveryFunc{}, tracker)); err != nil {
		t.Fatalf("departed project still gates readiness: %v", err)
	}
	if provider.Starts() != 1 {
		t.Fatalf("provider Start called %d times, want exactly once", provider.Starts())
	}
	if _, ok := mgr.GetProvider().(multicluster.ProviderRunnable); ok {
		t.Fatal("mcmanager can see the provider's Start and would leader-gate engagement")
	}
}

func TestActiveConsumerProjectsMatchesProviderEngagementRule(t *testing.T) {
	now := metav1.Now()
	consumer := func(name, service, project string, phase servicesv1alpha1.ConsumerPhase, deleting bool) servicesv1alpha1.ServiceConsumer {
		sc := servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Name: name}}
		sc.Spec.ServiceRef.Name = service
		sc.Spec.ConsumerProjectRef.Name = project
		sc.Status.Phase = phase
		if deleting {
			sc.DeletionTimestamp = &now
		}
		return sc
	}
	got := ActiveConsumerProjects(map[string]struct{}{"metal": {}}, []servicesv1alpha1.ServiceConsumer{
		consumer("a", "metal", "active-b", servicesv1alpha1.ConsumerPhaseActive, false),
		consumer("b", "metal", "active-a", servicesv1alpha1.ConsumerPhaseActive, false),
		consumer("c", "metal", "active-a", servicesv1alpha1.ConsumerPhaseActive, false),
		consumer("d", "metal", "pending", servicesv1alpha1.ConsumerPhasePendingApproval, false),
		consumer("e", "metal", "denied", servicesv1alpha1.ConsumerPhaseDenied, false),
		consumer("f", "metal", "deleting", servicesv1alpha1.ConsumerPhaseActive, true),
		consumer("g", "compute", "other-service", servicesv1alpha1.ConsumerPhaseActive, false),
		consumer("h", "metal", "", servicesv1alpha1.ConsumerPhaseActive, false),
	})
	if want := []multicluster.ClusterName{"active-a", "active-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active projects = %v, want %v", got, want)
	}
}

func TestDiscoveryClientsAreReadOnly(t *testing.T) {
	scheme := testScheme(t)
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	sc := &servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Name: "sc-metal", Namespace: "default"}}
	inner := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sc).WithObjects(sc).Build()
	c := readOnlyClient{Client: inner}
	ctx := context.Background()

	if err := c.Get(ctx, client.ObjectKeyFromObject(sc), &servicesv1alpha1.ServiceConsumer{}); err != nil {
		t.Fatalf("reads must pass through: %v", err)
	}
	writes := map[string]error{
		"create":        c.Create(ctx, &servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default"}}),
		"update":        c.Update(ctx, sc.DeepCopy()),
		"patch":         c.Patch(ctx, sc.DeepCopy(), client.MergeFrom(sc)),
		"delete":        c.Delete(ctx, sc.DeepCopy()),
		"deleteAllOf":   c.DeleteAllOf(ctx, &servicesv1alpha1.ServiceConsumer{}, client.InNamespace("default")),
		"statusUpdate":  c.Status().Update(ctx, sc.DeepCopy()),
		"machineStatus": c.Status().Patch(ctx, &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default"}}, client.MergeFrom(&fleetv1.Machine{})),
	}
	for name, err := range writes {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s = %v, want ErrReadOnly", name, err)
		}
	}

	// The provider's Paused bookkeeping is dropped, not written: the
	// resolver must never confirm a suspension it does not perform.
	paused := sc.DeepCopy()
	paused.Status.Conditions = []metav1.Condition{{Type: servicesv1alpha1.ConditionTypePaused, Status: metav1.ConditionTrue, Reason: "Paused", LastTransitionTime: metav1.Now()}}
	if err := c.Status().Patch(ctx, paused, client.MergeFrom(sc)); err != nil {
		t.Fatalf("Paused bookkeeping must not abort the provider reconcile: %v", err)
	}
	var stored servicesv1alpha1.ServiceConsumer
	if err := inner.Get(ctx, client.ObjectKeyFromObject(sc), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Status.Conditions) != 0 {
		t.Fatalf("resolver confirmed suspension: %#v", stored.Status.Conditions)
	}
}

func TestMetalServiceNameMatchesBundledService(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "components", "service-catalog", "service.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var service servicesv1alpha1.Service
	if err := yaml.Unmarshal(data, &service); err != nil {
		t.Fatal(err)
	}
	if service.Spec.ServiceName != MetalServiceName {
		t.Fatalf("bundled Service serviceName = %q, resolver scopes to %q", service.Spec.ServiceName, MetalServiceName)
	}
}
