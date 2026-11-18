package requestresolver

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

func failProfileReads(funcs *interceptor.Funcs) {
	funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
		if _, ok := object.(*fleetv1.MachineProfile); ok {
			return errors.New("synthetic project API outage")
		}
		return c.Get(ctx, key, object, opts...)
	}
}

func TestProjectsResolveIndependently(t *testing.T) {
	// Equal Machine and profile names in different projects must resolve
	// against their own project's profile only.
	east := newProject(t, "project-east", []client.Object{machine("m", "general"), profile("general", 3, 8, "32Gi")})
	west := newProject(t, "project-west", []client.Object{machine("m", "general"), profile("general", 7, 16, "64Gi")})
	provider := newTestProvider()
	r := startResolver(t, provider, nil)
	provider.engage(t, context.Background(), east)
	provider.engage(t, context.Background(), west)

	eventually(t, "east pinned from its own profile", pinnedCores(east, t, "m", 8))
	eventually(t, "west pinned from its own profile", pinnedCores(west, t, "m", 16))
	if got := east.machine(t, "m").Status.RequestedHardware; got.Memory.Capacity != "32Gi" || got.Source.ProfileGeneration != 3 {
		t.Fatalf("east snapshot = %#v", got)
	}
	if got := west.machine(t, "m").Status.RequestedHardware; got.Memory.Capacity != "64Gi" || got.Source.ProfileGeneration != 7 {
		t.Fatalf("west snapshot = %#v", got)
	}
	for _, p := range []*project{east, west} {
		if c := resolvedCondition(p.machine(t, "m")); c == nil || c.Status != metav1.ConditionTrue || c.Reason != controller.ReasonRequestResolved {
			t.Fatalf("%s RequestResolved = %#v", p.name, c)
		}
	}
	if got := r.tracker.Names(); !reflect.DeepEqual(got, []multicluster.ClusterName{"project-east", "project-west"}) {
		t.Fatalf("engaged = %v", got)
	}
}

func TestBrokenProjectDoesNotAffectOthers(t *testing.T) {
	healthy := newProject(t, "healthy", []client.Object{machine("m", "general"), profile("general", 1, 8, "32Gi")})
	missing := newProject(t, "missing-profile", []client.Object{machine("m", "general")})
	invalid := newProject(t, "invalid-profile", []client.Object{machine("m", "general"), profile("general", 1, 0, "32Gi")})
	outage := newProject(t, "api-outage", []client.Object{machine("m", "general"), profile("general", 1, 8, "32Gi")}, failProfileReads)
	provider := newTestProvider()
	startResolver(t, provider, nil)
	for _, p := range []*project{missing, invalid, outage, healthy} {
		provider.engage(t, context.Background(), p)
	}

	eventually(t, "healthy project pinned", pinnedCores(healthy, t, "m", 8))
	eventually(t, "missing profile reported", hasReason(missing, t, "m", controller.ReasonProfileNotFound))
	eventually(t, "invalid profile reported", hasReason(invalid, t, "m", controller.ReasonProfileInvalid))
	for _, p := range []*project{missing, invalid} {
		if snapshot := p.machine(t, "m").Status.RequestedHardware; snapshot != nil {
			t.Fatalf("%s invented a snapshot: %#v", p.name, snapshot)
		}
	}
	// An unknown read is retried, never written as success or failure.
	consistently(t, "outage project stays unwritten", func() bool {
		m := outage.machine(t, "m")
		return outage.writes.Load() == 0 && m.Status.RequestedHardware == nil && resolvedCondition(m) == nil
	})
}

