package main

import (
	"context"
	"flag"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	eventmanager "go.datum.net/fleet/internal/manager"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
)

func main() {
	var authorityID, projectUID, projectRoute, privateNamespace string
	flag.StringVar(&authorityID, "authority-id", "", "configured Milo authority identifier")
	flag.StringVar(&projectUID, "project-uid", "", "exact root Project UID served by this cache")
	flag.StringVar(&projectRoute, "project-route", "", "Milo project route name")
	flag.StringVar(&privateNamespace, "private-namespace", "", "service-controlled private record namespace")
	zapOptions := zap.Options{Development: false}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))

	project := controller.ProjectIdentity{AuthorityID: authorityID, RouteName: projectRoute, UID: types.UID(projectUID)}
	if !project.Valid() || privateNamespace == "" {
		ctrl.Log.Error(eventmanager.ErrEventWiringUnavailable, "exact project identity and private namespace are required")
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(fleetv1.AddToScheme(scheme))
	must(networkingv1.AddToScheme(scheme))
	must(quotav1alpha1.AddToScheme(scheme))
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, Cache: eventmanager.CacheOptions(privateNamespace), LeaderElection: true, LeaderElectionID: "fleet-manager.fleet.datumapis.com", HealthProbeBindAddress: ":8081"})
	must(err)
	// Production handlers require the trusted admission and live Milo event
	// adapters that this increment deliberately does not fabricate. The binary
	// therefore refuses to start effects until those owners supply wiring.
	blocked := eventmanager.HandlerFunc(func(context.Context, eventmanager.ProjectRequest) eventmanager.Outcome { return eventmanager.Outcome{} })
	verifier := eventmanager.ProjectVerifierFunc(func(context.Context, controller.ProjectIdentity) error { return eventmanager.ErrEventWiringUnavailable })
	ctx := ctrl.SetupSignalHandler()
	must(eventmanager.Setup(ctx, mgr, eventmanager.SetupOptions{Project: project, PrivateNamespace: privateNamespace, ProjectVerifier: verifier, Handlers: eventmanager.Handlers{Claim: blocked, Installation: blocked, ClaimProjector: blocked, InstallationProjector: blocked}}))
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
