package projectdiscovery

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsingle "sigs.k8s.io/multicluster-runtime/providers/single"

	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"
	consumerprovider "go.miloapis.com/service-catalog/pkg/multicluster-runtime/consumer"
)

// Selection is the configured project discovery.
type Selection struct {
	Provider  multicluster.Provider
	Discovery Discovery
	// RootConfig reaches the root Milo API (Services, Projects).
	RootConfig *rest.Config
	// ProjectConfig is the project kubeconfig template.
	ProjectConfig *rest.Config
	// LocalRunnables must be added to the multicluster manager's local
	// manager (the single-mode project cache).
	LocalRunnables []manager.Runnable
	// SideRunnables run beside the multicluster manager on every replica (the
	// provider-project ServiceConsumer manager).
	SideRunnables []manager.Runnable
}

// Select builds the provider for cfg. It performs no project reads; engaged
// projects' identities are resolved when they engage.
func Select(cfg Config, scheme *runtime.Scheme) (*Selection, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rootConfig, err := clientcmd.BuildConfigFromFlags("", cfg.Discovery.DiscoveryKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("load discovery kubeconfig: %w", err)
	}
	projectConfig, err := clientcmd.BuildConfigFromFlags("", cfg.Discovery.ProjectKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("load project kubeconfig: %w", err)
	}
	selection := &Selection{RootConfig: rootConfig, ProjectConfig: projectConfig}
	switch cfg.Discovery.Mode {
	case multiclusterproviders.ProviderSingle:
		routed, err := consumerprovider.ProjectRestConfig(projectConfig, cfg.Discovery.Project)
		if err != nil {
			return nil, err
		}
		project, err := cluster.New(routed, func(o *cluster.Options) { o.Scheme = scheme }, ProjectCacheOptions)
		if err != nil {
			return nil, err
		}
		name := multicluster.ClusterName(cfg.Discovery.Project)
		selection.Provider = mcsingle.New(name, project)
		selection.Discovery = StaticDiscovery{name}
		selection.LocalRunnables = []manager.Runnable{project}
	case multiclusterproviders.ProviderMilo:
		consumers, err := NewConsumerDiscovery(rootConfig, projectConfig, *cfg.Discovery.ConsumerScopedProjection, scheme)
		if err != nil {
			return nil, err
		}
		selection.Provider = consumers.Provider
		selection.Discovery = consumers
		selection.SideRunnables = []manager.Runnable{consumers.ProviderManager}
	default:
		return nil, fmt.Errorf("%w: unsupported discovery mode %q", ErrInvalidConfig, cfg.Discovery.Mode)
	}
	return selection, nil
}

// ProjectRESTConfig re-addresses base to the named project's control plane.
func ProjectRESTConfig(base *rest.Config, project string) (*rest.Config, error) {
	return consumerprovider.ProjectRestConfig(base, project)
}
