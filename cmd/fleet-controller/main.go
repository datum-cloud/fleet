// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	coordinationv1 "go.datum.net/fleet/internal/api/coordination/v1alpha1"
	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/staging/sessionfixture"
)

func main() {
	federationKubeconfig := flag.String("federation-kubeconfig", "", "kubeconfig for the Karmada federation API")
	webhookCertDir := flag.String("webhook-cert-dir", "/var/run/fleet-webhook", "webhook serving certificate directory")
	metrics := flag.String("metrics-bind-address", ":8080", "metrics address")
	probe := flag.String("health-probe-bind-address", ":8081", "probe address")
	authorityFreshness := flag.Duration("authority-freshness", 2*time.Minute, "maximum cached allocation-authority lifetime at a location")
	perClaimLimit := flag.Int("per-claim-session-limit", 1, "maximum concurrent sessions for one claim")
	perProjectLimit := flag.Int("per-project-session-limit", 5, "maximum concurrent sessions for one consumer project")
	lifecycleMode := flag.String("lifecycle-mode", "production", "production (unready) or synthetic (test-only private fixture grants)")
	privateNamespace := flag.String("private-namespace", controller.SystemNamespace, "private Coordinator Secret namespace")
	flag.Parse()
	if *federationKubeconfig == "" {
		slog.Error("--federation-kubeconfig is required")
		os.Exit(2)
	}
	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(fleetv1.AddToScheme(scheme))
	must(fleetv1alpha1.AddToScheme(scheme))
	must(coordinationv1.AddToScheme(scheme))
	federationConfig, err := clientcmd.BuildConfigFromFlags("", *federationKubeconfig)
	must(err)
	federationClient, err := client.New(federationConfig, client.Options{Scheme: scheme})
	must(err)
	wh := webhook.NewServer(webhook.Options{CertDir: *webhookCertDir})
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: *metrics}, HealthProbeBindAddress: *probe, WebhookServer: wh})
	must(err)
	privateClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	must(err)
	lifecycle, readiness, err := lifecycleWiring(*lifecycleMode, privateClient, *privateNamespace)
	must(err)
	mgr.GetWebhookServer().Register("/mutate-metal-datumapis-com-v1alpha1-machinemanagementsession", &admission.Webhook{Handler: controller.SessionAdmission{}})
	must((&controller.SessionReconciler{Lifecycle: lifecycle, PerClaimLimit: *perClaimLimit, PerProjectLimit: *perProjectLimit}).SetupWithManager(mgr))
	must((&controller.ProjectionReconciler{Lifecycle: lifecycle, FederationClient: federationClient, Freshness: *authorityFreshness}).SetupWithManager(mgr))
	must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	must(mgr.AddReadyzCheck("lifecycle-authority", readiness))
	slog.Info("starting Fleet session controllers")
	must(mgr.Start(ctrl.SetupSignalHandler()))
}

func lifecycleWiring(mode string, kube client.Client, namespace string) (controller.SessionLifecycle, healthz.Checker, error) {
	switch mode {
	case "production":
		return nil, func(*http.Request) error { return controller.ErrUnsupported }, nil
	case "synthetic":
		authority, err := sessionfixture.NewAuthority(kube, namespace)
		if err != nil {
			return nil, nil, err
		}
		slog.Warn("SYNTHETIC TEST ONLY: fixture grants replace production authorization and provider observations", "privateNamespace", namespace)
		return authority, healthz.Ping, nil
	default:
		return nil, nil, controller.ErrUnsupported
	}
}

func must(err error) {
	if err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
