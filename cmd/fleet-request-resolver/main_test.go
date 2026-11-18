package main

import (
	"context"
	"errors"
	"flag"
	"testing"

	"go.datum.net/fleet/internal/requestresolver"
)

func TestFlagsDefaultToLeaderElectedProbedServing(t *testing.T) {
	flags := flag.NewFlagSet("fleet-request-resolver", flag.ContinueOnError)
	opts := bindFlags(flags)
	if err := flags.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if !opts.leaderElect || opts.probeAddr != ":8081" || opts.metricsAddr != ":8080" || opts.leaderElectionID != "fleet-request-resolver.fleet.datumapis.com" {
		t.Fatalf("defaults = %#v", opts)
	}
}

func TestRunFailsClosedBeforeContactingAnyCluster(t *testing.T) {
	if err := run(context.Background(), &options{leaderElect: true}); err == nil {
		t.Fatal("leader election without a lease namespace must not start")
	}
	if err := run(context.Background(), &options{leaderElect: false}); !errors.Is(err, requestresolver.ErrInvalidConfig) {
		t.Fatalf("missing config err = %v, want ErrInvalidConfig", err)
	}
}
