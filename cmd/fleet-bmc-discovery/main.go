// fleet-bmc-discovery reads what each Device's BMC reports over read-only
// Redfish GETs and writes it into Device status in one Milo project. It runs
// where the BMC network is reachable, as one replica without leader election.
// With --once it contacts each BMC once and prints the statuses it would
// write, writing nothing to Milo.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"go.datum.net/fleet/internal/bmcdiscovery"
)

func main() {
	var configPath string
	var once bool
	flag.StringVar(&configPath, "config", "/etc/fleet/bmc-discovery/config.yaml", "discovery configuration file")
	flag.BoolVar(&once, "once", false, "contact each BMC once, print the statuses that would be written, and write nothing to Milo")
	zapOptions := zap.Options{Development: false}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	if err := run(ctrl.SetupSignalHandler(), configPath, once); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "fleet-bmc-discovery:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string, once bool) error {
	config, err := bmcdiscovery.LoadConfig(configPath)
	if err != nil {
		return err
	}
	milo, err := clientcmd.BuildConfigFromFlags("", config.MiloKubeconfig)
	if err != nil {
		return fmt.Errorf("Milo project kubeconfig: %w", err)
	}
	var local *rest.Config
	if config.LocalKubeconfig != "" {
		local, err = clientcmd.BuildConfigFromFlags("", config.LocalKubeconfig)
	} else {
		local, err = rest.InClusterConfig()
	}
	if err != nil {
		return fmt.Errorf("local cluster config: %w", err)
	}
	scheme, err := bmcdiscovery.NewScheme()
	if err != nil {
		return err
	}
	bmc := bmcdiscovery.RedfishBMC{Timeout: config.RequestTimeout.Duration}
	if once {
		devices, err := client.New(milo, client.Options{Scheme: scheme})
		if err != nil {
			return err
		}
		secrets, err := client.New(local, client.Options{Scheme: scheme})
		if err != nil {
			return err
		}
		// Reads only: RunOnce takes readers, so no write can reach Milo.
		return bmcdiscovery.RunOnce(ctx, config, client.Reader(devices), client.Reader(secrets), bmc, os.Stdout)
	}
	mgr, err := bmcdiscovery.NewManager(config, milo, local, scheme, bmc)
	if err != nil {
		return err
	}
	return mgr.Start(ctx)
}
