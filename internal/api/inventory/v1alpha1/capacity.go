package v1alpha1

import "errors"

var (
	// ErrUnidentifiedDevice means a Device was recorded without hardware
	// identity and cannot back MachineCapacity.
	ErrUnidentifiedDevice = errors.New("device has no recorded hardware identity and cannot back machine capacity")
	// ErrDeviceIdentityMissing means a Device carries neither a serial nor a
	// system UUID, for example a legacy record whose only identity was pruned.
	ErrDeviceIdentityMissing = errors.New("device has no serial or system UUID and cannot back machine capacity")
)

// CapacityBackingError reports why a Device cannot back a MachineCapacity, or
// nil when it may. Nothing resolves MachineCapacity.spec.deviceRef yet; any
// resolver that does must call this and treat a non-nil result as ineligible.
// It checks only recorded identity, which is matching evidence: a nil result
// is not verification or allocator eligibility.
func CapacityBackingError(device *Device) error {
	if device == nil {
		return ErrDeviceIdentityMissing
	}
	identity := device.Spec.Identity
	if identity.Unidentified || identity.UnidentifiedReason != "" {
		return ErrUnidentifiedDevice
	}
	if identity.Serial == "" && identity.SystemUUID == "" {
		return ErrDeviceIdentityMissing
	}
	return nil
}
