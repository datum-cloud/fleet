package controller

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

func exactProfile(name string, generation, cores int64, memory string) *fleetv1.MachineProfile {
	return &fleetv1.MachineProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: generation},
		Spec:       fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: cores}, Memory: fleetv1.MemoryRequirement{Capacity: memory}},
	}
}

func TestResolveRequestedHardwareRecordsProvenance(t *testing.T) {
	machine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "example", Generation: 3}, Spec: fleetv1.MachineSpec{MachineProfileRef: fleetv1.LocalReference{Name: "general-8c-32g"}}}
	profile := exactProfile("general-8c-32g", 2, 8, "32Gi")
	snapshot, err := ResolveRequestedHardware(machine, profile)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CPU.Cores != 8 || snapshot.CPU.Architecture != "amd64" || snapshot.Memory.Capacity != "32Gi" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	source := snapshot.Source
	if source.MachineGeneration != 3 || source.ProfileGeneration != 2 || source.ProfileRef.Name != "general-8c-32g" || !strings.HasPrefix(source.ProfileDigest, "sha256:") || len(source.ProfileDigest) != len("sha256:")+64 {
		t.Fatalf("provenance = %#v", source)
	}
	changed := exactProfile("general-8c-32g", 2, 8, "32Gi")
	changed.Spec.Disks = []fleetv1.DiskRequirement{{Name: "boot"}}
	other, err := ResolveRequestedHardware(machine, changed)
	if err != nil || other.Source.ProfileDigest == source.ProfileDigest {
		t.Fatalf("digest does not distinguish profile contents: %v", err)
	}
	if _, err := ResolveRequestedHardware(machine, exactProfile("another", 1, 8, "32Gi")); !errors.Is(err, ErrProfileNotExact) {
		t.Fatalf("profile name mismatch error = %v", err)
	}
}

func TestValidateExactHardwareRejectsAbsentOrInexactValues(t *testing.T) {
	if err := ValidateExactHardware(exactProfile("p", 1, 8, "32Gi").Spec); err != nil {
		t.Fatal(err)
	}
	for name, spec := range map[string]fleetv1.MachineProfileSpec{
		"zero value":        {},
		"no architecture":   {CPU: fleetv1.CPURequirement{Cores: 8}, Memory: fleetv1.MemoryRequirement{Capacity: "32Gi"}},
		"no cores":          exactProfile("p", 1, 0, "32Gi").Spec,
		"negative cores":    exactProfile("p", 1, -4, "32Gi").Spec,
		"no memory":         exactProfile("p", 1, 8, "").Spec,
		"zero memory":       exactProfile("p", 1, 8, "0").Spec,
		"fractional memory": exactProfile("p", 1, 8, "1.5Gi").Spec,
		"milli memory":      exactProfile("p", 1, 8, "500m").Spec,
		"unknown unit":      exactProfile("p", 1, 8, "32GB").Spec,
	} {
		if err := ValidateExactHardware(spec); !errors.Is(err, ErrProfileNotExact) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestAllocatorMatchesOnlyExactCPUAndMemory(t *testing.T) {
	observed := fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 24}, Memory: fleetv1.HardwareMemory{Capacity: "128Gi"}}
	if !matches(exactProfile("p", 1, 24, "128Gi").Spec, observed) {
		t.Fatal("exact request did not match equal capacity")
	}
	if !matches(exactProfile("p", 1, 24, "131072Mi").Spec, observed) {
		t.Fatal("equal memory in another unit did not match")
	}
	for name, spec := range map[string]fleetv1.MachineProfileSpec{
		"fewer cores":  exactProfile("p", 1, 8, "128Gi").Spec,
		"more cores":   exactProfile("p", 1, 32, "128Gi").Spec,
		"less memory":  exactProfile("p", 1, 24, "64Gi").Spec,
		"inexact zero": exactProfile("p", 1, 0, "128Gi").Spec,
	} {
		if matches(spec, observed) {
			t.Errorf("%s matched 24-core 128Gi capacity", name)
		}
	}
}

func TestSameRequestedHardwareComparesPinnedValues(t *testing.T) {
	pinned := &fleetv1.RequestedHardwareStatus{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 8}, Memory: fleetv1.MemoryRequirement{Capacity: "32Gi"}}
	if !SameRequestedHardware(pinned, exactProfile("p", 1, 8, "32768Mi").Spec) {
		t.Fatal("equal request reported as changed")
	}
	if SameRequestedHardware(pinned, exactProfile("p", 1, 16, "32Gi").Spec) || SameRequestedHardware(pinned, exactProfile("p", 1, 8, "64Gi").Spec) || SameRequestedHardware(nil, exactProfile("p", 1, 8, "32Gi").Spec) {
		t.Fatal("changed request reported as equal")
	}
}
