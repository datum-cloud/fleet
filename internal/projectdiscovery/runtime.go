package projectdiscovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// readinessProbeBudget bounds each readiness check so a slow cache cannot
// hang the kubelet probe.
const readinessProbeBudget = 2 * time.Second

// NewManager builds the fleet-manager's multicluster manager on the host cluster
// (where its leader-election lease lives) over provider.
//
// mcmanager.Start auto-adds a ProviderRunnable's Start as a plain
// manager.RunnableFunc. Such a runnable does not implement
// NeedLeaderElection, so controller-runtime routes it into the
// leader-election group, which would leave every non-leader replica with no
// engaged project and therefore permanently unready (datum-cloud/compute#117).
// Project engagement is per-pod cache bookkeeping, like the manager's own
// informer cache, and must run on every replica so a standby is warm and
// ready to take the lease. NewManager therefore hides Start from mcmanager
// for every ProviderRunnable (not only Milo's), re-adds it exactly once in the
// always-running group, and routes engagement through the returned tracker
// so readiness can require real engagement.
//
// Controllers stay leader-gated: only the lease holder reconciles.
//
// engage, when set, receives the manager and returns the Aware that project
// engagement goes through (for example the manager's project registry, which
// must bind a project's identity before its controllers engage). The tracker
// wraps it, so a project counts as engaged only after that Aware accepted it.
func NewManager(hostConfig *rest.Config, provider multicluster.Provider, options manager.Options, engage func(mcmanager.Manager) multicluster.Aware) (mcmanager.Manager, *EngageTracker, error) {
	if hostConfig == nil || provider == nil {
		return nil, nil, errors.New("fleet-manager requires a host config and a multicluster provider")
	}
	exposed := provider
	runnable, isRunnable := provider.(multicluster.ProviderRunnable)
	if isRunnable {
		exposed = providerOnly{provider}
	}
	mgr, err := mcmanager.New(hostConfig, exposed, options)
	if err != nil {
		return nil, nil, err
	}
	var aware multicluster.Aware = mgr
	if engage != nil {
		aware = engage(mgr)
	}
	tracker := NewEngageTracker(aware)
	if isRunnable {
		if err := mgr.GetLocalManager().Add(nonLeaderElectionRunnable{Runnable: manager.RunnableFunc(func(ctx context.Context) error {
			return runnable.Start(ctx, tracker)
		})}); err != nil {
			return nil, nil, err
		}
	}
	return mgr, tracker, nil
}

// providerOnly exposes only multicluster.Provider, hiding Start so mcmanager
// does not auto-wire (and silently leader-gate) it.
type providerOnly struct{ multicluster.Provider }

// nonLeaderElectionRunnable forces a runnable into controller-runtime's
// always-running group regardless of leader-election state.
type nonLeaderElectionRunnable struct{ manager.Runnable }

func (nonLeaderElectionRunnable) NeedLeaderElection() bool { return false }

// EngageTracker wraps the manager's multicluster.Aware and records which
// project clusters completed Engage, forgetting each one when its engagement
// context ends (the provider disengaged it).
type EngageTracker struct {
	aware multicluster.Aware

	mu      sync.RWMutex
	engaged map[multicluster.ClusterName]cluster.Cluster
}

func NewEngageTracker(aware multicluster.Aware) *EngageTracker {
	return &EngageTracker{aware: aware, engaged: map[multicluster.ClusterName]cluster.Cluster{}}
}

func (t *EngageTracker) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	if err := t.aware.Engage(ctx, name, cl); err != nil {
		return err
	}
	t.mu.Lock()
	t.engaged[name] = cl
	t.mu.Unlock()
	go func() {
		<-ctx.Done()
		t.mu.Lock()
		if t.engaged[name] == cl {
			delete(t.engaged, name)
		}
		t.mu.Unlock()
	}()
	return nil
}

// Engaged reports whether name is currently engaged.
func (t *EngageTracker) Engaged(name multicluster.ClusterName) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.engaged[name]
	return ok
}

