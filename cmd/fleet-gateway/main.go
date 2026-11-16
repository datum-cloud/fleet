// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log/slog"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	coordinationv1 "go.datum.net/fleet/internal/api/coordination/v1alpha1"
	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/gateway"
)

func main() {
	var cfg gateway.Config
	var tokenFile string
	flag.StringVar(&cfg.RelayAddress, "relay-address", "fleet-relay.machine-access-system.svc:8443", "relay address")
	flag.StringVar(&cfg.RelayPublicURL, "relay-public-url", "tcp://127.0.0.1:8443", "client-visible relay URL")
	flag.StringVar(&tokenFile, "relay-token-file", "/var/run/secrets/fleet/relay-token", "relay registration token")
	flag.StringVar(&cfg.Audience, "audience", "fleet-gateway/location-prototype/r1", "proof audience")
	flag.StringVar(&cfg.SupportedAdapter, "supported-adapter", "sushy-libvirt-v1", "qualified local enrollment adapter")
	flag.StringVar(&cfg.EnrollmentNamespace, "enrollment-namespace", "machine-access-system", "namespace containing safe capacity copies and private enrollments")
	flag.StringVar(&cfg.JournalDir, "journal-dir", "/var/lib/fleet", "durable journal directory")
	flag.StringVar(&cfg.StateDir, "state-dir", "/var/lib/fleet/sessions", "gateway state directory")
	flag.BoolVar(&cfg.DropResetResponseOnce, "fault-drop-reset-response-once", false, "test-only lost response injection")
	metrics := flag.String("metrics-bind-address", ":8080", "metrics address")
	probe := flag.String("health-probe-bind-address", ":8081", "probe address")
	flag.Parse()
	cfg.RelayGatewayToken = readSecret(tokenFile)
	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(fleetv1.AddToScheme(scheme))
	must(fleetv1alpha1.AddToScheme(scheme))
	must(coordinationv1.AddToScheme(scheme))
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{
			"machine-access-system": {},
		}},
		Metrics:                metricsserver.Options{BindAddress: *metrics},
		HealthProbeBindAddress: *probe,
	})
	must(err)
	must((&gateway.Reconciler{Config: cfg}).SetupWithManager(mgr))
	must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	must(mgr.AddReadyzCheck("readyz", healthz.Ping))
	slog.Info("starting location gateway", "audience", cfg.Audience, "supportedAdapter", cfg.SupportedAdapter)
	must(mgr.Start(ctrl.SetupSignalHandler()))
}
func readSecret(path string) string {
	b, err := os.ReadFile(path)
	must(err)
	return strings.TrimSpace(string(b))
}
func must(err error) {
	if err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
