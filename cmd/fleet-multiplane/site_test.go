package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAWorkerRunsOnlyAgainstARuntimeWithItsAccelerator refuses a runtime
// that published nothing or another accelerator.
func TestAWorkerRunsOnlyAgainstARuntimeWithItsAccelerator(t *testing.T) {
	dir := t.TempDir()
	if err := runtimeAccelerator(dir, "kvm"); err == nil {
		t.Fatal("a runtime that published no accelerator was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "accelerator"), []byte("tcg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtimeAccelerator(dir, "kvm"); err == nil {
		t.Fatal("a KVM worker accepted a TCG runtime")
	}
	if err := runtimeAccelerator(dir, "tcg"); err != nil {
		t.Fatalf("a TCG worker refused a TCG runtime: %v", err)
	}
}
