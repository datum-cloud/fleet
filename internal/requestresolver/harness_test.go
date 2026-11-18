package requestresolver

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// replayInformer delivers the current objects to every newly registered
// handler, as a real informer's initial list does, and removes handlers when
// asked, as a disengaged source requests.
type replayInformer struct {
	*controllertest.FakeInformer
	list func() []client.Object

	mu       sync.Mutex
	handlers map[*registration]toolscache.ResourceEventHandler
}

type registration struct {
	toolscache.ResourceEventHandlerRegistration
}

func (i *replayInformer) register(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	base, err := i.FakeInformer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{})
	if err != nil {
		return nil, err
	}
	reg := &registration{base}
	i.mu.Lock()
	i.handlers[reg] = handler
	objects := i.list()
	i.mu.Unlock()
	for _, object := range objects {
		handler.OnAdd(object, true)
	}
	return reg, nil
}

func (i *replayInformer) AddEventHandler(h toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.register(h)
}

func (i *replayInformer) AddEventHandlerWithResyncPeriod(h toolscache.ResourceEventHandler, _ time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.register(h)
}

func (i *replayInformer) AddEventHandlerWithOptions(h toolscache.ResourceEventHandler, _ toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.register(h)
}

func (i *replayInformer) RemoveEventHandler(handle toolscache.ResourceEventHandlerRegistration) error {
	if reg, ok := handle.(*registration); ok {
		i.mu.Lock()
		delete(i.handlers, reg)
		i.mu.Unlock()
	}
	return nil
}

func (i *replayInformer) handlerCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.handlers)
}

func (i *replayInformer) snapshot() []toolscache.ResourceEventHandler {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]toolscache.ResourceEventHandler, 0, len(i.handlers))
	for _, h := range i.handlers {
		out = append(out, h)
	}
	return out
}

func (i *replayInformer) emitAdd(object client.Object) {
	for _, h := range i.snapshot() {
		h.OnAdd(object, false)
	}
}

func (i *replayInformer) emitUpdate(old, updated client.Object) {
	for _, h := range i.snapshot() {
		h.OnUpdate(old, updated)
	}
}

// fakeCache serves replay informers backed by the project's fake client and
// records which kinds were requested.
type fakeCache struct {
	*informertest.FakeInformers
	scheme *runtime.Scheme
	source client.Reader

	mu        sync.Mutex
	informers map[schema.GroupVersionKind]*replayInformer
}

func (c *fakeCache) GetInformer(ctx context.Context, object client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	gvk, err := apiutil.GVKForObject(object, c.scheme)
	if err != nil {
		return nil, err
	}
	return c.informerFor(gvk)
}

func (c *fakeCache) GetInformerForKind(_ context.Context, gvk schema.GroupVersionKind, _ ...cache.InformerGetOption) (cache.Informer, error) {
	return c.informerFor(gvk)
}

func (c *fakeCache) informerFor(gvk schema.GroupVersionKind) (*replayInformer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if informer, ok := c.informers[gvk]; ok {
		return informer, nil
	}
	listObject, err := c.scheme.New(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err != nil {
		return nil, err
	}
	informer := &replayInformer{
		FakeInformer: controllertest.NewFakeInformer(),
		handlers:     map[*registration]toolscache.ResourceEventHandler{},
		list: func() []client.Object {
			list := listObject.DeepCopyObject().(client.ObjectList)
			if err := c.source.List(context.Background(), list); err != nil {
				return nil
			}
			items, _ := apimeta.ExtractList(list)
			out := make([]client.Object, 0, len(items))
			for _, item := range items {
				out = append(out, item.(client.Object))
			}
			return out
		},
	}
	c.informers[gvk] = informer
	return informer, nil
}

func (c *fakeCache) requestedKinds() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	kinds := map[string]bool{}
	for gvk := range c.informers {
		kinds[gvk.Kind] = true
	}
	return kinds
}

type noopIndexer struct{}

func (noopIndexer) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return nil
}

