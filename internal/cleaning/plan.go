package cleaning

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

var (
	// ErrUncovered means a server has a drive the policy cannot clean, or
	// storage Fleet cannot account for. Its capacity is not offered.
	ErrUncovered = errors.New("CleaningUncovered")
	// ErrStorageUnknown means discovery has not reported the server's drives.
	ErrStorageUnknown = errors.New("the server's drives are not known")
)

// MaxDrives bounds a manifest and a plan.
const MaxDrives = 16

func driveOf(d inventory.ObservedDrive) Drive {
	return Drive{Protocol: ops.DriveProtocol(d.Protocol), MediaType: ops.MediaType(d.MediaType), Model: d.Model, Firmware: d.Firmware}
}

// Manifest pins a server's drives under a policy. Every drive must be
// addressed directly by the host, identify itself, and have a planned method;
// otherwise the server is not covered. Drives are named disk-0, disk-1 and so
// on, ordered by protocol and serial number.
func Manifest(spec ops.CleaningPolicySpec, storage *inventory.ObservedStorage) ([]ops.ManifestDrive, error) {
	if storage == nil {
		return nil, fmt.Errorf("%w: %w", ErrUncovered, ErrStorageUnknown)
	}
	if len(storage.Drives) == 0 {
		return nil, fmt.Errorf("%w: discovery reports no drives", ErrUncovered)
	}
	if len(storage.Drives) > MaxDrives {
		return nil, fmt.Errorf("%w: more than %d drives", ErrUncovered, MaxDrives)
	}
	drives := slices.Clone(storage.Drives)
	slices.SortFunc(drives, func(a, b inventory.ObservedDrive) int {
		return strings.Compare(a.Protocol+"\x00"+a.SerialNumber, b.Protocol+"\x00"+b.SerialNumber)
	})
	var out []ops.ManifestDrive
	seen := map[string]bool{}
	for i, d := range drives {
		switch {
		case d.InVolume:
			return nil, fmt.Errorf("%w: drive %s backs a RAID volume", ErrUncovered, d.ID)
		case d.SerialNumber == "" || d.CapacityBytes <= 0:
			return nil, fmt.Errorf("%w: drive %s reports no serial number or capacity", ErrUncovered, d.ID)
		case seen[d.SerialNumber]:
			return nil, fmt.Errorf("%w: two drives report serial number %s", ErrUncovered, d.SerialNumber)
		}
		seen[d.SerialNumber] = true
		rule := Match(spec, driveOf(d))
		if rule == nil || len(Methods(spec, *rule, driveOf(d))) == 0 {
			return nil, fmt.Errorf("%w: no qualified method cleans drive %s (%s %s %s)", ErrUncovered, d.SerialNumber, d.Protocol, d.MediaType, d.Model)
		}
		out = append(out, ops.ManifestDrive{Name: "disk-" + strconv.Itoa(i), SerialNumber: d.SerialNumber, Model: d.Model, Firmware: d.Firmware,
			Protocol: ops.DriveProtocol(d.Protocol), MediaType: ops.MediaType(d.MediaType), CapacityBytes: d.CapacityBytes, Rule: rule.Name})
	}
	return out, nil
}

