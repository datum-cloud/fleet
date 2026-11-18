// Package multiclustertest provides fake project clusters and a fake
// multicluster provider for tests. A fake cluster's cache reads through a fake
// client and its informers deliver the current objects to every new handler,
// as a real informer's initial list does. It is test support only and is
// never linked into a binary.
package multiclustertest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Informer delivers the current objects to every newly registered handler
// and removes handlers when asked, as a disengaged source requests.
type Informer struct {
	*controllertest.FakeInformer
	list func() []client.Object

	mu       sync.Mutex
	handlers map[*registration]toolscache.ResourceEventHandler
}

type registration struct {
	toolscache.ResourceEventHandlerRegistration
}

func (i *Informer) register(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	// The embedded fake informer is not goroutine-safe; it is only used for a
	// valid registration value, under this informer's lock.
	i.mu.Lock()
	base, err := i.FakeInformer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{})
	if err != nil {
		i.mu.Unlock()
		return nil, err
	}
	reg := &registration{base}
	i.handlers[reg] = handler
	objects := i.list()
	i.mu.Unlock()
	for _, object := range objects {
		handler.OnAdd(object, true)
	}
	return reg, nil
}

func (i *Informer) AddEventHandler(h toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.register(h)
}

func (i *Informer) AddEventHandlerWithResyncPeriod(h toolscache.ResourceEventHandler, _ time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.register(h)
}

func (i *Informer) AddEventHandlerWithOptions(h toolscache.ResourceEventHandler, _ toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.register(h)
}

func (i *Informer) RemoveEventHandler(handle toolscache.ResourceEventHandlerRegistration) error {
	if reg, ok := handle.(*registration); ok {
		i.mu.Lock()
		delete(i.handlers, reg)
		i.mu.Unlock()
	}
	return nil
}

// HandlerCount is the number of registered handlers.
func (i *Informer) HandlerCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.handlers)
}

func (i *Informer) snapshot() []toolscache.ResourceEventHandler {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]toolscache.ResourceEventHandler, 0, len(i.handlers))
	for _, h := range i.handlers {
		out = append(out, h)
	}
	return out
}

// EmitAdd delivers an Add event to every registered handler.
func (i *Informer) EmitAdd(object client.Object) {
	for _, h := range i.snapshot() {
		h.OnAdd(object, false)
	}
}

// EmitUpdate delivers an Update event to every registered handler.
func (i *Informer) EmitUpdate(old, updated client.Object) {
	for _, h := range i.snapshot() {
		h.OnUpdate(old, updated)
	}
}

// EmitDelete delivers a Delete event to every registered handler.
func (i *Informer) EmitDelete(object client.Object) {
	for _, h := range i.snapshot() {
		h.OnDelete(object)
	}
}

// Cache serves replay informers and reads through the backing client.
type Cache struct {
	*informertest.FakeInformers
	scheme *runtime.Scheme
	source client.Reader

	mu        sync.Mutex
	informers map[schema.GroupVersionKind]*Informer
}

func (c *Cache) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	return c.source.Get(ctx, key, object, opts...)
}

func (c *Cache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.source.List(ctx, list, opts...)
}

func (c *Cache) GetInformer(_ context.Context, object client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	gvk, err := apiutil.GVKForObject(object, c.scheme)
	if err != nil {
		return nil, err
	}
	return c.InformerFor(gvk)
}

func (c *Cache) GetInformerForKind(_ context.Context, gvk schema.GroupVersionKind, _ ...cache.InformerGetOption) (cache.Informer, error) {
	return c.InformerFor(gvk)
}

