// Package milo holds Fleet's Milo service identity and its quota claim for a
// Machine.
package milo

import "errors"

var (
	// ErrQuotaPending means the quota claim exists and Milo has not granted
	// it yet. The allocator retries.
	ErrQuotaPending = errors.New("Milo quota grant is pending")
	// ErrQuotaUnknown means the claim could not be read or written, or is
	// being deleted; its outcome is unknown.
	ErrQuotaUnknown = errors.New("Milo quota outcome is unknown")
	// ErrQuotaInvalid means a claim with the Machine's name is not the claim
	// Fleet would create for it.
	ErrQuotaInvalid = errors.New("the quota claim does not match the Machine")
)

const (
	ServiceName          = "metal.datumapis.com"
	EntitlementName      = "metal"
	MachineQuotaResource = "metal.datumapis.com/machines"
)
