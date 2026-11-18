package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/api/resource"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// The RequestResolved condition reports whether a Machine's exact requested
// CPU and memory have been resolved from its MachineProfile and pinned in
// status.requestedHardware. It never reports allocation or readiness.
const (
	ConditionRequestResolved = "RequestResolved"

	// ReasonRequestResolved: the snapshot is present and pinned.
	ReasonRequestResolved = "Resolved"
	// ReasonProfileNotFound: the referenced MachineProfile does not exist.
	ReasonProfileNotFound = "ProfileNotFound"
	// ReasonProfileDeleting: the referenced MachineProfile is being deleted.
	ReasonProfileDeleting = "ProfileDeleting"
	// ReasonProfileInvalid: the profile does not request exact CPU and memory.
	ReasonProfileInvalid = "ProfileInvalid"
)

// ErrProfileNotExact identifies a MachineProfile whose CPU or memory request is
// missing, non-positive, fractional, or otherwise not one exact value.
var ErrProfileNotExact = errors.New("machine profile does not request exact CPU and memory")

// exactByteQuantity mirrors the CRD pattern: a positive integer with an
// optional binary or decimal suffix. Milli, exponent, signed, and fractional
// forms are rejected so the requested value is one unambiguous byte count.
var exactByteQuantity = regexp.MustCompile(`^[1-9][0-9]*(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$`)

// ValidateExactHardware checks the CPU and memory request independently of
// CRD validation, because stored objects can predate the current schema.
func ValidateExactHardware(spec fleetv1.MachineProfileSpec) error {
	if spec.CPU.Architecture == "" {
		return fmt.Errorf("%w: cpu.architecture is required", ErrProfileNotExact)
	}
	if spec.CPU.Cores < 1 {
		return fmt.Errorf("%w: cpu.cores must be a positive integer", ErrProfileNotExact)
	}
	if _, err := exactMemory(spec.Memory.Capacity); err != nil {
		return err
	}
	return nil
}

func exactMemory(value string) (resource.Quantity, error) {
	if !exactByteQuantity.MatchString(value) {
		return resource.Quantity{}, fmt.Errorf("%w: memory.capacity must be a positive integer byte quantity", ErrProfileNotExact)
	}
	quantity, err := resource.ParseQuantity(value)
	if err != nil || quantity.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("%w: memory.capacity must be a positive integer byte quantity", ErrProfileNotExact)
	}
	return quantity, nil
}

// ProfileSpecDigest identifies the complete resolved profile contents.
func ProfileSpecDigest(spec fleetv1.MachineProfileSpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ResolveRequestedHardware derives the snapshot for machine from profile. It
// never defaults or chooses a value: an inexact profile is an error.
func ResolveRequestedHardware(machine *fleetv1.Machine, profile *fleetv1.MachineProfile) (*fleetv1.RequestedHardwareStatus, error) {
	if machine == nil || profile == nil || profile.Name != machine.Spec.MachineProfileRef.Name {
		return nil, fmt.Errorf("%w: profile does not match the Machine reference", ErrProfileNotExact)
	}
	if err := ValidateExactHardware(profile.Spec); err != nil {
		return nil, err
	}
	digest, err := ProfileSpecDigest(profile.Spec)
	if err != nil {
		return nil, err
	}
	return &fleetv1.RequestedHardwareStatus{
		CPU:    fleetv1.CPURequirement{Architecture: profile.Spec.CPU.Architecture, Cores: profile.Spec.CPU.Cores},
		Memory: fleetv1.MemoryRequirement{Capacity: profile.Spec.Memory.Capacity},
		Source: fleetv1.RequestedHardwareSource{
			MachineGeneration: machine.Generation,
			ProfileRef:        fleetv1.LocalReference{Name: profile.Name},
			ProfileGeneration: profile.Generation,
			ProfileDigest:     digest,
		},
	}, nil
}

// SameRequestedHardware reports whether profile still requests exactly the
// pinned CPU and memory. Quantities compare by value, so 1Gi equals 1024Mi.
func SameRequestedHardware(pinned *fleetv1.RequestedHardwareStatus, profile fleetv1.MachineProfileSpec) bool {
	if pinned == nil || ValidateExactHardware(profile) != nil {
		return false
	}
	want, err := exactMemory(pinned.Memory.Capacity)
	if err != nil {
		return false
	}
	got, _ := exactMemory(profile.Memory.Capacity)
	return pinned.CPU.Architecture == profile.CPU.Architecture && pinned.CPU.Cores == profile.CPU.Cores && want.Cmp(got) == 0
}

// sameMemory compares an exact requested capacity with an observed one by
// value. Missing or unparseable observations never match.
func sameMemory(requested, observed string) bool {
	want, err := exactMemory(requested)
	if err != nil {
		return false
	}
	got, err := resource.ParseQuantity(observed)
	return err == nil && want.Cmp(got) == 0
}