// Plan is the one shared planner. The release controller uses it to create a
// wipe, and admission uses it to check one. Every pinned drive that was not
// disposed of gets a step, and so does every drive discovery reports now that
// the manifest lacks. Methods come from the pinned policy and the drive as
// discovery reports it now, so changed firmware must still be qualified. A
// pinned drive discovery no longer reports is Missing. A drive with no
// planned method is Uncovered, and so is every drive that shares its serial
// number with another, a pinned drive whose model, protocol, media type or
// capacity changed, and a drive that reports no serial number: none of them
// can be matched to one physical drive.
func Plan(policy ops.CleaningPolicySpec, manifest []ops.ManifestDrive, storage *inventory.ObservedStorage, disposed []string) ([]ops.PlannedDrive, error) {
	if storage == nil {
		return nil, ErrStorageUnknown
	}
	current := map[string]inventory.ObservedDrive{}
	count := map[string]int{}
	for _, d := range storage.Drives {
		count[d.SerialNumber]++
		if d.SerialNumber != "" {
			current[d.SerialNumber] = d
		}
	}
	unmatchable := func(d inventory.ObservedDrive) bool { return d.SerialNumber == "" || count[d.SerialNumber] > 1 }
	uncovered := func(p ops.PlannedDrive) ops.PlannedDrive {
		p.State, p.Methods, p.Verify, p.TimePerTiB, p.Rule = ops.DriveUncovered, nil, nil, nil, ""
		return p
	}
	step := func(name string, d inventory.ObservedDrive) ops.PlannedDrive {
		planned := ops.PlannedDrive{ManifestDrive: ops.ManifestDrive{Name: name, SerialNumber: d.SerialNumber, Model: d.Model, Firmware: d.Firmware,
			Protocol: ops.DriveProtocol(d.Protocol), MediaType: ops.MediaType(d.MediaType), CapacityBytes: d.CapacityBytes}}
		rule := Match(policy, driveOf(d))
		var methods []ops.CleaningMethod
		if rule != nil && !d.InVolume {
			methods = Methods(policy, *rule, driveOf(d))
		}
		if len(methods) == 0 {
			planned.State = ops.DriveUncovered
			return planned
		}
		verify, bound := rule.Verify, rule.TimePerTiB
		planned.Rule, planned.Methods, planned.Verify, planned.TimePerTiB = rule.Name, methods, &verify, &bound
		return planned
	}
	var out []ops.PlannedDrive
	pinned := map[string]bool{}
	used := map[string]bool{}
	for _, m := range manifest {
		pinned[m.SerialNumber] = true
		used[m.Name] = true
		if slices.Contains(disposed, m.Name) {
			continue
		}
		d, ok := current[m.SerialNumber]
		switch {
		case !ok:
			out = append(out, ops.PlannedDrive{ManifestDrive: m, State: ops.DriveMissing})
		case unmatchable(d) || d.Model != m.Model || ops.DriveProtocol(d.Protocol) != m.Protocol || ops.MediaType(d.MediaType) != m.MediaType || d.CapacityBytes != m.CapacityBytes:
			out = append(out, uncovered(ops.PlannedDrive{ManifestDrive: m}))
		default:
			out = append(out, step(m.Name, d))
		}
	}
	// Every drive not planned above gets a step: a new drive, a drive with
	// no serial number, and every extra drive sharing a serial number.
	var added []inventory.ObservedDrive
	planned := map[string]bool{}
	for _, d := range storage.Drives {
		if pinned[d.SerialNumber] && !planned[d.SerialNumber] {
			planned[d.SerialNumber] = true
			continue
		}
		added = append(added, d)
	}
	slices.SortFunc(added, func(a, b inventory.ObservedDrive) int {
		return strings.Compare(a.Protocol+"\x00"+a.SerialNumber+"\x00"+a.ID, b.Protocol+"\x00"+b.SerialNumber+"\x00"+b.ID)
	})
	next := 0
	for _, d := range added {
		for used["disk-"+strconv.Itoa(next)] {
			next++
		}
		name := "disk-" + strconv.Itoa(next)
		used[name] = true
		step := step(name, d)
		if unmatchable(d) {
			step = uncovered(step)
		}
		out = append(out, step)
	}
	if len(out) > MaxDrives {
		return nil, fmt.Errorf("a plan has at most %d drives", MaxDrives)
	}
	return out, nil
}

// Cleanable reports whether every planned drive can be cleaned.
func Cleanable(plan []ops.PlannedDrive) bool {
	return !slices.ContainsFunc(plan, func(d ops.PlannedDrive) bool { return d.State != "" })
}

// StorageOf returns the drives discovery last observed for the Device's
// system: the System whose UUID is the Device's, or the only System when the
// Device records no UUID. It is nil when discovery has not reported storage.
func StorageOf(device *inventory.Device) *inventory.ObservedStorage {
	if device == nil || device.Status.Discovery == nil || device.Status.Discovery.Inventory == nil {
		return nil
	}
	systems := device.Status.Discovery.Inventory.Systems
	want := strings.ToLower(device.Spec.Identity.SystemUUID)
	var found *inventory.ObservedSystem
	for i := range systems {
		if want == "" && len(systems) == 1 || want != "" && strings.ToLower(systems[i].UUID) == want {
			if found != nil {
				return nil
			}
			found = &systems[i]
		}
	}
	if found == nil {
		return nil
	}
	return found.Storage
}