func TestDisengagedProjectStopsWithoutWriting(t *testing.T) {
	departing := newProject(t, "departing", []client.Object{machine("pinned", "general"), profile("general", 1, 8, "32Gi")})
	provider := newTestProvider()
	r := startResolver(t, provider, nil)
	cl := provider.engage(t, context.Background(), departing)
	eventually(t, "pinned before departure", pinnedCores(departing, t, "pinned", 8))
	eventually(t, "tracker engaged", func() bool { return r.tracker.Engaged("departing") })
	machineInformer, err := cl.cache.informerFor(fleetv1.GroupVersion.WithKind("Machine"))
	if err != nil {
		t.Fatal(err)
	}

	provider.disengage("departing")
	eventually(t, "tracker forgets the project", func() bool { return !r.tracker.Engaged("departing") })
	eventually(t, "sources removed their handlers", func() bool { return machineInformer.handlerCount() == 0 })
	writes := departing.writes.Load()

	// A Machine that appears after departure, and an event or stale queued
	// request for it, must not produce any status, in particular not
	// RequestResolved=False.
	late := machine("late", "absent")
	if err := departing.client.Create(context.Background(), late); err != nil {
		t.Fatal(err)
	}
	machineInformer.emitAdd(late)
	result, err := Reconciler{Clusters: r.manager}.Reconcile(context.Background(), mcreconcile.Request{ClusterName: "departing", Request: requestFor(late)})
	if err != nil || !result.IsZero() {
		t.Fatalf("stale request = %#v, %v; want dropped", result, err)
	}
	consistently(t, "no writes after departure", func() bool {
		return departing.writes.Load() == writes && len(departing.machine(t, "late").Status.Conditions) == 0
	})
}

func TestNewlyEngagedProjectResolvesExistingMachines(t *testing.T) {
	first := newProject(t, "first", []client.Object{machine("m", "general"), profile("general", 1, 8, "32Gi")})
	later := newProject(t, "later", []client.Object{machine("a", "general"), machine("b", "general"), profile("general", 2, 4, "16Gi")})
	provider := newTestProvider()
	startResolver(t, provider, nil)
	provider.engage(t, context.Background(), first)
	eventually(t, "first project pinned", pinnedCores(first, t, "m", 8))

	// The later project joins the consumer set while the resolver runs; its
	// pre-existing Machines are delivered by the initial informer list.
	provider.engage(t, context.Background(), later)
	eventually(t, "existing Machine a pinned", pinnedCores(later, t, "a", 4))
	eventually(t, "existing Machine b pinned", pinnedCores(later, t, "b", 4))
}

func TestPinnedValuesSurviveRestart(t *testing.T) {
	p := newProject(t, "restart", []client.Object{machine("m", "general"), profile("general", 1, 8, "32Gi")})
	provider := newTestProvider()
	first := startResolver(t, provider, nil)
	provider.engage(t, context.Background(), p)
	eventually(t, "pinned by first resolver", pinnedCores(p, t, "m", 8))
	accepted := *p.machine(t, "m").Status.RequestedHardware
	first.stop()

	// While the resolver is down the profile is deleted and recreated under
	// the same name with different hardware.
	if err := p.client.Delete(context.Background(), profile("general", 1, 8, "32Gi")); err != nil {
		t.Fatal(err)
	}
	if err := p.client.Create(context.Background(), profile("general", 5, 64, "512Gi")); err != nil {
		t.Fatal(err)
	}
	writes := p.writes.Load()

	restarted := newTestProvider()
	second := startResolver(t, restarted, nil)
	restarted.engage(t, context.Background(), p)
	eventually(t, "restarted resolver engaged", func() bool { return second.tracker.Engaged("restart") })
	eventually(t, "restarted resolver re-read the Machine", func() bool { return p.machineReads("m") > 0 })
	consistently(t, "snapshot stays pinned across restart", func() bool {
		m := p.machine(t, "m")
		c := resolvedCondition(m)
		return m.Status.RequestedHardware != nil && reflect.DeepEqual(accepted, *m.Status.RequestedHardware) &&
			c != nil && c.Reason == controller.ReasonRequestResolved && p.writes.Load() == writes
	})
}

