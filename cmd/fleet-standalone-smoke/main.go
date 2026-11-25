// fleet-standalone-smoke runs the standalone smoke test's phases, and the
// coordinator process the phases rely on, in one kind cluster.
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
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/delivery"
	"go.datum.net/fleet/internal/site/siteapi"
	"go.datum.net/fleet/internal/standalone"
)

const (
	providerPrincipal = "system:serviceaccount:fleet-system:fleet-standalone-smoke"
	releasePrincipal  = "system:serviceaccount:fleet-system:fleet-release-controller"
)

func main() {
	options := standalone.DefaultOptions()
	var phase, coordinatorToken, releaseToken, siteURL, siteToken, siteName, devices string
	flag.StringVar(&phase, "phase", "", "required: allocate, install, reinstall, release, or coordinate")
	flag.StringVar(&coordinatorToken, "coordinator-token-file", "", "the custody coordinator's ServiceAccount token (phases)")
	flag.StringVar(&releaseToken, "release-token-file", "", "the release controller's ServiceAccount token (phases)")
	flag.StringVar(&siteURL, "site-url", "http://fleet-simulator:8080", "the simulator site's API (coordinate)")
	flag.StringVar(&siteToken, "site-token-file", "", "the simulator site's bearer token (coordinate)")
	flag.StringVar(&siteName, "site", "simulator", "the simulator site's name (coordinate)")
	flag.StringVar(&devices, "devices", "synthetic-device-central-01", "comma-separated Devices the site serves (coordinate)")
	flag.StringVar(&options.ProviderNamespace, "provider-namespace", options.ProviderNamespace, "synthetic provider namespace")
	flag.StringVar(&options.FirstNamespace, "first-namespace", options.FirstNamespace, "first consumer namespace stand-in")
	flag.StringVar(&options.SecondNamespace, "second-namespace", options.SecondNamespace, "second consumer namespace stand-in")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config := ctrl.GetConfigOrDie()
	registry, err := custody.DefaultRegistry()
	must(err)
	if phase == "coordinate" {
		must(coordinate(ctx, config, registry, options.ProviderNamespace, siteName, siteURL, siteToken, strings.Split(devices, ",")))
		return
	}
	if coordinatorToken == "" || releaseToken == "" {
		must(errors.New("phases need the coordinator and release controller token files"))
	}
	provider, err := newClient(config)
	must(err)
	coordinatorConfig, releaseConfig := withToken(config, coordinatorToken), withToken(config, releaseToken)
	coordinatorClient, err := newClient(coordinatorConfig)
	must(err)
	releaseClient, err := newClient(releaseConfig)
	must(err)
	providerCreator, err := allocation.Creator(ctx, config)
	must(err)
	releaseCreator, err := allocation.Creator(ctx, releaseConfig)
	must(err)
	allocator := &allocation.Allocator{
		Provider: provider, ProviderCreator: providerCreator,
		Releaser: releaseClient, ReleaseCreator: releaseCreator,
		Custody:   &custody.Coordinator{Client: coordinatorClient, Registry: registry, Config: custody.Config{Namespace: options.ProviderNamespace, Principals: principals()}},
		Namespace: options.ProviderNamespace,
		// The fixture Device's boot NIC and the simulator's one enrolled disk.
		BootInterface: "primary", TargetDisks: []string{"disk-0"},
	}
	must(standalone.Runner{Client: provider, Allocator: allocator, Options: options}.Run(ctx, phase))
	fmt.Printf("standalone synthetic smoke phase %s completed\n", phase)
}

// coordinate is the coordinator process: the custody loop with the simulator
// as the only site.
func coordinate(ctx context.Context, config *rest.Config, registry *custody.Registry, namespace, siteName, siteURL, tokenFile string, devices []string) error {
	if tokenFile == "" {
		return errors.New("coordinate needs the site token file")
	}
	api, err := newClient(config)
	if err != nil {
		return err
	}
	remote, err := siteapi.NewClient(siteURL, func() (string, error) { return readToken(tokenFile) }, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	sites := &delivery.Sites{Sites: map[string]delivery.Site{siteName: remote}, Devices: map[string][]string{}}
	for _, device := range devices {
		if device = strings.TrimSpace(device); device != "" {
			sites.Devices[device] = []string{siteName}
		}
	}
	coordinator := &custody.Coordinator{Client: api, Registry: registry, Sites: sites, Config: custody.Config{Namespace: namespace, Principals: principals()}}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	loop := &delivery.Loop{Client: api, Coordinator: coordinator, Namespace: namespace, Courier: &delivery.Courier{Client: api, Registry: registry, Coordinator: coordinator, Sites: sites, Namespace: namespace},
		OnError: func(err error) { logger.Info("coordinator pass incomplete", "error", err.Error()) }}
	logger.Info("custody coordinator running", "namespace", namespace, "site", siteName)
	return loop.Run(ctx)
}

func principals() custody.Principals {
	return custody.Principals{custody.CreatorMetalProvider: {{Username: providerPrincipal}}, custody.CreatorReleaseController: {{Username: releasePrincipal}}}
}

func newClient(config *rest.Config) (client.Client, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, fleetv1.AddToScheme, inventory.AddToScheme, ops.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return client.New(config, client.Options{Scheme: scheme})
}

// withToken is the in-cluster config authenticated as another
// ServiceAccount.
func withToken(config *rest.Config, tokenFile string) *rest.Config {
	copied := rest.AnonymousClientConfig(config)
	copied.BearerTokenFile = tokenFile
	return copied
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("the token must be one nonempty line")
	}
	return token, nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
