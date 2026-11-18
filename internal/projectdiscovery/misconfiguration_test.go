package projectdiscovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

func metalService(producer string) *servicesv1alpha1.Service {
	service := &servicesv1alpha1.Service{ObjectMeta: metav1.ObjectMeta{Name: "metal"}}
	service.Spec.ServiceName = MetalServiceName
	service.Spec.Owner.ProducerProjectRef.Name = producer
	return service
}

func activeConsumer(project string) *servicesv1alpha1.ServiceConsumer {
	sc := &servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sc-" + project}}
	sc.Spec.ServiceRef.Name = "metal"
	sc.Spec.ConsumerProjectRef.Name = project
	sc.Status.Phase = servicesv1alpha1.ConsumerPhaseActive
	return sc
}

// discoveryWith builds the consumer discovery's readiness view over fake root
// and provider-project readers.
func discoveryWith(t *testing.T, providerProject string, services ...client.Object) (*ConsumerDiscovery, client.Client) {
	t.Helper()
	scheme := testScheme(t)
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	root := fake.NewClientBuilder().WithScheme(scheme).WithObjects(services...).Build()
	consumers := fake.NewClientBuilder().WithScheme(scheme).WithObjects(activeConsumer("east")).Build()
	return &ConsumerDiscovery{rootClient: root, consumers: consumers, providerProject: providerProject, serviceNames: map[string]struct{}{MetalServiceName: {}}}, root
}

func TestDiscoveryExpectsActiveConsumersOfTheConfiguredProducer(t *testing.T) {
	discovery, _ := discoveryWith(t, "datum-cloud", metalService("datum-cloud"))
	expected, err := discovery.Expected(context.Background())
	if err != nil || !reflect.DeepEqual(expected, []multicluster.ClusterName{"east"}) {
		t.Fatalf("expected = %v, %v", expected, err)
	}
	if err := probe(ProjectsEngagedCheck(discovery, NewEngageTracker(nil))); err == nil || !strings.Contains(err.Error(), "east") {
		t.Fatalf("readiness before east engaged = %v", err)
	}
}

// A missing metal Service would otherwise make the expected set empty and
// readiness pass while nothing is engaged.
func TestReadinessFailsWithoutTheMetalService(t *testing.T) {
	discovery, root := discoveryWith(t, "datum-cloud")
	if _, err := discovery.Expected(context.Background()); !errors.Is(err, ErrDiscoveryMisconfigured) {
		t.Fatalf("missing Service err = %v, want ErrDiscoveryMisconfigured", err)
	}
	if err := probe(ProjectsEngagedCheck(discovery, NewEngageTracker(nil))); !errors.Is(err, ErrDiscoveryMisconfigured) {
		t.Fatalf("readiness without the Service = %v, want it to fail", err)
	}
	// Not cached as a failure: registering the Service clears it.
	if err := root.Create(context.Background(), metalService("datum-cloud")); err != nil {
		t.Fatal(err)
	}
	if _, err := discovery.Expected(context.Background()); err != nil {
		t.Fatalf("after registration: %v", err)
	}
}

// A provider project other than the Service's producer watches a project with
// no metal ServiceConsumers, engages nothing, and would look healthy.
func TestReadinessFailsForAProviderProjectThatIsNotTheProducer(t *testing.T) {
	discovery, root := discoveryWith(t, "not-the-producer", metalService("datum-cloud"))
	_, err := discovery.Expected(context.Background())
	if !errors.Is(err, ErrDiscoveryMisconfigured) || !strings.Contains(err.Error(), `"datum-cloud"`) || !strings.Contains(err.Error(), `"not-the-producer"`) {
		t.Fatalf("mismatch err = %v, want an explicit producer mismatch", err)
	}
	tracker := NewEngageTracker(nil)
	for i := 0; i < 2; i++ {
		if err := probe(ProjectsEngagedCheck(discovery, tracker)); !errors.Is(err, ErrDiscoveryMisconfigured) {
			t.Fatalf("readiness probe %d with a mismatched provider project = %v, want it to keep failing", i, err)
		}
	}
	// Correcting the Service (or the configuration) clears it.
	var service servicesv1alpha1.Service
	if err := root.Get(context.Background(), client.ObjectKey{Name: "metal"}, &service); err != nil {
		t.Fatal(err)
	}
	service.Spec.Owner.ProducerProjectRef.Name = "not-the-producer"
	if err := root.Update(context.Background(), &service); err != nil {
		t.Fatal(err)
	}
	if _, err := discovery.Expected(context.Background()); err != nil {
		t.Fatalf("after correction: %v", err)
	}
}