// fakeCluster is one engaged project. Unimplemented methods panic through the
// nil embedded interface, so an unexpected dependency fails the test loudly.
type fakeCluster struct {
	cluster.Cluster
	client client.Client
	cache  *fakeCache
	config *rest.Config
	scheme *runtime.Scheme
}

func (c *fakeCluster) GetClient() client.Client             { return c.client }
func (c *fakeCluster) GetCache() cache.Cache                { return c.cache }
func (c *fakeCluster) GetFieldIndexer() client.FieldIndexer { return noopIndexer{} }
func (c *fakeCluster) GetConfig() *rest.Config              { return c.config }
func (c *fakeCluster) GetScheme() *runtime.Scheme           { return c.scheme }
func (c *fakeCluster) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// project is a synthetic Milo project control plane.
type project struct {
	name   multicluster.ClusterName
	client client.WithWatch
	writes atomic.Int64
	reads  sync.Map // machine name -> *atomic.Int64
}

type projectOption func(*interceptor.Funcs)

func newProject(t *testing.T, name string, objects []client.Object, options ...projectOption) *project {
	t.Helper()
	p := &project{name: multicluster.ClusterName(name)}
	funcs := interceptor.Funcs{}
	for _, option := range options {
		option(&funcs)
	}
	get := funcs.Get
	funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
		if _, ok := object.(*fleetv1.Machine); ok {
			counter, _ := p.reads.LoadOrStore(key.Name, &atomic.Int64{})
			counter.(*atomic.Int64).Add(1)
		}
		if get != nil {
			return get(ctx, c, key, object, opts...)
		}
		return c.Get(ctx, key, object, opts...)
	}
	update := funcs.SubResourceUpdate
	funcs.SubResourceUpdate = func(ctx context.Context, c client.Client, subResource string, object client.Object, opts ...client.SubResourceUpdateOption) error {
		p.writes.Add(1)
		if update != nil {
			return update(ctx, c, subResource, object, opts...)
		}
		return c.SubResource(subResource).Update(ctx, object, opts...)
	}
	p.client = fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&fleetv1.Machine{}).
		WithIndex(&fleetv1.Machine{}, IndexMachineProfileName, IndexMachineProfile).
		WithObjects(objects...).
		WithInterceptorFuncs(funcs).
		Build()
	return p
}

func (p *project) machineReads(name string) int64 {
	counter, ok := p.reads.Load(name)
	if !ok {
		return 0
	}
	return counter.(*atomic.Int64).Load()
}

func (p *project) newCluster(t *testing.T) *fakeCluster {
	scheme := testScheme(t)
	return &fakeCluster{
		client: p.client,
		cache:  &fakeCache{FakeInformers: &informertest.FakeInformers{Scheme: scheme}, scheme: scheme, source: p.client, informers: map[schema.GroupVersionKind]*replayInformer{}},
		config: &rest.Config{Host: "https://milo.example.test/projects/" + string(p.name)},
		scheme: scheme,
	}
}

func (p *project) machine(t *testing.T, name string) *fleetv1.Machine {
	t.Helper()
	var machine fleetv1.Machine
	if err := p.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &machine); err != nil {
		t.Fatal(err)
	}
	return &machine
}

// testProvider models the consumer provider's contract: engagement publishes
// the cluster to Get before Engage, and disengagement removes it from Get and
// then cancels the engagement context.
type testProvider struct {
	starts atomic.Int64

	mu      sync.Mutex
	aware   multicluster.Aware
	started chan struct{}
	engaged map[multicluster.ClusterName]*engagement
}

type engagement struct {
	cluster *fakeCluster
	cancel  context.CancelFunc
}

func newTestProvider() *testProvider {
	return &testProvider{started: make(chan struct{}), engaged: map[multicluster.ClusterName]*engagement{}}
}

func (p *testProvider) Start(ctx context.Context, aware multicluster.Aware) error {
	if p.starts.Add(1) == 1 {
		p.mu.Lock()
		p.aware = aware
		p.mu.Unlock()
		close(p.started)
	}
	<-ctx.Done()
	return nil
}

