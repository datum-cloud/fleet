package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	eventmanager "go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/projectdiscovery"
	"go.datum.net/fleet/internal/staging"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

// leaseName is the fleet-manager's host-cluster leader lease.
const leaseName = "fleet-manager.fleet.datumapis.com"

type managerFlags struct {
	configPath, leaderElectionNamespace string

	syntheticStaging                                            bool
	syntheticProviderURL, syntheticTokenFile, syntheticLocation string
	syntheticConsumerProjectName                                string
	syntheticProviderProjectName, syntheticPublishedClassName   string
	syntheticPublishedProfileName                               string
	syntheticCoordinatorTokenFile, syntheticReleaseTokenFile    string
	syntheticEnrollmentFile, syntheticDeviceNamespace           string
}

func bindFlags(flags *flag.FlagSet) *managerFlags {
	f := &managerFlags{}
	flags.StringVar(&f.configPath, "config", "", "path to the FleetManager configuration file (authority and project discovery)")
	flags.StringVar(&f.leaderElectionNamespace, "leader-election-namespace", "", "management-cluster namespace for the leader lease")
	flags.BoolVar(&f.syntheticStaging, "synthetic-staging", false, "enable the explicit non-production synthetic allocation integration for one consumer project")
	flags.StringVar(&f.syntheticProviderURL, "synthetic-provider-url", "http://127.0.0.1:8080", "synthetic provider runtime URL")
	flags.StringVar(&f.syntheticTokenFile, "synthetic-provider-token-file", "", "file containing the synthetic provider site's bearer token")
	flags.StringVar(&f.syntheticCoordinatorTokenFile, "synthetic-coordinator-token-file", "", "file containing the custody coordinator's Milo token")
	flags.StringVar(&f.syntheticReleaseTokenFile, "synthetic-release-token-file", "", "file containing the release controller's Milo token")
	flags.StringVar(&f.syntheticEnrollmentFile, "synthetic-site-enrollment-file", "", "JSON enrollment of the Devices the synthetic provider site serves")
	flags.StringVar(&f.syntheticDeviceNamespace, "synthetic-device-namespace", "default", "namespace of the Devices, capacities and allocations in the provider project")
	flags.StringVar(&f.syntheticLocation, "synthetic-location", "us-central-1", "only location accepted by synthetic staging")
	flags.StringVar(&f.syntheticConsumerProjectName, "synthetic-consumer-project-name", "", "the one engaged consumer project synthetic staging allocates for and publishes into")
	flags.StringVar(&f.syntheticProviderProjectName, "synthetic-provider-project-name", "", "provider project used by synthetic cross-project staging")
	flags.StringVar(&f.syntheticPublishedClassName, "synthetic-published-class-name", "", "provider MachineClass published into the synthetic consumer project")
	flags.StringVar(&f.syntheticPublishedProfileName, "synthetic-published-profile-name", "", "provider MachineProfile published into the synthetic consumer project")
	return f
}

var errFlags = errors.New("invalid fleet-manager flags")

// validate checks flag combinations before any cluster is contacted.
func (f *managerFlags) validate() error {
	if f.configPath == "" || f.leaderElectionNamespace == "" {
		return errors.Join(errFlags, errors.New("--config and --leader-election-namespace are required"))
	}
	if !f.syntheticStaging {
		if f.syntheticConsumerProjectName != "" || f.syntheticProviderProjectName != "" || f.syntheticPublishedClassName != "" || f.syntheticPublishedProfileName != "" || f.syntheticTokenFile != "" || f.syntheticCoordinatorTokenFile != "" || f.syntheticReleaseTokenFile != "" || f.syntheticEnrollmentFile != "" {
			return errors.Join(errFlags, errors.New("synthetic flags require --synthetic-staging"))
		}
		return nil
	}
	if f.syntheticTokenFile == "" || f.syntheticConsumerProjectName == "" || f.syntheticCoordinatorTokenFile == "" || f.syntheticReleaseTokenFile == "" || f.syntheticEnrollmentFile == "" {
		return errors.Join(errFlags, errors.New("--synthetic-staging requires the provider site token, the coordinator and release controller tokens, the site enrollment, and --synthetic-consumer-project-name"))
	}
	if f.syntheticProviderProjectName != "" && (f.syntheticPublishedClassName == "" || f.syntheticPublishedProfileName == "") {
		return errors.Join(errFlags, errors.New("--synthetic-provider-project-name requires the published class and profile names"))
	}
	if f.syntheticProviderProjectName != "" && f.syntheticProviderProjectName == f.syntheticConsumerProjectName {
		return errors.Join(errFlags, errors.New("the synthetic provider and consumer projects must differ"))
	}
	return nil
}

