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
	"syscall"
	"time"

	"go.datum.net/fleet/internal/coordination"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/localvm"
	"go.datum.net/fleet/internal/multiplane"
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
		return fmt.Errorf("usage: fleet-multiplane site-worker|site-observer|image|boot-service|boot-denials|<management phase> [flags]")
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
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	switch os.Args[1] {
	case "seed", "allocate", "install", "admit-install", "release", "release-b", "reuse", "asset":
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
		p, err := localvm.OpenReadOnly()
		if err != nil {
			return err
		}
		mapping := &coordination.LocalAssetMapping{API: api, Namespace: coordinationNamespace, Cell: *cell, ScopedAsset: *scoped, PhysicalAsset: localvm.AssetID, Backend: p}
		observer := coordination.Cell{API: api, Namespace: coordinationNamespace, Name: *cell, Executor: mapping}
		return observer.Observe(ctx, *interval, func(name string, err error) {
			fmt.Fprintf(os.Stderr, "observer: handoff %s not published: %s\n", name, classify(err))
		})
	}
	p, err := localvm.Open()
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
