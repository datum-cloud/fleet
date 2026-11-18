package requestresolver

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	"sigs.k8s.io/yaml"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
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

func TestReadinessRequiresDiscoveryEngagementAndSyncedCaches(t *testing.T) {
	p := newProject(t, "ready", []client.Object{machine("m", "general"), profile("general", 1, 8, "32Gi")})
	provider := newTestProvider()
	discovery := discoveryFunc{expected: []multicluster.ClusterName{"ready"}}
	r := startResolver(t, provider, discovery)

	if err := probe(DiscoverySyncedCheck(discoveryFunc{synced: errors.New("not synced")})); err == nil {
		t.Fatal("unsynced discovery reported ready")
	}
	if err := probe(ProjectsEngagedCheck(discovery, r.tracker)); err == nil {
		t.Fatal("ready before the expected project engaged")
	}
	cl := provider.engage(t, context.Background(), p)
	eventually(t, "engaged", func() bool { return probe(ProjectsEngagedCheck(discovery, r.tracker)) == nil })
	if err := probe(EngagedCachesSyncedCheck(r.tracker)); err != nil {
		t.Fatalf("synced caches reported unready: %v", err)
	}

	cl.cache.Synced = ptr.To(false)
	if err := probe(EngagedCachesSyncedCheck(r.tracker)); err == nil {
		t.Fatal("an unsynced engaged project cache reported ready")
	}
	cl.cache.Synced = nil

	provider.disengage("ready")
	eventually(t, "unready after an expected project disengages", func() bool {
		return probe(ProjectsEngagedCheck(discovery, r.tracker)) != nil
	})
	// A project that left the consumer set is no longer expected.
	if err := probe(ProjectsEngagedCheck(discoveryFunc{}, r.tracker)); err != nil {
		t.Fatalf("departed project still gates readiness: %v", err)
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

// TestResolverBinaryDoesNotLinkLifecycleWiring proves the resolver binary
// cannot start Fleet's lifecycle controllers: the packages that register
// them, and the integrations they need, are not part of its build at all.
func TestResolverBinaryDoesNotLinkLifecycleWiring(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", "go.datum.net/fleet/cmd/fleet-request-resolver").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	deps := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		deps[line] = true
	}
	if !deps["go.datum.net/fleet/internal/requestresolver"] {
		t.Fatalf("unexpected dependency listing:\n%s", out)
	}
	for _, forbidden := range []string{
		"go.datum.net/fleet/internal/manager",    // lifecycle Setup and controllers
		"go.datum.net/fleet/internal/staging",    // synthetic allocation handler
		"go.datum.net/fleet/internal/milo",       // quota and entitlement adapters
		"go.datum.net/fleet/internal/location",   // location routing
		"go.datum.net/fleet/internal/standalone", // standalone runner
		"go.datum.net/fleet/internal/network/nso",
		"go.datum.net/network-services-operator/api/v1alpha",
		"go.miloapis.com/milo/pkg/apis/quota/v1alpha1",
		"go.miloapis.com/locations/api/v1alpha1",
	} {
		if deps[forbidden] {
			t.Errorf("fleet-request-resolver links %s", forbidden)
		}
	}
}
