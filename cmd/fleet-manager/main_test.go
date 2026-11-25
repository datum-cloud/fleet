package main

import (
	"errors"
	"flag"
	"io"
	"testing"
)

func parse(t *testing.T, args ...string) error {
	t.Helper()
	flags := flag.NewFlagSet("fleet-manager", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	f := bindFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	return f.validate()
}

var required = []string{"--config=/etc/fleet/manager/config.yaml", "--leader-election-namespace=fleet-system"}

func TestDefaultConfigurationHasNoSyntheticIntegration(t *testing.T) {
	if err := parse(t, required...); err != nil {
		t.Fatal(err)
	}
	for _, missing := range required {
		var args []string
		for _, arg := range required {
			if arg != missing {
				args = append(args, arg)
			}
		}
		if err := parse(t, args...); !errors.Is(err, errFlags) {
			t.Fatalf("without %s: err = %v", missing, err)
		}
	}
}

func TestRemovedSingleProjectFlagsAreRejected(t *testing.T) {
	for _, removed := range []string{"--project-name=consumer", "--authority-id=milo", "--project-kubeconfig=/k", "--enable-requested-hardware-projector=false", "--private-namespace=fleet-system-private"} {
		if err := parse(t, append(required, removed)...); err == nil {
			t.Fatalf("%s was accepted", removed)
		}
	}
}

func TestSyntheticFlagsRequireAndScopeStaging(t *testing.T) {
	if err := parse(t, append(required, "--synthetic-consumer-project-name=consumer")...); !errors.Is(err, errFlags) {
		t.Fatalf("synthetic flag without --synthetic-staging: %v", err)
	}
	if err := parse(t, append(required, "--synthetic-staging", "--synthetic-provider-token-file=/t")...); !errors.Is(err, errFlags) {
		t.Fatalf("staging without a consumer project: %v", err)
	}
	components := []string{"--synthetic-coordinator-token-file=/c", "--synthetic-release-token-file=/r", "--synthetic-projector-token-file=/p", "--synthetic-site-enrollment-file=/e"}
	staging := append(append(required, "--synthetic-staging", "--synthetic-provider-token-file=/t", "--synthetic-consumer-project-name=consumer"), components...)
	if err := parse(t, staging...); err != nil {
		t.Fatal(err)
	}
	// Each component authenticates as itself, and the site's Devices are
	// configuration: staging refuses to start without any of them.
	for _, missing := range components {
		var args []string
		for _, arg := range staging {
			if arg != missing {
				args = append(args, arg)
			}
		}
		if err := parse(t, args...); !errors.Is(err, errFlags) {
			t.Fatalf("staging without %s: %v", missing, err)
		}
		if err := parse(t, append(required, missing)...); !errors.Is(err, errFlags) {
			t.Fatalf("%s without --synthetic-staging: %v", missing, err)
		}
	}
	if err := parse(t, append(staging, "--synthetic-provider-project-name=provider-project")...); !errors.Is(err, errFlags) {
		t.Fatalf("provider project without publication names: %v", err)
	}
	if err := parse(t, append(staging, "--synthetic-provider-project-name=consumer", "--synthetic-published-class-name=c", "--synthetic-published-profile-name=p")...); !errors.Is(err, errFlags) {
		t.Fatalf("provider equal to consumer: %v", err)
	}
	if err := parse(t, append(staging, "--synthetic-provider-project-name=provider-project", "--synthetic-published-class-name=c", "--synthetic-published-profile-name=p")...); err != nil {
		t.Fatal(err)
	}
}
