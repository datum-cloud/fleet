// fleet-inventory-observer refreshes InventoryObservation status from NetBox
// once and exits. It reads NetBox with GET requests only and writes only the
// status subresource of existing observations in one namespace.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventoryv1 "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/inventoryobserver"
	"go.datum.net/fleet/internal/netbox"
)

func main() {
	var namespace, origin, tokenFile string
	var timeout time.Duration
	flag.StringVar(&namespace, "namespace", os.Getenv("POD_NAMESPACE"), "namespace holding InventoryObservations")
	flag.StringVar(&origin, "netbox-url", os.Getenv("NETBOX_URL"), "NetBox origin, https://host[:port]")
	flag.StringVar(&tokenFile, "netbox-token-file", "", "file holding a read-only NetBox API token")
	flag.DurationVar(&timeout, "timeout", 2*time.Minute, "deadline for the whole refresh")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := run(ctx, logger, namespace, origin, tokenFile); err != nil {
		logger.Error("inventory observation refresh incomplete", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, namespace, origin, tokenFile string) error {
	if namespace == "" || origin == "" || tokenFile == "" {
		return errors.New("--namespace, --netbox-url, and --netbox-token-file are required")
	}
	source, err := netbox.NewClient(origin, func() (string, error) {
		data, err := os.ReadFile(tokenFile)
		return string(data), err
	}, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err := inventoryv1.AddToScheme(scheme); err != nil {
		return err
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	kube, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	observer := &inventoryobserver.Observer{Client: kube, Namespace: namespace, NetBox: source}
	results, err := observer.RefreshAll(ctx)
	if err != nil {
		return err
	}
	failed := 0
	for _, result := range results {
		attrs := []any{"observation", result.Name, "reason", result.Reason}
		if result.WriteErr != nil {
			attrs = append(attrs, "writeError", result.WriteErr.Error())
		}
		if result.Succeeded() {
			logger.Info("observation refreshed", attrs...)
			continue
		}
		failed++
		logger.Warn("observation not refreshed", append(attrs, "message", result.Message)...)
	}
	logger.Info("refresh finished", "observations", len(results), "notRefreshed", failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d observations were not refreshed", failed, len(results))
	}
	return nil
}
