package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// ReasonLegacyCustodyRecord is the Machine condition reason used when the
// retained private allocation for a Machine was accepted under the pre-exact
// MachineProfile CPU and memory contract. The allocation is never resumed; it
// is surfaced as not bound and not ready until the Machine is deleted and the
// normal verified release path returns the capacity.
const ReasonLegacyCustodyRecord = "LegacyCustodyRecord"

// ErrLegacyRecord identifies a retained private record whose accepted
// requirements use the CPU and memory constraint shape that predates exact
// requests. It wraps ErrRecordInvalid so every caller that fails closed on an
// invalid record keeps doing so for continued use. Only release and cleanup
// may consume such a record.
var ErrLegacyRecord = fmt.Errorf("%w: retained allocation predates exact CPU and memory requirements and can only be released", ErrRecordInvalid)

// RetainedRequirements is the private-record encoding of the MachineProfile
// requirements accepted for one allocation. It exists so that records written
// before CPU cores and memory capacity became exact values still decode for
// release, without changing the public MachineProfileSpec or its CRD.
//
// An exact value marshals byte-for-byte like the MachineProfileSpec it wraps,
// so record comparisons and requirement digests are unchanged. A legacy value
// keeps its original JSON verbatim and re-encodes it unchanged, so retained
// inputs (and any digest over them) are preserved exactly across rewrites; its
// requirements are deliberately not exposed as a MachineProfileSpec.
type RetainedRequirements struct {
	spec   fleetv1.MachineProfileSpec
	legacy json.RawMessage
}

// ExactRequirements wraps requirements accepted under the exact contract.
func ExactRequirements(spec fleetv1.MachineProfileSpec) RetainedRequirements {
	return RetainedRequirements{spec: spec}
}

// Legacy reports whether the retained requirements use the pre-exact CPU and
// memory constraint shape.
func (r RetainedRequirements) Legacy() bool { return r.legacy != nil }

// Exact returns the retained requirements, or ErrLegacyRecord when they predate
// exact CPU and memory requests and therefore cannot satisfy any current use.
func (r RetainedRequirements) Exact() (fleetv1.MachineProfileSpec, error) {
	if r.Legacy() {
		return fleetv1.MachineProfileSpec{}, ErrLegacyRecord
	}
	return r.spec, nil
}

func (r RetainedRequirements) MarshalJSON() ([]byte, error) {
	if r.Legacy() {
		return append([]byte(nil), r.legacy...), nil
	}
	return json.Marshal(r.spec)
}

// UnmarshalJSON accepts the current exact shape, and otherwise only the
// complete pre-exact shape: cpu.cores as a {min,max,exact} count constraint
// and memory.capacity as a {min,max,exact} quantity constraint, with no
// unknown fields. A mixture of the two shapes was never written and is
// rejected.
func (r *RetainedRequirements) UnmarshalJSON(data []byte) error {
	var spec fleetv1.MachineProfileSpec
	exactErr := json.Unmarshal(data, &spec)
	if exactErr == nil {
		*r = RetainedRequirements{spec: spec}
		return nil
	}
	var legacy legacyProfileSpec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return exactErr
	}
	if decoder.More() {
		return errors.New("trailing data after retained requirements")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return err
	}
	*r = RetainedRequirements{legacy: compact.Bytes()}
	return nil
}

// legacyProfileSpec is the MachineProfileSpec shape retained before CPU cores
// and memory capacity became exact. It is used only to recognize that shape.
type legacyProfileSpec struct {
	CPU struct {
		Architecture string             `json:"architecture"`
		Cores        fleetv1.Constraint `json:"cores"`
	} `json:"cpu"`
	Memory struct {
		Capacity fleetv1.QuantityConstraint `json:"capacity"`
	} `json:"memory"`
	GPUs       *fleetv1.GPURequirement        `json:"gpus,omitempty"`
	Disks      []fleetv1.DiskRequirement      `json:"disks,omitempty"`
	Interfaces []fleetv1.InterfaceRequirement `json:"interfaces,omitempty"`
}
