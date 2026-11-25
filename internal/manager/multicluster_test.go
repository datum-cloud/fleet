package manager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/multiclustertest"
)

var (
	eventually   = multiclustertest.Eventually
	consistently = multiclustertest.Consistently
)

// The default configuration (no provider, no synthetic integration) resolves
// requested hardware in every engaged project and performs no lifecycle
// effect anywhere. Both projects use identical Machine names and UIDs.
func TestNoProviderResolvesRequestedHardwareWithoutLifecycleEffects(t *testing.T) {
	installation := func() *fleetv1.Installation {
		return &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "install", UID: "shared-installation-uid"}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: "m"}, ImageRef: fleetv1.LocalReference{Name: "image"}}}
	}
	east := newConsumer(t, "east", mcNamespace("east-ns"), mcMachine("m", "shared-uid", "general"), mcProfile("general", 2, 8, "32Gi"), installation())
	west := newConsumer(t, "west", mcNamespace("west-ns"), mcMachine("m", "shared-uid", "general"), mcProfile("general", 5, 16, "64Gi"), installation())
	root := newMilo(t, map[string]types.UID{"east": "east-project", "west": "west-project"})
	provider := multiclustertest.NewProvider()
	startFleet(t, provider, root, SetupOptions{})
	provider.Engage(t, context.Background(), east)
	provider.Engage(t, context.Background(), west)

	eventually(t, "east pinned from its own profile", pinnedWith(t, east, "m", 8))
	eventually(t, "west pinned from its own profile", pinnedWith(t, west, "m", 16))
	for _, p := range []*multiclustertest.Project{east, west} {
		m := getMachine(t, p, "m")
		if c := apiMeta.FindStatusCondition(m.Status.Conditions, controller.ConditionRequestResolved); c == nil || c.Status != metav1.ConditionTrue {
			t.Fatalf("%s RequestResolved = %#v", p.Name, c)
		}
	}
	consistently(t, "no lifecycle effect in any project", func() bool {
		for _, p := range []*multiclustertest.Project{east, west} {
			m := getMachine(t, p, "m")
			if len(m.Finalizers) != 0 || len(m.Status.Conditions) != 1 || m.Status.Phase != "" || m.Status.AllocatedHardware != nil {
				return false
			}
			var inst fleetv1.Installation
			if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "install"}, &inst); err != nil || len(inst.Status.Conditions) != 0 || inst.Status.Phase != "" || len(inst.Finalizers) != 0 {
				return false
			}
			// Exactly one write per project: the pinning status update.
			if p.Writes() != 1 {
				return false
			}
		}
		return true
	})
}

// When a project leaves the consumer set, the manager only stops reconciling
// it: no handler runs and no status is rewritten. Re-engagement resumes.
func TestDisengageStopsReconcilingWithoutRewrite(t *testing.T) {
	east := newConsumer(t, "east", mcNamespace("east-ns"), mcMachine("m", "east-machine", "general"), mcProfile("general", 1, 8, "32Gi"))
	root := newMilo(t, map[string]types.UID{"east": "east-project"})
	recorder := newRecordingHandlers()
	provider := multiclustertest.NewProvider()
	f := startFleet(t, provider, root, SetupOptions{HandlersFor: recorder.factory})
	cl := provider.Engage(t, context.Background(), east)
	eventually(t, "engaged and reconciled", func() bool { return recorder.count("east") > 0 && pinnedWith(t, east, "m", 8)() })
	machineInformer, err := cl.FakeCache.InformerFor(fleetv1.GroupVersion.WithKind("Machine"))
	if err != nil {
		t.Fatal(err)
	}

	provider.Disengage("east")
	eventually(t, "unbound", func() bool { return f.projects.Get("east") == nil && !f.tracker.Engaged("east") })
	eventually(t, "project sources stopped", func() bool { return machineInformer.HandlerCount() == 0 })
	handled, machine := recorder.count("east"), getMachine(t, east, "m")

	// A stale queued request and project events arrive after departure.
	machineInformer.EmitUpdate(machine, machine)
	request := ProjectRequest{ClusterName: "east", AuthorityID: testAuthority, ProjectUID: "east-project", Namespace: "default", Name: "m", UID: "east-machine"}
	if result, err := (ClusterProjectReconciler{Projects: f.projects, Owner: func(r *ProjectRuntime) *LifecycleOwner { return r.Machine }}).Reconcile(context.Background(), request); err != nil || !result.IsZero() {
		t.Fatalf("stale request = %#v, %v; want dropped", result, err)
	}
	consistently(t, "nothing reconciled or written after departure", func() bool {
		current := getMachine(t, east, "m")
		return recorder.count("east") == handled &&
			current.ResourceVersion == machine.ResourceVersion && reflect.DeepEqual(current.Status, machine.Status)
	})

	// Re-engagement resumes reconciliation.
	provider.Engage(t, context.Background(), east)
	eventually(t, "resumed after re-engagement", func() bool { return recorder.count("east") > handled })
}

