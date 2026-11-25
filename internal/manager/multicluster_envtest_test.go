package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/projectdiscovery"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

const projectRoutePrefix = "/apis/resourcemanager.miloapis.com/v1alpha1/projects/"

// syntheticMilo routes Milo's project control-plane URLs to separate API
// servers, so the real consumer provider, project caches, and Machine CRD
// validation run exactly as they would against Milo.
type syntheticMilo struct {
	url      string
	root     *rest.Config
	projects map[string]client.Client
}

func startAPIServer(t *testing.T, crds ...string) *rest.Config {
	t.Helper()
	env := &envtest.Environment{CRDDirectoryPaths: crds, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	return cfg
}

func reverseProxy(t *testing.T, cfg *rest.Config) *httputil.ReverseProxy {
	t.Helper()
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(cfg.Host)
	if err != nil {
		t.Fatal(err)
	}
	return &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = r.In.URL.Path
			r.Out.URL.RawPath = ""
		},
	}
}

func moduleDir(t *testing.T, module string) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	if err != nil {
		t.Fatalf("locate %s: %v", module, err)
	}
	return strings.TrimSpace(string(out))
}

// newSyntheticMilo starts a root API server (Milo root and, here, also the
// management cluster: Services, Projects, the provider project's
// ServiceConsumers, and the lease) and one API server
// per consumer project serving every kind the manager watches.
func newSyntheticMilo(t *testing.T, scheme *runtime.Scheme, consumerProjects ...string) *syntheticMilo {
	t.Helper()
	catalog := filepath.Join(moduleDir(t, "go.miloapis.com/service-catalog"), "config", "base", "crd", "bases")
	miloCRDs := filepath.Join(moduleDir(t, "go.miloapis.com/milo"), "config", "crd", "bases")
	nso := filepath.Join(moduleDir(t, "go.datum.net/network-services-operator"), "config", "crd", "bases")
	locations := filepath.Join(moduleDir(t, "go.miloapis.com/locations"), "config", "base", "crd", "bases", "locations.miloapis.com_locations.yaml")
	root := startAPIServer(t,
		filepath.Join(catalog, "services.miloapis.com_services.yaml"),
		filepath.Join(catalog, "services.miloapis.com_serviceconsumers.yaml"),
		filepath.Join(miloCRDs, "resourcemanager", "resourcemanager.miloapis.com_projects.yaml"),
	)
	routes := map[string]*httputil.ReverseProxy{}
	m := &syntheticMilo{root: root, projects: map[string]client.Client{}}
	// The provider project shares the root server: it hosts ServiceConsumers.
	routes["datum-cloud"] = reverseProxy(t, root)
	for _, name := range consumerProjects {
		cfg := startAPIServer(t,
			filepath.Join("..", "..", "config", "base", "crd", "metal.datumapis.com.yaml"),
			filepath.Join(nso, "networking.datumapis.com_networks.yaml"),
			filepath.Join(miloCRDs, "quota", "quota.miloapis.com_resourceclaims.yaml"),
			locations,
			filepath.Join(catalog, "services.miloapis.com_serviceavailabilities.yaml"),
		)
		routes[name] = reverseProxy(t, cfg)
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		m.projects[name] = c
	}
	rootProxy := reverseProxy(t, root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, projectRoutePrefix); ok {
			name, tail, _ := strings.Cut(rest, "/")
			proxy, known := routes[name]
			tail, isControlPlane := strings.CutPrefix(tail, "control-plane")
			if !known || !isControlPlane {
				http.NotFound(w, r)
				return
			}
			if tail == "" {
				tail = "/"
			}
			r.URL.Path = tail
			proxy.ServeHTTP(w, r)
			return
		}
		rootProxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	m.url = server.URL
	return m
}