func main() {
	flags := bindFlags(flag.CommandLine)
	zapOptions := zap.Options{Development: false}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	must(flags.validate())
	cfg, err := projectdiscovery.LoadConfig(flags.configPath)
	must(err)

	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(fleetv1.AddToScheme(scheme))
	must(inventory.AddToScheme(scheme))
	must(ops.AddToScheme(scheme))
	must(networkingv1.AddToScheme(scheme))
	must(locationsv1alpha1.AddToScheme(scheme))
	must(quotav1alpha1.AddToScheme(scheme))
	must(resourcemanagerv1alpha1.AddToScheme(scheme))
	must(servicesv1alpha1.AddToScheme(scheme))
	hostConfig, err := rest.InClusterConfig()
	must(err)
	selection, err := projectdiscovery.Select(cfg, scheme)
	must(err)
	rootMiloClient, err := ctrlclient.New(selection.RootConfig, ctrlclient.Options{Scheme: scheme})
	must(err)
	ctx := ctrl.SetupSignalHandler()

	// Every engaged project's identity is read from Milo when it engages and
	// re-verified before every reconcile; it is never configuration.
	resolve := func(ctx context.Context, name multicluster.ClusterName) (controller.ProjectIdentity, error) {
		return eventmanager.ResolveProjectIdentity(ctx, rootMiloClient, cfg.AuthorityID, string(name))
	}
	verifierFor := func(expected controller.ProjectIdentity) eventmanager.ProjectVerifier {
		return eventmanager.ProjectVerifierFunc(func(ctx context.Context, project controller.ProjectIdentity) error {
			current, err := eventmanager.ResolveProjectIdentity(ctx, rootMiloClient, cfg.AuthorityID, expected.RouteName)
			if err != nil || current != expected || project != expected {
				return eventmanager.ErrProjectIdentityUnavailable
			}
			return nil
		})
	}
	projects := eventmanager.NewProjects(resolve, verifierFor)
	mgr, tracker, err := projectdiscovery.NewManager(hostConfig, selection.Provider, ctrl.Options{Scheme: scheme, Cache: eventmanager.ProjectCacheOptions(), LeaderElection: true, LeaderElectionID: leaseName, LeaderElectionNamespace: flags.leaderElectionNamespace, HealthProbeBindAddress: ":8081"}, projects.Aware)
	must(err)
	for _, runnable := range selection.LocalRunnables {
		must(mgr.GetLocalManager().Add(runnable))
	}
	setupOptions := eventmanager.SetupOptions{HandlersFor: eventmanager.DefaultHandlers}

	// Without --synthetic-staging every project gets blocked lifecycle
	// handlers: no allocation, installation, provider call, or custody write.
	// Only the requested-hardware projector writes.
	if flags.syntheticStaging {
		consumerProject, err := eventmanager.ResolveProjectIdentity(ctx, rootMiloClient, cfg.AuthorityID, flags.syntheticConsumerProjectName)
		must(err)
		// Devices, capacities, allocations and custody live in the provider
		// project, which is the consumer project when nothing is published.
		providerProject := consumerProject
		if flags.syntheticProviderProjectName != "" {
			providerProject, err = eventmanager.ResolveProjectIdentity(ctx, rootMiloClient, cfg.AuthorityID, flags.syntheticProviderProjectName)
			must(err)
			if providerProject.SameLifetime(consumerProject) {
				must(eventmanager.ErrEventWiringUnavailable)
			}
		}
		providerConfig, err := projectdiscovery.ProjectRESTConfig(selection.ProjectConfig, providerProject.RouteName)
		must(err)
		allocator, loop, discover, err := syntheticCustody(ctx, scheme, providerConfig, flags)
		must(err)
		// Both need the leader lease: one coordinator writes custody.
		must(mgr.GetLocalManager().Add(manager.RunnableFunc(loop.Run)))
		must(mgr.GetLocalManager().Add(manager.RunnableFunc(discover)))
		template := staging.Handler{Allocator: allocator, Location: flags.syntheticLocation}
		if flags.syntheticProviderProjectName != "" {
			providerCluster, err := ctrlcluster.New(providerConfig, func(options *ctrlcluster.Options) {
				options.Scheme = scheme
				options.Cache = eventmanager.ProjectCacheOptions()
			})
			must(err)
			template.Provider, template.ProviderProject = providerCluster.GetClient(), providerProject
			setupOptions.ProviderProject, setupOptions.ProviderCluster, setupOptions.ProviderVerifier = providerProject, providerCluster, verifierFor(providerProject)
			setupOptions.ProviderConsumer = multicluster.ClusterName(consumerProject.RouteName)
			must(staging.SetupPublisher(mgr, projects, providerCluster, staging.PublisherOptions{ConsumerProject: consumerProject, ProviderProject: providerProject, ClassName: flags.syntheticPublishedClassName, ProfileName: flags.syntheticPublishedProfileName}))
		}
		setupOptions.HandlersFor = staging.ConsumerHandlers(eventmanager.DefaultHandlers, consumerProject, template)
	}
	must(eventmanager.Setup(ctx, mgr, projects, setupOptions))
	// Readiness reflects discovery and cache health only. It never depends on
	// a provider integration, and passing it enables no lifecycle effect.
	must(projectdiscovery.AddHealthChecks(mgr, selection.Discovery, tracker))

	group, ctx := errgroup.WithContext(ctx)
	for _, runnable := range selection.SideRunnables {
		group.Go(func() error { return ignoreCanceled(runnable.Start(ctx)) })
	}
	group.Go(func() error { return ignoreCanceled(mgr.Start(ctx)) })
	must(group.Wait())
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func must(err error) {
	if err != nil {
		ctrl.Log.Error(err, "fleet manager failed")
		os.Exit(1)
	}
}