// InformerFor returns (creating if needed) the informer for gvk.
func (c *Cache) InformerFor(gvk schema.GroupVersionKind) (*Informer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if informer, ok := c.informers[gvk]; ok {
		return informer, nil
	}
	listGVK := gvk.GroupVersion().WithKind(gvk.Kind + "List")
	newList := func() client.ObjectList {
		if listObject, err := c.scheme.New(listGVK); err == nil {
			return listObject.(client.ObjectList)
		}
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listGVK)
		return list
	}
	informer := &Informer{
		FakeInformer: controllertest.NewFakeInformer(controllertest.Synced),
		handlers:     map[*registration]toolscache.ResourceEventHandler{},
		list: func() []client.Object {
			list := newList()
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

// RequestedKinds lists the kinds whose informers were requested.
func (c *Cache) RequestedKinds() map[string]bool {
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

// Cluster is one fake API server. Unimplemented methods panic through the nil
// embedded interface, so an unexpected dependency fails the test loudly.
type Cluster struct {
	cluster.Cluster
	FakeCache *Cache
	client    client.Client
	config    *rest.Config
	scheme    *runtime.Scheme
}

func (c *Cluster) GetClient() client.Client { return c.client }
func (c *Cluster) GetCache() cache.Cache    { return c.FakeCache }

// GetFieldIndexer is a no-op: indexes are installed on the backing client
// when the Project is built (see Index).
func (c *Cluster) GetFieldIndexer() client.FieldIndexer { return noopIndexer{} }
func (c *Cluster) GetConfig() *rest.Config              { return c.config }
func (c *Cluster) GetScheme() *runtime.Scheme           { return c.scheme }
func (c *Cluster) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Index is a field index to install on a Project's backing client.
type Index struct {
	Object client.Object
	Field  string
	Fn     client.IndexerFunc
}

// IndexCollector is a client.FieldIndexer that records registrations, so a
// production index registration function can populate a fake client.
type IndexCollector struct{ Indexes []Index }

func (c *IndexCollector) IndexField(_ context.Context, object client.Object, field string, fn client.IndexerFunc) error {
	c.Indexes = append(c.Indexes, Index{Object: object, Field: field, Fn: fn})
	return nil
}

// Project is a synthetic API server (a Milo project control plane or the
// management cluster) backed by a fake client.
type Project struct {
	Name   multicluster.ClusterName
	Client client.WithWatch
	scheme *runtime.Scheme
	writes atomic.Int64
	uids   atomic.Int64
	reads  sync.Map // machine name -> *atomic.Int64

	cachesMu sync.Mutex
	caches   []*Cache
}

// deliver emits the watch event a real API server would send for a
// completed write to every cache built over this project.
func (p *Project) deliver(old, current client.Object) {
	p.cachesMu.Lock()
	caches := append([]*Cache(nil), p.caches...)
	p.cachesMu.Unlock()
	object := current
	if object == nil {
		object = old
	}
	gvk, err := apiutil.GVKForObject(object, p.scheme)
	if err != nil {
		return
	}
	for _, c := range caches {
		c.mu.Lock()
		informer := c.informers[gvk]
		c.mu.Unlock()
		if informer == nil {
			continue
		}
		switch {
		case current == nil:
			informer.EmitDelete(old)
		case old == nil:
			informer.EmitAdd(current)
		default:
			informer.EmitUpdate(old, current)
		}
	}
}

// snapshot reads the stored object with object's kind and key, or nil.
func (p *Project) snapshot(ctx context.Context, c client.Client, object client.Object) client.Object {
	copied, ok := object.DeepCopyObject().(client.Object)
	if !ok {
		return nil
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(object), copied); err != nil {
		return nil
	}
	return copied
}

// observe wraps a write: it captures the stored object before, runs the
// write, and on success delivers the resulting event.
func (p *Project) observe(ctx context.Context, c client.Client, object client.Object, write func() error) error {
	p.writes.Add(1)
	old := p.snapshot(ctx, c, object)
	if err := write(); err != nil {
		return err
	}
	current := p.snapshot(ctx, c, object)
	if current == nil {
		if probe, ok := object.DeepCopyObject().(client.Object); ok {
			if err := c.Get(ctx, client.ObjectKeyFromObject(object), probe); !apierrors.IsNotFound(err) {
				return nil
			}
		}
	}
	if old == nil && current == nil {
		return nil
	}
	p.deliver(old, current)
	return nil
}

// ProjectOptions configures NewProject.
type ProjectOptions struct {
	Scheme             *runtime.Scheme
	Objects            []client.Object
	StatusSubresources []client.Object
	Indexes            []Index
	Funcs              interceptor.Funcs
	// CountReadsOf counts Gets of this kind by object name.
	CountReadsOf client.Object
}

func NewProject(t *testing.T, name string, options ProjectOptions) *Project {
	t.Helper()
	p := &Project{Name: multicluster.ClusterName(name), scheme: options.Scheme}
	funcs := options.Funcs
	countGVK := schema.GroupVersionKind{}
	if options.CountReadsOf != nil {
		gvk, err := apiutil.GVKForObject(options.CountReadsOf, options.Scheme)
		if err != nil {
			t.Fatal(err)
		}
		countGVK = gvk
	}
	get := funcs.Get
	funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
		if gvk, err := apiutil.GVKForObject(object, options.Scheme); err == nil && gvk == countGVK {
			counter, _ := p.reads.LoadOrStore(key.Name, &atomic.Int64{})
			counter.(*atomic.Int64).Add(1)
		}
		if get != nil {
			return get(ctx, c, key, object, opts...)
		}
		return c.Get(ctx, key, object, opts...)
	}
	wrap := func(funcs *interceptor.Funcs) {
		create, update, patch, del, subUpdate, subPatch := funcs.Create, funcs.Update, funcs.Patch, funcs.Delete, funcs.SubResourceUpdate, funcs.SubResourcePatch
		funcs.Create = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			// Like an API server, assign a UID and initial generation.
			if o.GetUID() == "" {
				o.SetUID(types.UID(fmt.Sprintf("%s-uid-%d", p.Name, p.uids.Add(1))))
			}
			if o.GetGeneration() == 0 {
				o.SetGeneration(1)
			}
			return p.observe(ctx, c, o, func() error {
				if create != nil {
					return create(ctx, c, o, opts...)
				}
				return c.Create(ctx, o, opts...)
			})
		}
		funcs.Update = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			return p.observe(ctx, c, o, func() error {
				if update != nil {
					return update(ctx, c, o, opts...)
				}
				return c.Update(ctx, o, opts...)
			})
		}
		funcs.Patch = func(ctx context.Context, c client.WithWatch, o client.Object, patchValue client.Patch, opts ...client.PatchOption) error {
			return p.observe(ctx, c, o, func() error {
				if patch != nil {
					return patch(ctx, c, o, patchValue, opts...)
				}
				return c.Patch(ctx, o, patchValue, opts...)
			})
		}
		funcs.Delete = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			return p.observe(ctx, c, o, func() error {
				if del != nil {
					return del(ctx, c, o, opts...)
				}
				return c.Delete(ctx, o, opts...)
			})
		}
		funcs.SubResourceUpdate = func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			return p.observe(ctx, c, o, func() error {
				if subUpdate != nil {
					return subUpdate(ctx, c, sub, o, opts...)
				}
				return c.SubResource(sub).Update(ctx, o, opts...)
			})
		}
		funcs.SubResourcePatch = func(ctx context.Context, c client.Client, sub string, o client.Object, patchValue client.Patch, opts ...client.SubResourcePatchOption) error {
			return p.observe(ctx, c, o, func() error {
				if subPatch != nil {
					return subPatch(ctx, c, sub, o, patchValue, opts...)
				}
				return c.SubResource(sub).Patch(ctx, o, patchValue, opts...)
			})
		}
	}
	wrap(&funcs)
	builder := fake.NewClientBuilder().WithScheme(options.Scheme).WithStatusSubresource(options.StatusSubresources...).WithObjects(options.Objects...).WithInterceptorFuncs(funcs)
	for _, index := range options.Indexes {
		builder = builder.WithIndex(index.Object, index.Field, index.Fn)
	}
	p.Client = builder.Build()
	return p
}