// Names returns the currently engaged project clusters, sorted.
func (t *EngageTracker) Names() []multicluster.ClusterName {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]multicluster.ClusterName, 0, len(t.engaged))
	for name := range t.engaged {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func (t *EngageTracker) snapshot() map[multicluster.ClusterName]cluster.Cluster {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[multicluster.ClusterName]cluster.Cluster, len(t.engaged))
	for name, cl := range t.engaged {
		out[name] = cl
	}
	return out
}

// Discovery describes the project set the manager should have engaged.
// Synced reports whether the discovery source itself has completed its
// initial sync; Expected lists the projects that must be engaged before the
// manager is ready. Neither depends on any lifecycle or provider integration.
type Discovery interface {
	Synced(context.Context) error
	Expected(context.Context) ([]multicluster.ClusterName, error)
}

// StaticDiscovery expects a fixed set of clusters (single mode and tests).
type StaticDiscovery []multicluster.ClusterName

func (StaticDiscovery) Synced(context.Context) error { return nil }

func (d StaticDiscovery) Expected(context.Context) ([]multicluster.ClusterName, error) {
	return append([]multicluster.ClusterName(nil), d...), nil
}

// NamedCache is an additional cache whose initial sync gates readiness, such
// as the manager's private-record cache.
type NamedCache struct {
	Name  string
	Cache interface{ WaitForCacheSync(context.Context) bool }
}

// AddHealthChecks registers liveness and the readiness boundary: discovery
// has synced, every expected project is engaged, every engaged project cache
// has synced, and every named cache has synced. Readiness never depends on a
// provider integration.
func AddHealthChecks(mgr mcmanager.Manager, discovery Discovery, tracker *EngageTracker, caches ...NamedCache) error {
	if mgr == nil || discovery == nil || tracker == nil {
		return errors.New("health checks require a manager, discovery, and engagement tracker")
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("discovery-synced", DiscoverySyncedCheck(discovery)); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("projects-engaged", ProjectsEngagedCheck(discovery, tracker)); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("project-caches-synced", EngagedCachesSyncedCheck(tracker)); err != nil {
		return err
	}
	for _, c := range caches {
		if c.Name == "" || c.Cache == nil {
			return errors.New("named readiness cache requires a name and a cache")
		}
		if err := mgr.AddReadyzCheck(c.Name+"-synced", CacheSyncedCheck(c)); err != nil {
			return err
		}
	}
	return nil
}

// CacheSyncedCheck fails until c has completed its initial sync.
func CacheSyncedCheck(c NamedCache) healthz.Checker {
	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), readinessProbeBudget)
		defer cancel()
		if !c.Cache.WaitForCacheSync(ctx) {
			return fmt.Errorf("%s cache has not synced", c.Name)
		}
		return nil
	}
}

func DiscoverySyncedCheck(discovery Discovery) healthz.Checker {
	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), readinessProbeBudget)
		defer cancel()
		return discovery.Synced(ctx)
	}
}

// ProjectsEngagedCheck fails while any project discovery expects is not yet
// engaged on this replica. A synced discovery cache alone only proves the
// project set is known, not that each project's connection is established.
func ProjectsEngagedCheck(discovery Discovery, tracker *EngageTracker) healthz.Checker {
	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), readinessProbeBudget)
		defer cancel()
		expected, err := discovery.Expected(ctx)
		if err != nil {
			return err
		}
		for _, name := range expected {
			if !tracker.Engaged(name) {
				return fmt.Errorf("project %q consumes the service but is not yet engaged", name)
			}
		}
		return nil
	}
}

// EngagedCachesSyncedCheck fails while any engaged project cache, including
// informers the controller added at engagement, has not synced.
func EngagedCachesSyncedCheck(tracker *EngageTracker) healthz.Checker {
	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), readinessProbeBudget)
		defer cancel()
		for name, cl := range tracker.snapshot() {
			if !cl.GetCache().WaitForCacheSync(ctx) {
				return fmt.Errorf("project %q cache has not synced", name)
			}
		}
		return nil
	}
}
