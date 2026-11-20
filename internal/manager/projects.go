package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"go.datum.net/fleet/internal/controller"
)

// ErrProjectConflict reports two engaged clusters claiming one project
// lifetime. The second engagement is refused rather than allowed to share
// private custody.
var ErrProjectConflict = errors.New("project lifetime is already engaged under another cluster name")

// ProjectBinding is what the handler factory receives for one engaged
// project: its cluster name, its immutable identity, and the client bound to
// that project's control plane. Nothing in it reaches another project.
type ProjectBinding struct {
	ClusterName multicluster.ClusterName
	Project     controller.ProjectIdentity
	Client      client.Client
}

// HandlerFactory builds the lifecycle and projector handlers for one engaged
// project. It is called once per engagement.
type HandlerFactory func(ProjectBinding) (Handlers, error)

// ProjectRuntime is everything Fleet holds for one engaged project. Every
// reconcile for the project goes through exactly one runtime.
type ProjectRuntime struct {
	Binding      ProjectBinding
	Reader       client.Reader
	Verifier     ProjectVerifier
	Mappers      Mappers
	Machine      *LifecycleOwner
	Installation *LifecycleOwner

	reconcileMu sync.RWMutex
	stopped     bool
	drainOnce   sync.Once
	drained     chan struct{}
}

func (r *ProjectRuntime) acquire() bool {
	r.reconcileMu.RLock()
	if r.stopped {
		r.reconcileMu.RUnlock()
		return false
	}
	return true
}

func (r *ProjectRuntime) stop() {
	<-r.drain()
}

func (r *ProjectRuntime) drain() <-chan struct{} {
	r.drainOnce.Do(func() {
		r.drained = make(chan struct{})
		go func() {
			r.reconcileMu.Lock()
			r.stopped = true
			r.reconcileMu.Unlock()
			close(r.drained)
		}()
	})
	return r.drained
}

// ProjectResolver resolves an engaged cluster name to its immutable project
// identity (from Milo; never from configuration or a consumer object).
type ProjectResolver func(context.Context, multicluster.ClusterName) (controller.ProjectIdentity, error)

// Projects is the registry of engaged project runtimes. It is the
// multicluster.Aware that project discovery engages through: a project's
// identity and runtime are bound before its controllers engage, and unbound
// when the engagement context ends.
type Projects struct {
	resolve     ProjectResolver
	verifierFor func(controller.ProjectIdentity) ProjectVerifier
	build       func(ProjectBinding, client.Reader, ProjectVerifier) (*ProjectRuntime, error)

	mu        sync.RWMutex
	aware     multicluster.Aware
	byCluster map[multicluster.ClusterName]*ProjectRuntime
	byProject map[projectKey]multicluster.ClusterName
}

type projectKey struct {
	authorityID string
	uid         types.UID
}

func newProjects(resolve ProjectResolver, verifierFor func(controller.ProjectIdentity) ProjectVerifier, build func(ProjectBinding, client.Reader, ProjectVerifier) (*ProjectRuntime, error)) *Projects {
	return &Projects{resolve: resolve, verifierFor: verifierFor, build: build, byCluster: map[multicluster.ClusterName]*ProjectRuntime{}, byProject: map[projectKey]multicluster.ClusterName{}}
}

// bindAware sets the downstream Aware (the multicluster manager).
func (p *Projects) bindAware(aware multicluster.Aware) {
	p.mu.Lock()
	p.aware = aware
	p.mu.Unlock()
}

