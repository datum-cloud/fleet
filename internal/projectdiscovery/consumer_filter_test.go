package projectdiscovery

import (
	"context"
	"errors"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

func consumerOf(service, project string) *servicesv1alpha1.ServiceConsumer {
	sc := &servicesv1alpha1.ServiceConsumer{ObjectMeta: metav1.ObjectMeta{Name: "sc-" + service + "-" + project}}
	sc.Spec.ServiceRef.Name = service
	sc.Spec.ConsumerProjectRef.Name = project
	return sc
}

func otherService() *servicesv1alpha1.Service {
	service := &servicesv1alpha1.Service{ObjectMeta: metav1.ObjectMeta{Name: "compute"}}
	service.Spec.ServiceName = "compute.example.test"
	service.Spec.Owner.ProducerProjectRef.Name = "datum-cloud"
	return service
}

func TestRelevantConsumerKeepsOnlyTheConfiguredService(t *testing.T) {
	discovery, _ := discoveryWith(t, "datum-cloud", metalService("datum-cloud"), otherService())
	cases := map[string]struct {
		object any
		want   bool
	}{
		"metal consumer":         {consumerOf("metal", "east"), true},
		"other service consumer": {consumerOf("compute", "east"), false},
		"metal tombstone":        {toolscache.DeletedFinalStateUnknown{Key: "sc", Obj: consumerOf("metal", "east")}, true},
		"other tombstone":        {toolscache.DeletedFinalStateUnknown{Key: "sc", Obj: consumerOf("compute", "east")}, false},
		"unknown object":         {"not a consumer", true},
	}
	for name, tc := range cases {
		if got := discovery.relevantConsumer(tc.object); got != tc.want {
			t.Errorf("%s: relevant = %v, want %v", name, got, tc.want)
		}
	}
}

// Without a resolvable Service the filter keeps every event: the consumer
// provider still filters authoritatively, so this can only add work, never
// drop an engagement.
func TestRelevantConsumerFailsOpenWhileTheServiceIsUnresolved(t *testing.T) {
	discovery, _ := discoveryWith(t, "datum-cloud")
	if !discovery.relevantConsumer(consumerOf("compute", "east")) {
		t.Fatal("filtered an event before the Service could be resolved")
	}
	scheme := testScheme(t)
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	failing := interceptor.NewClient(discovery.rootClient.(client.WithWatch), interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("root unavailable")
	}})
	discovery.rootClient = failing
	if !discovery.relevantConsumer(consumerOf("compute", "east")) {
		t.Fatal("filtered an event while the root API was unavailable")
	}
}

type countingHandler struct {
	mu                    sync.Mutex
	adds, updates, delete []string
}

func (h *countingHandler) OnAdd(obj any, _ bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.adds = append(h.adds, obj.(*servicesv1alpha1.ServiceConsumer).Name)
}
func (h *countingHandler) OnUpdate(_, obj any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updates = append(h.updates, obj.(*servicesv1alpha1.ServiceConsumer).Name)
}
func (h *countingHandler) OnDelete(obj any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.delete = append(h.delete, obj.(*servicesv1alpha1.ServiceConsumer).Name)
}

func TestFilteredInformerDeliversOnlyRelevantConsumers(t *testing.T) {
	discovery, _ := discoveryWith(t, "datum-cloud", metalService("datum-cloud"), otherService())
	base := controllertest.NewFakeInformer(controllertest.Synced)
	informer := filteredInformer{Informer: base, relevant: discovery.relevantConsumer}
	handler := &countingHandler{}
	if _, err := informer.AddEventHandlerWithOptions(handler, toolscache.HandlerOptions{}); err != nil {
		t.Fatal(err)
	}
	metal, other := consumerOf("metal", "east"), consumerOf("compute", "east")
	base.Add(metal)
	base.Add(other)
	base.Update(other, other)
	base.Delete(other)
	if len(handler.adds) != 1 || handler.adds[0] != metal.Name || len(handler.updates) != 0 || len(handler.delete) != 0 {
		t.Fatalf("delivered adds=%v updates=%v deletes=%v; want only the metal consumer", handler.adds, handler.updates, handler.delete)
	}
	// A consumer that moves off the metal Service is delivered as a delete,
	// so the engaged set is still recomputed.
	moved := metal.DeepCopy()
	moved.Spec.ServiceRef.Name = "compute"
	base.Update(metal, moved)
	if len(handler.delete) != 1 || handler.delete[0] != metal.Name {
		t.Fatalf("leaving the metal Service delivered deletes=%v", handler.delete)
	}
}

func TestConsumerEventCacheWrapsOnlyServiceConsumers(t *testing.T) {
	scheme := testScheme(t)
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := &consumerEventCache{Cache: &informertest.FakeInformers{Scheme: scheme}, relevant: func(any) bool { return false }}
	consumers, err := c.GetInformer(context.Background(), &servicesv1alpha1.ServiceConsumer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := consumers.(filteredInformer); !ok {
		t.Fatalf("ServiceConsumer informer = %T, want filtered", consumers)
	}
	byKind, err := c.GetInformerForKind(context.Background(), servicesv1alpha1.GroupVersion.WithKind("ServiceConsumer"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byKind.(filteredInformer); !ok {
		t.Fatalf("ServiceConsumer informer by kind = %T, want filtered", byKind)
	}
	machines, err := c.GetInformer(context.Background(), &fleetv1.Machine{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := machines.(filteredInformer); ok {
		t.Fatal("a non-ServiceConsumer informer was filtered")
	}
}