func TestProfileEventRequeuesOnlyReferencingMachines(t *testing.T) {
	p := newProject(t, "fanout", []client.Object{machine("wants-p", "p"), machine("wants-q", "q")})
	provider := newTestProvider()
	startResolver(t, provider, nil)
	cl := provider.engage(t, context.Background(), p)
	eventually(t, "wants-p unresolved", hasReason(p, t, "wants-p", controller.ReasonProfileNotFound))
	eventually(t, "wants-q unresolved", hasReason(p, t, "wants-q", controller.ReasonProfileNotFound))
	readsQ := p.machineReads("wants-q")

	created := profile("p", 1, 8, "32Gi")
	if err := p.client.Create(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	profileInformer, err := cl.cache.informerFor(fleetv1.GroupVersion.WithKind("MachineProfile"))
	if err != nil {
		t.Fatal(err)
	}
	profileInformer.emitAdd(created)

	eventually(t, "wants-p resolved by the profile event", pinnedCores(p, t, "wants-p", 8))
	consistently(t, "wants-q not re-queued", func() bool { return p.machineReads("wants-q") == readsQ })
}

func TestMachinesForProfileSkipsOtherProfilesAndPinnedMachines(t *testing.T) {
	pinned := machine("pinned", "p")
	pinned.Status.RequestedHardware = &fleetv1.RequestedHardwareStatus{}
	p := newProject(t, "mapper", []client.Object{machine("unpinned", "p"), machine("other", "q"), pinned})
	got := MachinesForProfile(context.Background(), p.client, "p")
	if len(got) != 1 || got[0].Name != "unpinned" || got[0].Namespace != "default" {
		t.Fatalf("requests = %#v, want only default/unpinned", got)
	}
	if got := MachinesForProfile(context.Background(), p.client, ""); got != nil {
		t.Fatalf("empty profile mapped to %#v", got)
	}
}

func TestSetupRegistersOnlyTheResolverControllerAndWatches(t *testing.T) {
	p := newProject(t, "watches", []client.Object{machine("m", "general"), profile("general", 1, 8, "32Gi")})
	provider := newTestProvider()
	r := startResolver(t, provider, nil)
	cl := provider.engage(t, context.Background(), p)
	eventually(t, "pinned", pinnedCores(p, t, "m", 8))
	if got := cl.cache.requestedKinds(); !reflect.DeepEqual(got, map[string]bool{"Machine": true, "MachineProfile": true}) {
		t.Fatalf("engaged project informers = %v, want only Machine and MachineProfile", got)
	}
	if provider.starts.Load() != 1 {
		t.Fatalf("provider Start called %d times, want exactly once", provider.starts.Load())
	}
	if _, ok := r.manager.GetProvider().(multicluster.ProviderRunnable); ok {
		t.Fatal("mcmanager can see the provider's Start and would leader-gate engagement")
	}
}

func TestSetupAddsExactlyOneController(t *testing.T) {
	provider := newTestProvider()
	r := startResolver(t, provider, nil)
	recording := &recordingManager{Manager: r.manager}
	if err := Setup(context.Background(), recording); err != nil {
		t.Fatal(err)
	}
	if recording.added != 1 {
		t.Fatalf("Setup added %d runnables, want exactly the one resolver controller", recording.added)
	}
}

func TestReconcilerRetriesUnknownClusterErrors(t *testing.T) {
	broken := clusterGetterFunc(func(context.Context, multicluster.ClusterName) error { return errors.New("provider unavailable") })
	if _, err := (Reconciler{Clusters: broken}).Reconcile(context.Background(), mcreconcile.Request{ClusterName: "x"}); err == nil {
		t.Fatal("an unknown cluster lookup error must be retried")
	}
	if _, err := (Reconciler{}).Reconcile(context.Background(), mcreconcile.Request{ClusterName: "x"}); err == nil {
		t.Fatal("a resolver without a cluster source must fail closed")
	}
}
