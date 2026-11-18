// Command fleet-request-resolver pins Machine status.requestedHardware and the
// RequestResolved condition in every project that consumes the Bare Metal
// service. It runs only that projection: no lifecycle controllers, private
// namespace, provider, quota, network, or entitlement integration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsingle "sigs.k8s.io/multicluster-runtime/providers/single"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/requestresolver"
	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

// singleClusterName is the cluster name registered in single mode.
const singleClusterName multicluster.ClusterName = "single"

type options struct {
	configPath              string
	probeAddr               string
	metricsAddr             string
	leaderElect             bool
	leaderElectionNamespace string
	leaderElectionID        string
}

func bindFlags(flags *flag.FlagSet) *options {
	o := &options{}
	flags.StringVar(&o.configPath, "config", "", "path to the RequestResolver configuration file")
	flags.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "address for /healthz and /readyz")
	flags.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "address for /metrics; 0 disables")
	flags.BoolVar(&o.leaderElect, "leader-elect", true, "hold a host-cluster lease so only one replica writes Machine status")
	flags.StringVar(&o.leaderElectionNamespace, "leader-election-namespace", "", "host-cluster namespace for the leader lease")
	flags.StringVar(&o.leaderElectionID, "leader-election-id", "fleet-request-resolver.fleet.datumapis.com", "leader lease name")
	return o
}

func main() {
	opts := bindFlags(flag.CommandLine)
	zapOptions := zap.Options{Development: false}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	if err := run(ctrl.SetupSignalHandler(), opts); err != nil {
		ctrl.Log.Error(err, "fleet request resolver failed")
		os.Exit(1)
	}
}

func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, fleetv1.AddToScheme, servicesv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return scheme, nil
}

func run(ctx context.Context, opts *options) error {
	if opts.leaderElect && opts.leaderElectionNamespace == "" {
		return errors.New("--leader-election-namespace is required when --leader-elect is true")
	}
	config, err := requestresolver.LoadConfig(opts.configPath)
	if err != nil {
		return err
	}
	scheme, err := newScheme()
	if err != nil {
		return err
	}
	// The host cluster holds the leader lease and, only in single mode, the
	// one served project. It is never used as a Milo endpoint.
	hostConfig, err := ctrl.GetConfig()
	if err != nil {
		return err
	}

	var (
		provider  multicluster.Provider
		discovery requestresolver.Discovery
		// hostCluster is set only in single mode; its cache runs in the
		// multicluster manager's always-running cache group.
		hostCluster cluster.Cluster
		// providerManager is set only in milo mode. It watches
		// ServiceConsumers on every replica and runs beside the manager, as
		// Compute runs it.
		providerManager manager.Manager
	)
	switch config.Discovery.Mode {
	case multiclusterproviders.ProviderSingle:
		hostCluster, err = cluster.New(hostConfig, func(o *cluster.Options) { o.Scheme = scheme }, requestresolver.ProjectCacheOptions)
		if err != nil {
			return err
		}
		provider = mcsingle.New(singleClusterName, hostCluster)
		discovery = requestresolver.StaticDiscovery{singleClusterName}
	case multiclusterproviders.ProviderMilo:
		discoveryConfig, err := clientcmd.BuildConfigFromFlags("", config.Discovery.DiscoveryKubeconfigPath)
		if err != nil {
			return fmt.Errorf("load discovery kubeconfig: %w", err)
		}
		projectConfig, err := clientcmd.BuildConfigFromFlags("", config.Discovery.ProjectKubeconfigPath)
		if err != nil {
			return fmt.Errorf("load project kubeconfig: %w", err)
		}
		consumers, err := requestresolver.NewConsumerDiscovery(discoveryConfig, projectConfig, *config.Discovery.ConsumerScopedProjection, scheme)
		if err != nil {
			return err
		}
		provider, discovery, providerManager = consumers.Provider, consumers, consumers.ProviderManager
	default:
		return fmt.Errorf("unsupported discovery mode %q", config.Discovery.Mode)
	}

	mgr, tracker, err := requestresolver.NewManager(hostConfig, provider, manager.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress:  opts.probeAddr,
		LeaderElection:          opts.leaderElect,
		LeaderElectionID:        opts.leaderElectionID,
		LeaderElectionNamespace: opts.leaderElectionNamespace,
		// Exit promptly on lease loss rather than keep writing as a stale
		// leader; the process ends right after the manager stops.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return err
	}
	if hostCluster != nil {
		if err := mgr.GetLocalManager().Add(hostCluster); err != nil {
			return err
		}
	}
	if err := requestresolver.Setup(ctx, mgr); err != nil {
		return err
	}
	if err := requestresolver.AddHealthChecks(mgr, discovery, tracker); err != nil {
		return err
	}

	group, ctx := errgroup.WithContext(ctx)
	if providerManager != nil {
		group.Go(func() error { return ignoreCanceled(providerManager.Start(ctx)) })
	}
	group.Go(func() error { return ignoreCanceled(mgr.Start(ctx)) })
	return group.Wait()
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