func (p *testProvider) Get(_ context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.engaged[name]; ok {
		return e.cluster, nil
	}
	return nil, fmt.Errorf("project %q not engaged: %w", name, multicluster.ErrClusterNotFound)
}

func (p *testProvider) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return nil
}

func (p *testProvider) engage(t *testing.T, ctx context.Context, proj *project) *fakeCluster {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider was never started")
	}
	cl := proj.newCluster(t)
	engageCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.engaged[proj.name] = &engagement{cluster: cl, cancel: cancel}
	aware := p.aware
	p.mu.Unlock()
	if err := aware.Engage(engageCtx, proj.name, cl); err != nil {
		t.Fatal(err)
	}
	return cl
}

func (p *testProvider) disengage(name multicluster.ClusterName) {
	p.mu.Lock()
	e := p.engaged[name]
	delete(p.engaged, name)
	p.mu.Unlock()
	if e != nil {
		e.cancel()
	}
}

type resolver struct {
	manager mcmanager.Manager
	tracker *EngageTracker
	cancel  context.CancelFunc
	done    chan error
}

// startResolver runs the production wiring (NewManager, Setup, health
// checks) over provider. The host config is never contacted: leader
// election, metrics, and probes are disabled and nothing watches the host.
func startResolver(t *testing.T, provider multicluster.Provider, discovery Discovery) *resolver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	mgr, tracker, err := NewManager(&rest.Config{Host: "https://host.example.test"}, provider, manager.Options{
		Scheme:                 testScheme(t),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := Setup(ctx, mgr); err != nil {
		cancel()
		t.Fatal(err)
	}
	if discovery == nil {
		discovery = StaticDiscovery{}
	}
	if err := AddHealthChecks(mgr, discovery, tracker); err != nil {
		cancel()
		t.Fatal(err)
	}
	r := &resolver{manager: mgr, tracker: tracker, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- mgr.Start(ctx) }()
	t.Cleanup(r.stop)
	return r
}

func (r *resolver) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
	}
}

func eventually(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// consistently asserts condition holds for a settling period, for negative
// assertions about work that must not happen.
func consistently(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !condition() {
			t.Fatalf("%s did not hold", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func machine(name, profile string) *fleetv1.Machine {
	return &fleetv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID("uid-machine-" + name), Generation: 1},
		Spec:       fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "standard"}, MachineProfileRef: fleetv1.LocalReference{Name: profile}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"},
	}
}

func profile(name string, generation, cores int64, memory string) *fleetv1.MachineProfile {
	return &fleetv1.MachineProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-profile-" + name), Generation: generation},
		Spec:       fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: cores}, Memory: fleetv1.MemoryRequirement{Capacity: memory}},
	}
}

func resolvedCondition(m *fleetv1.Machine) *metav1.Condition {
	return apimeta.FindStatusCondition(m.Status.Conditions, controller.ConditionRequestResolved)
}

func hasReason(p *project, t *testing.T, name, reason string) func() bool {
	return func() bool {
		c := resolvedCondition(p.machine(t, name))
		return c != nil && c.Reason == reason
	}
}

func pinnedCores(p *project, t *testing.T, name string, cores int64) func() bool {
	return func() bool {
		snapshot := p.machine(t, name).Status.RequestedHardware
		return snapshot != nil && snapshot.CPU.Cores == cores
	}
}

func requestFor(object client.Object) reconcile.Request {
	return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(object)}
}

// recordingManager counts runnables added through the multicluster manager,
// which is how mcbuilder registers controllers.
type recordingManager struct {
	mcmanager.Manager
	added int
}

func (m *recordingManager) Add(r mcmanager.Runnable) error {
	m.added++
	return m.Manager.Add(r)
}

type clusterGetterFunc func(context.Context, multicluster.ClusterName) error

func (f clusterGetterFunc) GetCluster(ctx context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	return nil, f(ctx, name)
}
