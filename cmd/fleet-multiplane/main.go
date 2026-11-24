// fleet-multiplane is an opt-in local acceptance binary. Production managers do
// not import its synthetic admission, quota or hardware enrollment fixtures.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.datum.net/fleet/internal/coordination"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/localvm"
	"go.datum.net/fleet/internal/multiplane"
	"go.datum.net/fleet/internal/redfish"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "multi-plane command failed:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: fleet-multiplane site-worker|site-observer|image|boot-service|boot-denials|dhcp-feed|redfish-probe|<management phase> [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	once := fs.Bool("once", false, "one reconcile attempt for fault injection")
	handoff := fs.String("handoff", "", "exact delivered handoff name (site-worker)")
	origin := fs.String("origin", "", "boot listener origin to probe (boot-denials); default the configured origin")
	interval := fs.Duration("interval", 10*time.Second, "observation refresh interval (site-observer)")
	configPath := fs.String("config", "", "private acceptance config JSON")
	kubeconfig := fs.String("kubeconfig", "", "cell-local kubeconfig; default in-cluster ServiceAccount")
	cell := fs.String("cell", "", "exact Karmada member name")
	scoped := fs.String("scoped-asset", "", "exact retained provider-scoped Device ID")
	extraMAC := fs.String("extra-mac", "", "another enrolled NIC whose boot script must be denied (boot-denials, Redfish mode)")
	endpoint := fs.String("endpoint", "", "Redfish endpoint (redfish-probe)")
	credentials := fs.String("credentials", "", "directory holding username and password files (redfish-probe)")
	system := fs.String("system", "", "enrolled Redfish System Id (redfish-probe)")
	uuid := fs.String("uuid", "", "enrolled Redfish System UUID (redfish-probe)")
	syncToken := fs.String("sync-token-file", "", "file holding the DHCP synchronizer bearer token (dhcp-feed)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	switch os.Args[1] {
	case "seed", "allocate", "install", "install-b", "admit-install", "release", "release-b", "reuse", "asset":
		config, err := multiplane.ReadConfig(*configPath)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if os.Args[1] == "seed" {
			return multiplane.Seed(ctx, config)
		}
		r, err := multiplane.New(ctx, config)
		if err != nil {
			return err
		}
		if os.Args[1] == "asset" {
			fmt.Println(r.ScopedAsset())
			return nil
		}
		var action func(context.Context) error
		switch os.Args[1] {
		case "allocate":
			action = r.Allocate
		case "install", "admit-install":
			action = r.Install
		case "install-b":
			action = r.InstallSecond
		case "release":
			action = r.Release
		case "release-b":
			action = r.ReleaseSecond
		case "reuse":
			action = r.Reuse
		}
		for {
			err = action(ctx)
			if os.Args[1] == "admit-install" {
				if errors.Is(err, executor.ErrOutcomeUnknown) {
					fmt.Println("Installation intent retained; execution result remains Unknown")
					return nil
				}
				if err == nil {
					return fmt.Errorf("admission probe unexpectedly completed; run before starting cell")
				}
				return err
			}
			if (*once || terminal(err)) && err != nil {
				return err
			}
			if err == nil {
				fmt.Println(os.Args[1] + ": verified local acceptance phase (synthetic authority)")
				return nil
			}
			if ctx.Err() != nil {
				return fmt.Errorf("%s did not converge; authority retained; last error: %w", os.Args[1], err)
			}
			time.Sleep(3 * time.Second)
		}
	}
	if os.Args[1] == "redfish-probe" {
		return probe(*endpoint, *credentials, *system, *uuid)
	}
	if redfishMode() {
		switch os.Args[1] {
		case "boot-service", "boot-denials", "dhcp-feed":
			p, err := redfish.OpenReadOnly()
			if err != nil {
				return err
			}
			if os.Args[1] == "boot-service" {
				return p.ServeBoot()
			}
			if os.Args[1] == "dhcp-feed" {
				token, err := os.ReadFile(*syncToken)
				if err != nil {
					return fmt.Errorf("DHCP synchronizer token: %w", err)
				}
				return p.ServeDHCP(strings.TrimSpace(string(token)))
			}
			var extra []string
			if *extraMAC != "" {
				extra = append(extra, *extraMAC)
			}
			summary, err := p.CheckClosedBoot(*origin, extra)
			if err != nil {
				return err
			}
			fmt.Println(summary)
			return nil
		}
	}
	switch os.Args[1] {
	case "image", "boot-service", "boot-denials":
		// Read-only: the boot service mounts the journal read-only.
		p, err := localvm.OpenReadOnly()
		if err != nil {
			return err
		}
		switch os.Args[1] {
		case "image":
			fmt.Println(p.ImageReference())
			return nil
		case "boot-service":
			return p.ServeBoot()
		}
		summary, err := p.CheckClosedBoot(*origin)
		if err != nil {
			return err
		}
		fmt.Println(summary)
		return nil
	case "site-worker", "site-observer":
	default:
		return fmt.Errorf("unknown command")
	}
	if *cell == "" || *scoped == "" || (os.Args[1] == "site-worker" && *handoff == "") {
		return fmt.Errorf("cell, scoped-asset and (for site-worker) handoff required")
	}
	cfg, err := rest.InClusterConfig()
	if *kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}
	if err != nil {
		return err
	}
	cfg.Timeout = 30 * time.Second
	api, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if os.Args[1] == "site-observer" {
		// The observer never executes: its provider is read-only.
		var p executor.Client
		var err error
		if redfishMode() {
			p, err = redfish.OpenReadOnly()
		} else {
			p, err = localvm.OpenReadOnly()
		}
		if err != nil {
			return err
		}
		mapping := &coordination.LocalAssetMapping{API: api, Namespace: coordinationNamespace, Cell: *cell, ScopedAsset: *scoped, PhysicalAsset: localvm.AssetID, Backend: p}
		observer := coordination.Cell{API: api, Namespace: coordinationNamespace, Name: *cell, Executor: mapping}
		return observer.Observe(ctx, *interval, func(name string, err error) {
			fmt.Fprintf(os.Stderr, "observer: handoff %s not published: %s\n", name, classify(err))
		})
	}
	// A Redfish worker has no libvirt fallback: redfish.Open refuses any
	// hypervisor configuration, and the worker reaches the target only
	// through its enrolled BMC.
	var p executor.Client
	if redfishMode() {
		p, err = redfish.Open()
	} else {
		p, err = localvm.Open()
	}
	if err != nil {
		return err
	}
	mapping := &coordination.LocalAssetMapping{API: api, Namespace: coordinationNamespace, Cell: *cell, ScopedAsset: *scoped, PhysicalAsset: localvm.AssetID, Backend: p}
	worker := coordination.Cell{API: api, Namespace: coordinationNamespace, Name: *cell, Executor: mapping}
	err = worker.Execute(ctx, *handoff)
	fmt.Fprintf(os.Stderr, "site-worker: handoff %s: %s\n", *handoff, classify(err))
	if err != nil {
		if terminal(err) {
			os.Exit(exitRejected)
		}
		os.Exit(exitRetry)
	}
	return nil
}