// Writes counts every write made through the project's client.
func (p *Project) Writes() int64 { return p.writes.Load() }

// Reads counts Gets of the configured kind by name.
func (p *Project) Reads(name string) int64 {
	counter, ok := p.reads.Load(name)
	if !ok {
		return 0
	}
	return counter.(*atomic.Int64).Load()
}

// NewCluster returns a fresh engagement-sized cluster over the project.
func (p *Project) NewCluster() *Cluster {
	fakeCache := &Cache{FakeInformers: &informertest.FakeInformers{Scheme: p.scheme}, scheme: p.scheme, source: p.Client, informers: map[schema.GroupVersionKind]*Informer{}}
	p.cachesMu.Lock()
	p.caches = append(p.caches, fakeCache)
	p.cachesMu.Unlock()
	return &Cluster{
		client:    p.Client,
		FakeCache: fakeCache,
		config:    &rest.Config{Host: "https://milo.example.test/projects/" + string(p.Name)},
		scheme:    p.scheme,
	}
}

// Provider models the service catalog consumer provider's contract:
// engagement publishes the cluster to Get before Engage, and disengagement
// removes it from Get and then cancels the engagement context.
type Provider struct {
	starts atomic.Int64

	mu      sync.Mutex
	aware   multicluster.Aware
	started chan struct{}
	engaged map[multicluster.ClusterName]*engagement
}

type engagement struct {
	cluster *Cluster
	cancel  context.CancelFunc
}

func NewProvider() *Provider {
	return &Provider{started: make(chan struct{}), engaged: map[multicluster.ClusterName]*engagement{}}
}

// Starts counts Start calls.
func (p *Provider) Starts() int64 { return p.starts.Load() }

func (p *Provider) Start(ctx context.Context, aware multicluster.Aware) error {
	if p.starts.Add(1) == 1 {
		p.mu.Lock()
		p.aware = aware
		p.mu.Unlock()
		close(p.started)
	}
	<-ctx.Done()
	return nil
}

func (p *Provider) Get(_ context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.engaged[name]; ok {
		return e.cluster, nil
	}
	return nil, fmt.Errorf("project %q not engaged: %w", name, multicluster.ErrClusterNotFound)
}

func (p *Provider) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return nil
}

// Engage engages project and fails the test on error.
func (p *Provider) Engage(t *testing.T, ctx context.Context, project *Project) *Cluster {
	t.Helper()
	cl, err := p.TryEngage(t, ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

// TryEngage engages project and returns the Aware's error, if any.
func (p *Provider) TryEngage(t *testing.T, ctx context.Context, project *Project) (*Cluster, error) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider was never started")
	}
	cl := project.NewCluster()
	engageCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.engaged[project.Name] = &engagement{cluster: cl, cancel: cancel}
	aware := p.aware
	p.mu.Unlock()
	if err := aware.Engage(engageCtx, project.Name, cl); err != nil {
		p.Disengage(project.Name)
		return nil, err
	}
	return cl, nil
}

// Disengage removes the project from Get and cancels its engagement.
func (p *Provider) Disengage(name multicluster.ClusterName) {
	p.mu.Lock()
	e := p.engaged[name]
	delete(p.engaged, name)
	p.mu.Unlock()
	if e != nil {
		e.cancel()
	}
}

// Eventually polls condition for up to ten seconds.
func Eventually(t *testing.T, description string, condition func() bool) {
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

// Consistently asserts condition holds for a settling period.
func Consistently(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !condition() {
			t.Fatalf("%s did not hold", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
