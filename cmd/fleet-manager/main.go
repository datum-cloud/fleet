package main

import (
	"context"
	"flag"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	eventmanager "go.datum.net/fleet/internal/manager"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
)

func main() {
	var authorityID, projectName, privateNamespace, projectKubeconfig, leaderElectionNamespace string
	flag.StringVar(&authorityID, "authority-id", "", "configured Milo authority identifier")
	flag.StringVar(&projectName, "project-name", "", "Milo Project name served by this cache")
	flag.StringVar(&privateNamespace, "private-namespace", "", "service-controlled private record namespace")
	flag.StringVar(&projectKubeconfig, "project-kubeconfig", "", "root Milo kubeconfig used to reach the configured project route")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "", "management-cluster namespace for the leader lease")
	zapOptions := zap.Options{Development: false}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))

	if authorityID == "" || projectName == "" || privateNamespace == "" || projectKubeconfig == "" || leaderElectionNamespace == "" {
		ctrl.Log.Error(eventmanager.ErrEventWiringUnavailable, "project name and private namespace are required")
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(fleetv1.AddToScheme(scheme))
	must(networkingv1.AddToScheme(scheme))
	must(quotav1alpha1.AddToScheme(scheme))
	must(resourcemanagerv1alpha1.AddToScheme(scheme))
	hostConfig, err := rest.InClusterConfig()
	must(err)
	rootMiloConfig, err := clientcmd.BuildConfigFromFlags("", projectKubeconfig)
	must(err)
	rootMiloClient, err := ctrlclient.New(rootMiloConfig, ctrlclient.Options{Scheme: scheme})
	must(err)
	ctx := ctrl.SetupSignalHandler()
	project, err := eventmanager.ResolveProjectIdentity(ctx, rootMiloClient, authorityID, projectName)
	must(err)
	projectConfig, err := eventmanager.ProjectRESTConfig(rootMiloConfig, project.RouteName)
	must(err)
	mgr, err := ctrl.NewManager(projectConfig, ctrl.Options{Scheme: scheme, Cache: eventmanager.ProjectCacheOptions(), LeaderElection: true, LeaderElectionID: "fleet-manager.fleet.datumapis.com", LeaderElectionNamespace: leaderElectionNamespace, LeaderElectionConfig: hostConfig, HealthProbeBindAddress: ":8081"})
	must(err)
	privateCluster, err := ctrlcluster.New(hostConfig, func(options *ctrlcluster.Options) {
		options.Scheme = scheme
		options.Cache = eventmanager.PrivateCacheOptions(privateNamespace)
	})
	must(err)
	// Production handlers require the trusted admission and live Milo event
	// adapters that this increment deliberately does not fabricate. The binary
	// therefore refuses to start effects until those owners supply wiring.
	blocked := eventmanager.HandlerFunc(func(context.Context, eventmanager.ProjectRequest) eventmanager.Outcome { return eventmanager.Outcome{} })
	verifier := eventmanager.ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return eventmanager.ErrEventWiringUnavailable })
	must(eventmanager.Setup(ctx, mgr, eventmanager.SetupOptions{Project: project, PrivateNamespace: privateNamespace, PrivateCluster: privateCluster, ProjectVerifier: verifier, Handlers: eventmanager.Handlers{Claim: blocked, Installation: blocked, ClaimProjector: blocked, InstallationProjector: blocked}}))
	must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	must(mgr.AddReadyzCheck("integration", func(*http.Request) error { return eventmanager.ErrEventWiringUnavailable }))
	must(mgr.Start(ctx))
}

func must(err error) {
	if err != nil {
		ctrl.Log.Error(err, "fleet manager failed")
		os.Exit(1)
	}
}
