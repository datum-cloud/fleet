package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestFixtureFlagsAreIndependentOfGlobalKubeconfig(t *testing.T) {
	// Imported controller-runtime already owns this global flag in the executable.
	if flag.Lookup("kubeconfig") == nil {
		t.Fatal("expected controller-runtime global kubeconfig flag")
	}
	for i := 0; i < 2; i++ {
		err := run([]string{"--kubeconfig", "/synthetic/test-config"})
		if err == nil || !strings.Contains(err.Error(), "--synthetic") {
			t.Fatalf("expected safety validation before accessing a cluster: %v", err)
		}
	}
	if err := run([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}
