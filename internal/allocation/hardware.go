package allocation

import (
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

func matches(profile fleetv1.MachineProfileSpec, hardware fleetv1.Hardware) bool {
	// CPU and memory are exact requests. An inexact profile fails closed, and
	// capacity matches only when its observed cores and installed memory equal
	// the request; larger machines are not silently substituted.
	if controller.ValidateExactHardware(profile) != nil || profile.CPU.Architecture != hardware.CPU.Architecture || profile.CPU.Cores != hardware.CPU.Cores || !sameMemory(profile.Memory.Capacity, hardware.Memory.Capacity) {
		return false
	}
	var gpuCount int64
	for _, gpu := range hardware.GPUs {
		if profile.GPUs != nil && (profile.GPUs.Vendor == "" || profile.GPUs.Vendor == gpu.Vendor) && (len(profile.GPUs.Models) == 0 || slices.Contains(profile.GPUs.Models, gpu.Model)) && matchesQuantity(profile.GPUs.MemoryPerDevice, gpu.Memory) {
			gpuCount++
		}
	}
	if profile.GPUs != nil && !matchesCount(profile.GPUs.Count, gpuCount) {
		return false
	}
	used := make([]bool, len(hardware.Disks))
	for _, group := range profile.Disks {
		var candidates []int
		for i, disk := range hardware.Disks {
			if !used[i] && (group.Media == "" || group.Media == disk.Media) && (group.Interface == "" || group.Interface == disk.Interface) && matchesQuantity(group.Capacity, disk.Capacity) {
				candidates = append(candidates, i)
			}
		}
		if !matchesCount(group.Count, int64(len(candidates))) {
			return false
		}
		count := requiredCount(group.Count)
		// A disk group always requires at least one distinct device when no
		// explicit minimum or exact count is supplied. Fail closed on malformed
		// negative counts and on groups with too few candidates before slicing.
		if count < 0 || int64(len(candidates)) < count {
			return false
		}
		for _, index := range candidates[:count] {
			used[index] = true
		}
	}
	usedInterfaces := make([]bool, len(hardware.Interfaces))
	for _, requirement := range profile.Interfaces {
		found := false
		for i, iface := range hardware.Interfaces {
			if !usedInterfaces[i] && (requirement.Type == "" || requirement.Type == iface.Type) && matchesQuantity(requirement.Speed, iface.Speed) {
				usedInterfaces[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func requiredCount(c fleetv1.Constraint) int64 {
	if c.Exact != nil {
		return *c.Exact
	}
	if c.Min != nil {
		return *c.Min
	}
	return 1
}
func matchesCount(c fleetv1.Constraint, value int64) bool {
	if c.Exact != nil {
		return value == *c.Exact
	}
	return (c.Min == nil || value >= *c.Min) && (c.Max == nil || value <= *c.Max)
}
func matchesQuantity(c fleetv1.QuantityConstraint, value string) bool {
	if c.Min == "" && c.Max == "" && c.Exact == "" {
		return true
	}
	v, err := resource.ParseQuantity(value)
	if err != nil {
		return false
	}
	if c.Exact != "" {
		q, err := resource.ParseQuantity(c.Exact)
		return err == nil && v.Cmp(q) == 0
	}
	if c.Min != "" {
		q, err := resource.ParseQuantity(c.Min)
		if err != nil || v.Cmp(q) < 0 {
			return false
		}
	}
	if c.Max != "" {
		q, err := resource.ParseQuantity(c.Max)
		if err != nil || v.Cmp(q) > 0 {
			return false
		}
	}
	return true
}

func sameMemory(requested, observed string) bool {
	want, err := resource.ParseQuantity(requested)
	if err != nil {
		return false
	}
	got, err := resource.ParseQuantity(observed)
	return err == nil && want.Cmp(got) == 0
}