func TestPinnedRequestedHardwareSurvivesRestart(t *testing.T) {
	p := newConsumer(t, "restart", mcNamespace("ns"), mcMachine("m", "m-uid", "general"), mcProfile("general", 1, 8, "32Gi"))
	root := newMilo(t, map[string]types.UID{"restart": "restart-project"})
	provider := multiclustertest.NewProvider()
	first := startFleet(t, provider, root, SetupOptions{})
	provider.Engage(t, context.Background(), p)
	eventually(t, "pinned", pinnedWith(t, p, "m", 8))
	accepted := *getMachine(t, p, "m").Status.RequestedHardware
	first.stop()

	// While the manager is down the profile is replaced under the same name.
	if err := p.Client.Delete(context.Background(), mcProfile("general", 1, 8, "32Gi")); err != nil {
		t.Fatal(err)
	}
	if err := p.Client.Create(context.Background(), mcProfile("general", 9, 64, "512Gi")); err != nil {
		t.Fatal(err)
	}
	writes, reads := p.Writes(), p.Reads("m")
	restarted := multiclustertest.NewProvider()
	second := startFleet(t, restarted, root, SetupOptions{})
	restarted.Engage(t, context.Background(), p)
	eventually(t, "restarted manager re-read the Machine", func() bool { return second.tracker.Engaged("restart") && p.Reads("m") > reads })
	consistently(t, "snapshot stays pinned across restart", func() bool {
		m := getMachine(t, p, "m")
		return m.Status.RequestedHardware != nil && reflect.DeepEqual(accepted, *m.Status.RequestedHardware) && p.Writes() == writes
	})
}

func TestEngagementFailsClosedOnIdentity(t *testing.T) {
	known := newConsumer(t, "known", mcNamespace("ns"), mcMachine("m", "m-uid", "general"), mcProfile("general", 1, 8, "32Gi"))
	unknown := newConsumer(t, "unknown", mcNamespace("ns"), mcMachine("m", "m-uid", "general"), mcProfile("general", 1, 8, "32Gi"))
	alias := newConsumer(t, "alias", mcNamespace("ns"), mcMachine("m", "m-uid", "general"), mcProfile("general", 1, 8, "32Gi"))
	root := newMilo(t, map[string]types.UID{"known": "known-project", "alias": "known-project"})
	provider := multiclustertest.NewProvider()
	f := startFleet(t, provider, root, SetupOptions{})

	if _, err := provider.TryEngage(t, context.Background(), unknown); !errors.Is(err, ErrProjectIdentityUnavailable) {
		t.Fatalf("project without a Milo identity engaged: %v", err)
	}
	provider.Engage(t, context.Background(), known)
	eventually(t, "known pinned", pinnedWith(t, known, "m", 8))
	if _, err := provider.TryEngage(t, context.Background(), alias); !errors.Is(err, ErrProjectConflict) {
		t.Fatalf("a second cluster for one project lifetime engaged: %v", err)
	}
	if f.projects.Get("unknown") != nil || f.projects.Get("alias") != nil {
		t.Fatal("a refused project was registered")
	}

	// The project is deleted and recreated in Milo under the same name while
	// engaged: the per-reconcile verifier refuses work for the old lifetime.
	var project = root.identity(t, "known")
	if err := root.Delete(context.Background(), newMiloProject(project.RouteName, project.UID)); err != nil {
		t.Fatal(err)
	}
	if err := root.Create(context.Background(), newMiloProject("known", "replacement-project")); err != nil {
		t.Fatal(err)
	}
	late := mcMachine("late", "late-uid", "general")
	if err := known.Client.Create(context.Background(), late); err != nil {
		t.Fatal(err)
	}
	consistently(t, "no work for a replaced project lifetime", func() bool {
		return getMachine(t, known, "late").Status.RequestedHardware == nil && unknown.Writes() == 0 && alias.Writes() == 0
	})
}