const coordinationNamespace = "fleet-coordination"

// redfishMode selects the Redfish executor for site roles. The site's Flux
// layers set it; there is no automatic choice between providers.
func redfishMode() bool { return os.Getenv("FLEET_SITE_PROVIDER") == "redfish" }

// probe performs read-only Redfish discovery of one enrolled System and
// prints only an outcome class. Exit codes: 0 discovered, 10 credential
// rejected, 11 capability unsupported, 12 identity mismatch.
func probe(endpoint, credentials, system, uuid string) error {
	user, err1 := os.ReadFile(credentials + "/username")
	password, err2 := os.ReadFile(credentials + "/password")
	if credentials == "" || err1 != nil || err2 != nil {
		return fmt.Errorf("redfish-probe: credentials unavailable")
	}
	c, err := redfish.NewClient(endpoint, strings.TrimSpace(string(user)), strings.TrimSpace(string(password)), 20*time.Second)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := c.Discover(ctx, redfish.Target{SystemID: system, SystemUUID: uuid})
	switch {
	case err == nil:
		fmt.Printf("redfish-probe: discovered %s at %s; ResetType %s; boot targets %s\n", s.ID, s.Path, strings.Join(s.ResetTypes, ","), strings.Join(s.BootTargets, ","))
		return nil
	case errors.Is(err, redfish.ErrUnauthorized):
		fmt.Println("redfish-probe: credential rejected; no effect issued")
		os.Exit(10)
	case errors.Is(err, redfish.ErrUnsupported):
		fmt.Println("redfish-probe: required capability not advertised; no effect issued")
		os.Exit(11)
	case errors.Is(err, redfish.ErrIdentity):
		fmt.Println("redfish-probe: enrolled identity not matched exactly; no effect issued")
		os.Exit(12)
	}
	return err
}

// Worker exit codes are the Job podFailurePolicy contract: rejected authority
// fails the Job without retry; anything else is retried by the Job controller
// against the same retained operation. Exit 0 only follows a validated
// terminal journal result, and still does not publish or authorize readiness.
const (
	exitRejected = 65
	exitRetry    = 75
)

// classify names an outcome without printing handoff or journal contents.
func classify(err error) string {
	switch {
	case err == nil:
		return "terminal result verified in provider journal"
	case errors.Is(err, executor.ErrFenced):
		return "fenced: " + err.Error()
	case errors.Is(err, executor.ErrConflict):
		return "conflict: " + err.Error()
	case errors.Is(err, executor.ErrInvalid):
		return "invalid: " + err.Error()
	case errors.Is(err, executor.ErrOutcomeUnknown):
		return "outcome unknown: " + err.Error()
	default:
		return "pending: " + err.Error()
	}
}

// terminal errors are authority rejections; retrying cannot change them and
// Unknown (the only retained-uncertainty result) stays retryable.
func terminal(err error) bool {
	for _, target := range []error{executor.ErrConflict, executor.ErrFenced, executor.ErrInvalid, multiplane.ErrLocationNotServed} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
