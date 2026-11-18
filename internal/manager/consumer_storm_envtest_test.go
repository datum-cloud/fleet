package manager

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/config"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"go.datum.net/fleet/internal/projectdiscovery"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

// consumerReconciles reads controller-runtime's reconcile counters for the
// consumer provider's ServiceConsumer controller.
func consumerReconciles(t *testing.T) (total, errors float64) {
	t.Helper()
	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "controller_runtime_reconcile_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["controller"] != "consumer-serviceconsumer" {
				continue
			}
			total += metric.GetCounter().GetValue()
			if labels["result"] == "error" {
				errors += metric.GetCounter().GetValue()
			}
		}
	}
	return total, errors
}

// TestConsumerDiscoveryIgnoresOtherServicesAndBacksOff drives the real
// consumer provider. ServiceConsumers for another Service in the producer
// project must not reconcile at all, and a metal consumer whose engagement
// keeps failing must be retried with backoff by its own key only, not by
// every ServiceConsumer in the project. It needs envtest binaries.
func TestConsumerDiscoveryIgnoresOtherServicesAndBacksOff(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; see docs/development/README.md")
	}
	scheme := fleetScheme(t)
	milo := newSyntheticMilo(t, scheme)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rootClient, err := client.New(milo.root, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, canonical := range map[string]string{"metal": projectdiscovery.MetalServiceName, "compute": "compute.example.test"} {
		service := &servicesv1alpha1.Service{ObjectMeta: metav1.ObjectMeta{Name: name}}
		service.Spec.ServiceName, service.Spec.Phase, service.Spec.DisplayName = canonical, "Draft", name
		service.Spec.Owner.ProducerProjectRef.Name = "datum-cloud"
		must(rootClient.Create(ctx, service))
	}
	must(rootClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testPrivateNamespace}}))
	consumer := func(name, service, project string) {
		sc := &servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}}
		sc.Spec.ServiceRef.Name, sc.Spec.ConsumerProjectRef.Name = service, project
		must(rootClient.Create(ctx, sc))
		setConsumerPhase(t, rootClient, name, servicesv1alpha1.ConsumerPhaseActive)
	}
	const otherConsumers = 30
	for i := 0; i < otherConsumers; i++ {
		consumer(fmt.Sprintf("sc-compute-%02d", i), "compute", fmt.Sprintf("project-%02d", i))
	}

	miloConfig := &rest.Config{Host: milo.url}
	discovery, err := projectdiscovery.NewConsumerDiscovery(miloConfig, miloConfig, projectdiscovery.ConsumerScopedProjectionConfig{ProviderProject: "datum-cloud", ServiceNames: []string{projectdiscovery.MetalServiceName}}, scheme, func(o *ctrlmanager.Options) {
		o.Controller.SkipNameValidation = ptr.To(true)
	})
	must(err)
	identities := &rootMilo{Client: rootClient}
	projects := NewProjects(identities.resolver(), identities.verifierFor)
	mgr, _, err := projectdiscovery.NewManager(milo.root, discovery.Provider, ctrlmanager.Options{
		Scheme: scheme, Cache: ProjectCacheOptions(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	}, projects.Aware)
	must(err)
	privateCluster, err := ctrlcluster.New(milo.root, func(o *ctrlcluster.Options) {
		o.Scheme = scheme
		o.Cache = PrivateCacheOptions(testPrivateNamespace)
	})
	must(err)
	must(Setup(ctx, mgr, projects, SetupOptions{PrivateNamespace: testPrivateNamespace, PrivateCluster: privateCluster, HandlersFor: DefaultHandlers}))
	baseTotal, baseErrors := consumerReconciles(t)
	go func() { _ = discovery.ProviderManager.Start(ctx) }()
	go func() { _ = mgr.Start(ctx) }()
	must(discovery.Synced(ctx))

	// The initial list of 30 ServiceConsumers for another Service, and further
	// events on them, reconcile nothing.
	time.Sleep(2 * time.Second)
	if total, _ := consumerReconciles(t); total != baseTotal {
		t.Fatalf("ServiceConsumers for another Service caused %v reconciles, want 0", total-baseTotal)
	}

	// A metal consumer whose project has no Milo identity can never engage.
	consumer("sc-metal-missing", "metal", "missing-project")
	start := time.Now()
	window := 5 * time.Second
	time.Sleep(window)
	total, errors := consumerReconciles(t)
	retries := errors - baseErrors
	// One key with per-item exponential backoff (5ms doubling) makes about a
	// dozen attempts in five seconds. Without backoff, or with every
	// ServiceConsumer retrying, it would be hundreds.
	if retries < 1 || retries > 20 {
		t.Fatalf("failed engagement retried %v times in %v (%v reconciles), want backoff from one key", retries, time.Since(start).Round(time.Second), total-baseTotal)
	}
	t.Logf("failed engagement retried %v times in %v", retries, window)
}
