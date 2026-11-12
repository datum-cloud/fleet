// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log/slog"
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
)

func main() {
	federationKubeconfig := flag.String("federation-kubeconfig", "", "kubeconfig for the Karmada federation API")
	webhookCertDir := flag.String("webhook-cert-dir", "/var/run/fleet-webhook", "webhook serving certificate directory")
	metrics := flag.String("metrics-bind-address", ":8080", "metrics address")
	probe := flag.String("health-probe-bind-address", ":8081", "probe address")
	authorityFreshness := flag.Duration("authority-freshness", 2*time.Minute, "maximum cached allocation-authority lifetime at a location")
	perClaimLimit := flag.Int("per-claim-session-limit", 1, "maximum concurrent sessions for one claim")
	perProjectLimit := flag.Int("per-project-session-limit", 5, "maximum concurrent sessions for one consumer project")
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
	mgr.GetWebhookServer().Register("/mutate-metal-datumapis-com-v1alpha1-machinemanagementsession", &admission.Webhook{Handler: controller.SessionAdmission{}})
	must((&controller.SessionReconciler{PerClaimLimit: *perClaimLimit, PerProjectLimit: *perProjectLimit}).SetupWithManager(mgr))
	must((&controller.ProjectionReconciler{FederationClient: federationClient, Freshness: *authorityFreshness}).SetupWithManager(mgr))
	must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	must(mgr.AddReadyzCheck("readyz", healthz.Ping))
	slog.Info("starting Fleet session controllers")
	must(mgr.Start(ctrl.SetupSignalHandler()))
}

func must(err error) {
	if err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