// Engage resolves the project's identity, builds its runtime, registers it,
// and only then engages the controllers. Replacement drains the prior runtime
// first. Failure removes only this engagement's registration; a prior drain is
// not reversed. Errors are returned so discovery retries.
func (p *Projects) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	p.mu.RLock()
	aware := p.aware
	p.mu.RUnlock()
	if aware == nil || cl == nil || name == "" {
		return ErrEventWiringUnavailable
	}
	identity, err := p.resolve(ctx, name)
	if err != nil {
		return fmt.Errorf("resolve identity of project %q: %w", name, err)
	}
	if !identity.Valid() || identity.RouteName != string(name) {
		return fmt.Errorf("%w: project %q resolved to %q", ErrProjectIdentityUnavailable, name, identity.RouteName)
	}
	binding := ProjectBinding{ClusterName: name, Project: identity, Client: cl.GetClient()}
	runtime, err := p.build(binding, cl.GetCache(), p.verifierFor(identity))
	if err != nil {
		return err
	}
	key := projectKey{identity.AuthorityID, identity.UID}
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return err
		}
		if existing, ok := p.byProject[key]; ok && existing != name {
			p.mu.Unlock()
			return fmt.Errorf("%w: %q and %q", ErrProjectConflict, existing, name)
		}
		previous := p.byCluster[name]
		if previous != nil {
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				go p.unbind(name, previous)
				return ctx.Err()
			case <-previous.drain():
			}
			p.mu.Lock()
			if p.byCluster[name] != previous {
				p.mu.Unlock()
				continue
			}
			if err := ctx.Err(); err != nil {
				p.mu.Unlock()
				p.unbind(name, previous)
				return err
			}
			if existing, ok := p.byProject[key]; ok && existing != name {
				p.mu.Unlock()
				return fmt.Errorf("%w: %q and %q", ErrProjectConflict, existing, name)
			}
			delete(p.byProject, projectKey{previous.Binding.Project.AuthorityID, previous.Binding.Project.UID})
		}
		p.byCluster[name] = runtime
		p.byProject[key] = name
		p.mu.Unlock()
		break
	}

	if err := aware.Engage(ctx, name, cl); err != nil {
		p.unbind(name, runtime)
		return err
	}
	go func() {
		<-ctx.Done()
		p.unbind(name, runtime)
	}()
	return nil
}

func (p *Projects) unbind(name multicluster.ClusterName, runtime *ProjectRuntime) {
	runtime.stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byCluster[name] != runtime {
		return
	}
	delete(p.byCluster, name)
	key := projectKey{runtime.Binding.Project.AuthorityID, runtime.Binding.Project.UID}
	if p.byProject[key] == name {
		delete(p.byProject, key)
	}
}

// Get returns the runtime for an engaged cluster, or nil when the project is
// not (or no longer) engaged.
func (p *Projects) Get(name multicluster.ClusterName) *ProjectRuntime {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byCluster[name]
}

// ForProject returns the runtime engaged for an exact project lifetime, or nil.
func (p *Projects) ForProject(authorityID string, uid types.UID) *ProjectRuntime {
	p.mu.RLock()
	defer p.mu.RUnlock()
	name, ok := p.byProject[projectKey{authorityID, uid}]
	if !ok {
		return nil
	}
	return p.byCluster[name]
}

// For returns the runtime that owns request: the one engaged under its
// cluster name whose identity matches the request's project. Anything else
// returns nil and the request is dropped without effect.
func (p *Projects) For(request ProjectRequest) *ProjectRuntime {
	runtime := p.Get(request.ClusterName)
	if runtime == nil || !request.SameProject(runtime.Binding.Project.AuthorityID, runtime.Binding.Project.UID) {
		return nil
	}
	return runtime
}

// BlockedHandler performs no lifecycle work: no allocation, installation,
// provider call, custody write, or status change. It is the lifecycle handler
// whenever no production or synthetic integration is configured, so missing
// integrations stay inert rather than reporting readiness.
var BlockedHandler = HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} })

// DefaultHandlers is the handler set for a project with no lifecycle
// integration: blocked lifecycle handlers and the requested-hardware
// projector, which runs in every configuration.
func DefaultHandlers(binding ProjectBinding) (Handlers, error) {
	if binding.Client == nil || !binding.Project.Valid() {
		return Handlers{}, ErrEventWiringUnavailable
	}
	return Handlers{
		Machine:               BlockedHandler,
		Installation:          BlockedHandler,
		MachineProjector:      RequestedHardwareProjector{Client: binding.Client, Project: binding.Project},
		InstallationProjector: BlockedHandler,
	}, nil
}
