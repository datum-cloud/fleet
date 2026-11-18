package requestresolver

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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
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

func newSyntheticMilo(t *testing.T, scheme *runtime.Scheme, consumerProjects ...string) *syntheticMilo {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "go.miloapis.com/service-catalog").Output()
	if err != nil {
		t.Fatalf("locate service-catalog module: %v", err)
	}
	catalogCRDs := filepath.Join(strings.TrimSpace(string(out)), "config", "base", "crd", "bases")
	root := startAPIServer(t, filepath.Join(catalogCRDs, "services.miloapis.com_services.yaml"), filepath.Join(catalogCRDs, "services.miloapis.com_serviceconsumers.yaml"))
	routes := map[string]*httputil.ReverseProxy{}
	m := &syntheticMilo{root: root, projects: map[string]client.Client{}}
	// The provider project shares the root server: it hosts ServiceConsumers.
	routes["datum-cloud"] = reverseProxy(t, root)
	for _, name := range consumerProjects {
		cfg := startAPIServer(t, filepath.Join("..", "..", "config", "base", "crd", "metal.datumapis.com.yaml"))
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

// TestEndToEndAgainstSyntheticMilo exercises the production milo-mode wiring:
// the service catalog consumer provider, per-project caches, the host-cluster
// leader lease, readiness, and the Machine CRD's pinning rule. It needs
// envtest binaries (KUBEBUILDER_ASSETS) and is skipped without them.
func TestEndToEndAgainstSyntheticMilo(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; see docs/development/README.md")
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, fleetv1.AddToScheme, servicesv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	milo := newSyntheticMilo(t, scheme, "east", "west")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rootClient, err := client.New(milo.root, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	// Catalog state: the bundled metal Service and one ServiceConsumer per
	// consumer project in the provider project.
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "components", "service-catalog", "service.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var service servicesv1alpha1.Service
	if err := yaml.Unmarshal(data, &service); err != nil {
		t.Fatal(err)
	}
	if err := rootClient.Create(ctx, &service); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"east", "west"} {
		sc := &servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sc-" + name}}
		sc.Spec.ServiceRef.Name = service.Name
		sc.Spec.ConsumerProjectRef.Name = name
		if err := rootClient.Create(ctx, sc); err != nil {
			t.Fatal(err)
		}
	}
	setConsumerPhase(t, rootClient, "sc-east", servicesv1alpha1.ConsumerPhaseActive)
	setConsumerPhase(t, rootClient, "sc-west", servicesv1alpha1.ConsumerPhasePendingApproval)

	for name, cores := range map[string]int64{"east": 8, "west": 16} {
		c := milo.projects[name]
		if err := c.Create(ctx, profile("general", 0, cores, "32Gi")); err != nil {
			t.Fatal(err)
		}
		m := machine("m", "general")
		m.UID = ""
		if err := c.Create(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	miloConfig := &rest.Config{Host: milo.url}
	discovery, err := NewConsumerDiscovery(miloConfig, miloConfig, ConsumerScopedProjectionConfig{ProviderProject: "datum-cloud", ServiceNames: []string{MetalServiceName}}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	mgr, tracker, err := NewManager(milo.root, discovery.Provider, manager.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:  "0",
		LeaderElection:          true,
		LeaderElectionID:        "fleet-request-resolver-test",
		LeaderElectionNamespace: "default",
		Controller:              config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Setup(ctx, mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = discovery.ProviderManager.Start(ctx) }()
	go func() { _ = mgr.Start(ctx) }()

	get := func(project, name string) *fleetv1.Machine {
		var m fleetv1.Machine
		if err := milo.projects[project].Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &m); err != nil {
			t.Fatal(err)
		}
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
		deadline := time.Now().Add(60 * time.Second)
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
			func() error { return probe(DiscoverySyncedCheck(discovery)) },
			func() error { return probe(ProjectsEngagedCheck(discovery, tracker)) },
			func() error { return probe(EngagedCachesSyncedCheck(tracker)) },
		} {
			if err := check(); err != nil {
				return err
			}
		}
		return nil
	}

	slow("east pinned through the consumer provider", pinned("east", "m", 8))
	slow("ready with east engaged", func() bool { return ready() == nil })
	if tracker.Engaged("west") || get("west", "m").Status.RequestedHardware != nil || len(get("west", "m").Status.Conditions) != 0 {
		t.Fatal("a pending consumer project was engaged or written")
	}

	// West joins the consumer set and resolves its existing Machine.
	setConsumerPhase(t, rootClient, "sc-west", servicesv1alpha1.ConsumerPhaseActive)
	slow("west pinned after joining", pinned("west", "m", 16))
	slow("ready with both engaged", func() bool { return ready() == nil })

	// The API server's pinning rule and the resolver agree: a later profile
	// in east never changes the snapshot.
	accepted := *get("east", "m").Status.RequestedHardware

	// East leaves the consumer set. Its later Machines get no status at all.
	setConsumerPhase(t, rootClient, "sc-east", servicesv1alpha1.ConsumerPhaseDenied)
	slow("east disengaged", func() bool { return !tracker.Engaged("east") })
	late := machine("late", "absent")
	late.UID = ""
	if err := milo.projects["east"].Create(ctx, late); err != nil {
		t.Fatal(err)
	}
	// West keeps resolving while east is gone.
	other := machine("second", "general")
	other.UID = ""
	if err := milo.projects["west"].Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	slow("west keeps resolving", pinned("west", "second", 16))
	if conditions := get("east", "late").Status.Conditions; len(conditions) != 0 {
		t.Fatalf("departed project was written: %#v", conditions)
	}
	if got := get("east", "m").Status.RequestedHardware; got == nil || !reflect.DeepEqual(*got, accepted) {
		t.Fatalf("east snapshot changed after departure: %#v", got)
	}

	// Discovery stayed observational: no teardown finalizer and no Paused
	// confirmation, even when the platform signals suspension.
	var west servicesv1alpha1.ServiceConsumer
	if err := rootClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "sc-west"}, &west); err != nil {
		t.Fatal(err)
	}
	apimeta.SetStatusCondition(&west.Status.Conditions, metav1.Condition{Type: servicesv1alpha1.ConditionTypeSuspended, Status: metav1.ConditionTrue, Reason: "Suspended", Message: "synthetic suspension"})
	if err := rootClient.Status().Update(ctx, &west); err != nil {
		t.Fatal(err)
	}
	third := machine("third", "general")
	third.UID = ""
	if err := milo.projects["west"].Create(ctx, third); err != nil {
		t.Fatal(err)
	}
	slow("west resolves after the suspension signal", pinned("west", "third", 16))
	var consumers servicesv1alpha1.ServiceConsumerList
	if err := rootClient.List(ctx, &consumers); err != nil {
		t.Fatal(err)
	}
	for _, sc := range consumers.Items {
		if len(sc.Finalizers) != 0 || apimeta.FindStatusCondition(sc.Status.Conditions, servicesv1alpha1.ConditionTypePaused) != nil {
			t.Fatalf("resolver wrote ServiceConsumer %s: finalizers=%v conditions=%v", sc.Name, sc.Finalizers, sc.Status.Conditions)
		}
	}
	if c := apimeta.FindStatusCondition(get("west", "m").Status.Conditions, controller.ConditionRequestResolved); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("west RequestResolved = %#v", c)
	}
}
