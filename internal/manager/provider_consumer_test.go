package manager

import (
	"context"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/multiclustertest"
	"go.datum.net/fleet/internal/projectdiscovery"
)

// A project that is both the synthetic provider and a metal consumer engages
// like any other consumer: requested hardware only, no lifecycle effect, no
// MachineCapacity status write, and capacity events on its runtime resolve
// only to its own Machines. Readiness passes with it engaged.
func TestProviderProjectEngagesAsAnOrdinaryConsumer(t *testing.T) {
	var capacityStatusWrites atomic.Int64
	countCapacity := func(object client.Object) {
		if _, ok := object.(*fleetv1.MachineCapacity); ok {
			capacityStatusWrites.Add(1)
		}
	}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "standard", UID: "provider-class-uid"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "central"}}}}}}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "metal-01", UID: "capacity-uid", Labels: map[string]string{"pool": "central"}}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "metal-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "reuse"}, ReclaimPolicy: "Retain"}}
	provider := multiclustertest.NewProject(t, "provider", multiclustertest.ProjectOptions{
		Scheme:             fleetScheme(t),
		Objects:            []client.Object{mcNamespace("provider-ns"), class, capacity, mcMachine("m", "shared-uid", "general"), mcProfile("general", 1, 8, "32Gi")},
		StatusSubresources: []client.Object{&fleetv1.Machine{}, &fleetv1.MachineCapacity{}},
		Indexes:            collect(t, RegisterProjectIndexes),
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
	consumer := newConsumer(t, "consumer", mcNamespace("consumer-ns"), mcMachine("m", "shared-uid", "general"), mcProfile("general", 1, 16, "64Gi"))
	root := newMilo(t, map[string]types.UID{"provider": "provider-project", "consumer": "consumer-project"})
	providerIdentity := root.identity(t, "provider")

	discovery := multiclustertest.NewProvider()
	providerCluster := provider.NewCluster()
	f := startFleet(t, discovery, root, SetupOptions{
		ProviderProject:  providerIdentity,
		ProviderCluster:  providerCluster,
		ProviderVerifier: root.verifierFor(providerIdentity),
		ProviderConsumer: "consumer",
	})
	discovery.Engage(t, context.Background(), consumer)
	if _, err := discovery.TryEngage(t, context.Background(), provider); err != nil {
		t.Fatalf("the provider project could not engage as a consumer: %v", err)
	}

	eventually(t, "the provider project pins its own Machine", pinnedWith(t, provider, "m", 8))
	eventually(t, "the consumer pins its own Machine", pinnedWith(t, consumer, "m", 16))
	if err := probeCheck(projectdiscovery.ProjectsEngagedCheck(projectdiscovery.StaticDiscovery{"consumer", "provider"}, f.tracker)); err != nil {
		t.Fatalf("readiness with the provider among the consumers: %v", err)
	}

	// Deterministic routing: a MachineCapacity event on the provider's engaged
	// runtime resolves only to the provider's own Machines.
	providerRuntime := f.projects.Get("provider")
	if providerRuntime == nil || !providerRuntime.Binding.Project.SameLifetime(providerIdentity) {
		t.Fatal("the provider project is not engaged under its own identity")
	}
	route := providerRuntime.Mappers.Route(RouteMachineCapacity)(context.Background(), getCapacity(t, provider))
	if len(route) != 1 {
		t.Fatalf("capacity route = %#v", route)
	}
	targets, err := providerRuntime.Mappers.ResolveRoute(context.Background(), route[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.ClusterName != "provider" || !target.SameProject(providerIdentity.AuthorityID, providerIdentity.UID) {
			t.Fatalf("provider capacity event resolved to %#v", target)
		}
	}

	// Capacity events reach both the provider-cluster watch and the engaged
	// runtime's route watch; neither writes capacity status or lifecycle state.
	informer, err := providerCluster.FakeCache.InformerFor(fleetv1.GroupVersion.WithKind("MachineCapacity"))
	if err != nil {
		t.Fatal(err)
	}
	current := getCapacity(t, provider)
	informer.EmitUpdate(current, current)
	consistently(t, "no lifecycle effect in the provider project", func() bool {
		m := getMachine(t, provider, "m")
		return capacityStatusWrites.Load() == 0 && len(m.Finalizers) == 0 && len(m.Status.Conditions) == 1 && m.Status.Phase == "" && len(getCapacity(t, provider).Status.Conditions) == 0
	})
}

func getCapacity(t *testing.T, p *multiclustertest.Project) *fleetv1.MachineCapacity {
	t.Helper()
	var capacity fleetv1.MachineCapacity
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "metal-01"}, &capacity); err != nil {
		t.Fatal(err)
	}
	return &capacity
}
