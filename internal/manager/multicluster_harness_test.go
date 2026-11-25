package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/multiclustertest"
	"go.datum.net/fleet/internal/projectdiscovery"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

const testAuthority = "milo-synthetic"

func fleetScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, fleetv1.AddToScheme, networkingv1.AddToScheme, locationsv1alpha1.AddToScheme, quotav1alpha1.AddToScheme, resourcemanagerv1alpha1.AddToScheme, servicesv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func collect(t *testing.T, register func(context.Context, client.FieldIndexer) error) []multiclustertest.Index {
	t.Helper()
	collector := &multiclustertest.IndexCollector{}
	if err := register(context.Background(), collector); err != nil {
		t.Fatal(err)
	}
	return collector.Indexes
}

// newConsumer is a synthetic Milo project control plane with the manager's
// project indexes installed.
func newConsumer(t *testing.T, name string, objects ...client.Object) *multiclustertest.Project {
	t.Helper()
	return multiclustertest.NewProject(t, name, multiclustertest.ProjectOptions{
		Scheme:             fleetScheme(t),
		Objects:            objects,
		StatusSubresources: []client.Object{&fleetv1.Machine{}, &fleetv1.Installation{}, &fleetv1.MachineClass{}, &fleetv1.MachineProfile{}, &fleetv1.MachineCapacity{}},
		Indexes:            collect(t, RegisterProjectIndexes),
		CountReadsOf:       &fleetv1.Machine{},
	})
}

// milo is the root Milo API: Project identities by name.
type rootMilo struct {
	client.Client
}

func newMilo(t *testing.T, projects map[string]types.UID) *rootMilo {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(fleetScheme(t))
	for name, uid := range projects {
		builder = builder.WithObjects(newMiloProject(name, uid))
	}
	return &rootMilo{Client: builder.Build()}
}

func newMiloProject(name string, uid types.UID) *resourcemanagerv1alpha1.Project {
	return &resourcemanagerv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: name, UID: uid}}
}

func (m *rootMilo) identity(t *testing.T, name string) controller.ProjectIdentity {
	t.Helper()
	project, err := ResolveProjectIdentity(context.Background(), m, testAuthority, name)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func (m *rootMilo) resolver() ProjectResolver {
	return func(ctx context.Context, name multicluster.ClusterName) (controller.ProjectIdentity, error) {
		return ResolveProjectIdentity(ctx, m, testAuthority, string(name))
	}
}

func (m *rootMilo) verifierFor(expected controller.ProjectIdentity) ProjectVerifier {
	return ProjectVerifierFunc(func(ctx context.Context, project controller.ProjectIdentity) error {
		current, err := ResolveProjectIdentity(ctx, m, testAuthority, expected.RouteName)
		if err != nil || current != expected || project != expected {
			return ErrProjectIdentityUnavailable
		}
		return nil
	})
}

type fleet struct {
	manager  mcmanager.Manager
	projects *Projects
	tracker  *projectdiscovery.EngageTracker
	cancel   context.CancelFunc
	done     chan error
}

// startFleet runs the production wiring (projectdiscovery.NewManager,
// NewProjects, Setup) over provider. The host config is never contacted.
func startFleet(t *testing.T, provider multicluster.Provider, root *rootMilo, options SetupOptions) *fleet {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	projects := NewProjects(root.resolver(), root.verifierFor)
	mgr, tracker, err := projectdiscovery.NewManager(&rest.Config{Host: "https://host.example.test"}, provider, ctrlmanager.Options{
		Scheme:                 fleetScheme(t),
		Cache:                  ProjectCacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	}, projects.Aware)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if options.HandlersFor == nil {
		options.HandlersFor = DefaultHandlers
	}
	if err := Setup(ctx, mgr, projects, options); err != nil {
		cancel()
		t.Fatal(err)
	}
	f := &fleet{manager: mgr, projects: projects, tracker: tracker, cancel: cancel, done: make(chan error, 1)}
	go func() { f.done <- mgr.Start(ctx) }()
	t.Cleanup(f.stop)
	return f
}

func (f *fleet) stop() {
	if f.cancel == nil {
		return
	}
	f.cancel()
	f.cancel = nil
	select {
	case <-f.done:
	case <-time.After(10 * time.Second):
	}
}

// recordingHandlers records every lifecycle request per engaged project and
// otherwise behaves as DefaultHandlers.
type recordingHandlers struct {
	mu       sync.Mutex
	requests map[multicluster.ClusterName][]ProjectRequest
}

func newRecordingHandlers() *recordingHandlers {
	return &recordingHandlers{requests: map[multicluster.ClusterName][]ProjectRequest{}}
}

func (r *recordingHandlers) factory(binding ProjectBinding) (Handlers, error) {
	handlers, err := DefaultHandlers(binding)
	if err != nil {
		return handlers, err
	}
	handlers.Machine = HandlerFunc(func(_ context.Context, request ProjectRequest) Outcome {
		r.mu.Lock()
		r.requests[binding.ClusterName] = append(r.requests[binding.ClusterName], request)
		r.mu.Unlock()
		return Outcome{}
	})
	return handlers, nil
}

func (r *recordingHandlers) count(name multicluster.ClusterName) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests[name])
}

func (r *recordingHandlers) all(name multicluster.ClusterName) []ProjectRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ProjectRequest(nil), r.requests[name]...)
}

func mcMachine(name, uid, profile string) *fleetv1.Machine {
	return &fleetv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID(uid), Generation: 1},
		Spec:       fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "standard"}, MachineProfileRef: fleetv1.LocalReference{Name: profile}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"},
	}
}

func mcProfile(name string, generation, cores int64, memory string) *fleetv1.MachineProfile {
	return &fleetv1.MachineProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-profile-" + name), Generation: generation},
		Spec:       fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: cores}, Memory: fleetv1.MemoryRequirement{Capacity: memory}},
	}
}

func mcNamespace(uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: types.UID(uid)}}
}

func getMachine(t *testing.T, p *multiclustertest.Project, name string) *fleetv1.Machine {
	t.Helper()
	var machine fleetv1.Machine
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &machine); err != nil {
		t.Fatal(err)
	}
	return &machine
}

func pinnedWith(t *testing.T, p *multiclustertest.Project, name string, cores int64) func() bool {
	return func() bool {
		snapshot := getMachine(t, p, name).Status.RequestedHardware
		return snapshot != nil && snapshot.CPU.Cores == cores
	}
}