func setConsumerPhase(t *testing.T, c client.Client, name string, phase servicesv1alpha1.ConsumerPhase) {
	t.Helper()
	var sc servicesv1alpha1.ServiceConsumer
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &sc); err != nil {
		t.Fatal(err)
	}
	sc.Status.Phase = phase
	if err := c.Status().Update(context.Background(), &sc); err != nil {
		t.Fatal(err)
	}
}

// TestManagerEndToEndAgainstSyntheticMilo runs the fleet-manager's production
// milo-mode wiring with no provider: the service catalog consumer provider,
// per-project identities resolved from Milo, per-project caches, a real host
// lease, readiness, and the Machine CRD's
// pinning rule. It needs envtest binaries (KUBEBUILDER_ASSETS) and is skipped
// without them.
func TestManagerEndToEndAgainstSyntheticMilo(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; see docs/development/README.md")
	}
	scheme := fleetScheme(t)
	milo := newSyntheticMilo(t, scheme, "east", "west")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rootClient, err := client.New(milo.root, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "config", "components", "service-catalog", "service.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var service servicesv1alpha1.Service
	if err := yaml.Unmarshal(data, &service); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(rootClient.Create(ctx, &service))
	for _, name := range []string{"east", "west"} {
		must(rootClient.Create(ctx, &resourcemanagerv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: resourcemanagerv1alpha1.ProjectSpec{OwnerRef: resourcemanagerv1alpha1.OwnerReference{Kind: "Organization", Name: "synthetic-org"}}}))
		sc := &servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sc-" + name}}
		sc.Spec.ServiceRef.Name = service.Name
		sc.Spec.ConsumerProjectRef.Name = name
		must(rootClient.Create(ctx, sc))
	}
	setConsumerPhase(t, rootClient, "sc-east", servicesv1alpha1.ConsumerPhaseActive)
	setConsumerPhase(t, rootClient, "sc-west", servicesv1alpha1.ConsumerPhasePendingApproval)
	for name, cores := range map[string]int64{"east": 8, "west": 16} {
		c := milo.projects[name]
		must(c.Create(ctx, mcProfile("general", 0, cores, "32Gi")))
		m := mcMachine("m", "", "general")
		must(c.Create(ctx, m))
	}

	miloConfig := &rest.Config{Host: milo.url}
	discovery, err := projectdiscovery.NewConsumerDiscovery(miloConfig, miloConfig, projectdiscovery.ConsumerScopedProjectionConfig{ProviderProject: "datum-cloud", ServiceNames: []string{projectdiscovery.MetalServiceName}}, scheme, func(o *ctrlmanager.Options) {
		o.Controller.SkipNameValidation = ptr.To(true)
	})
	if err != nil {
		t.Fatal(err)
	}
	identities := &rootMilo{Client: rootClient}
	projects := NewProjects(identities.resolver(), identities.verifierFor)
	mgr, tracker, err := projectdiscovery.NewManager(milo.root, discovery.Provider, ctrlmanager.Options{
		Scheme:                  scheme,
		Cache:                   ProjectCacheOptions(),
		Metrics:                 metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:  "0",
		LeaderElection:          true,
		LeaderElectionID:        "fleet-manager-test",
		LeaderElectionNamespace: "default",
		Controller:              config.Controller{SkipNameValidation: ptr.To(true)},
	}, projects.Aware)
	if err != nil {
		t.Fatal(err)
	}
	must(Setup(ctx, mgr, projects, SetupOptions{HandlersFor: DefaultHandlers}))
	go func() { _ = discovery.ProviderManager.Start(ctx) }()
	go func() { _ = mgr.Start(ctx) }()

	get := func(project, name string) *fleetv1.Machine {
		var m fleetv1.Machine
		must(milo.projects[project].Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &m))
		return &m
	}
	pinned := func(project, name string, cores int64) func() bool {
		return func() bool {
			s := get(project, name).Status.RequestedHardware
			return s != nil && s.CPU.Cores == cores
		}
	}
	slow := func(description string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", description)
	}
	ready := func() error {
		for _, check := range []func() error{
			func() error { return probeCheck(projectdiscovery.DiscoverySyncedCheck(discovery)) },
			func() error { return probeCheck(projectdiscovery.ProjectsEngagedCheck(discovery, tracker)) },
			func() error { return probeCheck(projectdiscovery.EngagedCachesSyncedCheck(tracker)) },
		} {
			if err := check(); err != nil {
				return err
			}
		}
		return nil
	}
	noLifecycleEffect := func(project, name string) {
		t.Helper()
		m := get(project, name)
		if len(m.Finalizers) != 0 || m.Status.Phase != "" || len(m.Status.Conditions) > 1 {
			t.Fatalf("%s/%s lifecycle effect without a provider: finalizers=%v status=%#v", project, name, m.Finalizers, m.Status)
		}
	}

	slow("east pinned through the consumer provider", pinned("east", "m", 8))
	slow("ready with east engaged", func() bool { return ready() == nil })
	if c := apiMeta.FindStatusCondition(get("east", "m").Status.Conditions, controller.ConditionRequestResolved); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("east RequestResolved = %#v", c)
	}
	noLifecycleEffect("east", "m")
	if tracker.Engaged("west") || get("west", "m").Status.RequestedHardware != nil || len(get("west", "m").Status.Conditions) != 0 {
		t.Fatal("a pending consumer project was engaged or written")
	}

	// West joins and resolves its existing Machine from its own profile.
	setConsumerPhase(t, rootClient, "sc-west", servicesv1alpha1.ConsumerPhaseActive)
	slow("west pinned after joining", pinned("west", "m", 16))
	slow("ready with both engaged", func() bool { return ready() == nil })
	noLifecycleEffect("west", "m")
	accepted := *get("east", "m").Status.RequestedHardware

	// East leaves: no further status in east, nothing released or rewritten.
	setConsumerPhase(t, rootClient, "sc-east", servicesv1alpha1.ConsumerPhaseDenied)
	slow("east disengaged", func() bool { return !tracker.Engaged("east") && projects.Get("east") == nil })
	must(milo.projects["east"].Create(ctx, mcMachine("late", "", "absent")))
	must(milo.projects["west"].Create(ctx, mcMachine("second", "", "general")))
	slow("west keeps resolving", pinned("west", "second", 16))
	if conditions := get("east", "late").Status.Conditions; len(conditions) != 0 {
		t.Fatalf("departed project was written: %#v", conditions)
	}
	if got := get("east", "m").Status.RequestedHardware; got == nil || !reflect.DeepEqual(*got, accepted) {
		t.Fatalf("east snapshot changed after departure: %#v", got)
	}

	// Discovery stayed observational: no finalizer and no Paused
	// confirmation, even when the platform signals suspension.
	var west servicesv1alpha1.ServiceConsumer
	must(rootClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "sc-west"}, &west))
	apiMeta.SetStatusCondition(&west.Status.Conditions, metav1.Condition{Type: servicesv1alpha1.ConditionTypeSuspended, Status: metav1.ConditionTrue, Reason: "Suspended", Message: "synthetic suspension"})
	must(rootClient.Status().Update(ctx, &west))
	must(milo.projects["west"].Create(ctx, mcMachine("third", "", "general")))
	slow("west resolves after the suspension signal", pinned("west", "third", 16))
	var consumers servicesv1alpha1.ServiceConsumerList
	must(rootClient.List(ctx, &consumers))
	for _, sc := range consumers.Items {
		if len(sc.Finalizers) != 0 || apiMeta.FindStatusCondition(sc.Status.Conditions, servicesv1alpha1.ConditionTypePaused) != nil {
			t.Fatalf("manager wrote ServiceConsumer %s: finalizers=%v conditions=%v", sc.Name, sc.Finalizers, sc.Status.Conditions)
		}
	}
}

func probeCheck(check healthz.Checker) error {
	return check(httptest.NewRequest("GET", "/readyz", nil))
}
