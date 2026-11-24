package devicepower

import (
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
)

// Refusal is why the dispatcher refuses an operation, with nothing sent.
type Refusal struct {
	Reason, Message string
}

// Acceptance is what the dispatcher read for one operation's checks.
type Acceptance struct {
	Operation *inventory.DevicePowerOperation
	// Device is nil when it does not exist in the operation's namespace.
	Device *inventory.Device
	// Capacities are the MachineCapacities in the operation's namespace.
	Capacities []metal.MachineCapacity
	// Others are the other operations in the namespace.
	Others []inventory.DevicePowerOperation
	// DiscoveryMaxAge bounds how old discovery's inventory read may be.
	DiscoveryMaxAge time.Duration
	Now             time.Time
}

// BacksCapacity reports whether any MachineCapacity names the Device.
func BacksCapacity(capacities []metal.MachineCapacity, device string) bool {
	return slices.ContainsFunc(capacities, func(c metal.MachineCapacity) bool { return c.Spec.DeviceRef.Name == device })
}

// Running reports whether an operation was accepted and has no result.
func Running(op *inventory.DevicePowerOperation) bool {
	return op.Status.Result == "" && meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerAccepted)
}

// Check runs the acceptance checks in order and returns the first refusal,
// or nil when the operation may be accepted. It reads only what it is given.
func Check(a Acceptance) *Refusal {
	op, device := a.Operation, a.Device
	refuse := func(reason, message string) *Refusal { return &Refusal{Reason: reason, Message: message} }
	if a.Now.Sub(op.CreationTimestamp.Time) > AcceptanceWindow {
		return refuse(inventory.ReasonExpired, "the operation waited more than five minutes to be accepted; create a new one")
	}
	if device == nil {
		return refuse(inventory.ReasonDeviceNotFound, "the Device does not exist in this namespace")
	}
	if inventory.LifecycleOf(device) == inventory.DeviceRetired {
		return refuse(inventory.ReasonDeviceRetired, "the Device is retired")
	}
	if BacksCapacity(a.Capacities, device.Name) {
		return refuse(inventory.ReasonCapacityBacked, "customer capacity is defined on the Device; its power belongs to the allocation")
	}
	for i := range a.Others {
		other := &a.Others[i]
		if other.UID != op.UID && other.Spec.DeviceRef.Name == device.Name && Running(other) {
			return refuse(inventory.ReasonOperationInProgress, "operation "+other.Name+" on this Device is still running")
		}
	}
	if device.Spec.Identity.SystemUUID == "" {
		return refuse(inventory.ReasonSystemUUIDNotRecorded, "the Device has no system UUID on record")
	}
	if device.Spec.Identity.SystemUUID != op.Spec.ExpectedSystemUUID {
		return refuse(inventory.ReasonSystemUUIDMismatch, "spec.expectedSystemUUID differs from the Device's recorded system UUID")
	}
	status := device.Status
	discovery := status.Discovery
	if device.Spec.BMC == nil || discovery == nil || discovery.Inventory == nil || discovery.TLS == nil ||
		discovery.Inventory.Endpoint != device.Spec.BMC.Endpoint || discovery.TLS.Endpoint != device.Spec.BMC.Endpoint ||
		a.Now.Sub(discovery.Inventory.ObservedAt.Time) > a.DiscoveryMaxAge ||
		!current(status, inventory.ConditionDiscovered, device.Generation) || !current(status, inventory.ConditionIdentityMatched, device.Generation) {
		return refuse(inventory.ReasonDiscoveryNotCurrent, "discovery has not recently read this BMC and matched the Device's identity")
	}
	var systems []inventory.ObservedSystem
	for _, s := range discovery.Inventory.Systems {
		if s.UUID == op.Spec.ExpectedSystemUUID {
			systems = append(systems, s)
		}
	}
	if len(systems) != 1 {
		return refuse(inventory.ReasonSystemNotObserved, "discovery did not see exactly one server with the expected UUID")
	}
	if c := meta.FindStatusCondition(status.Conditions, inventory.ConditionPublicKeyUnique); c == nil || c.Status != metav1.ConditionTrue {
		if c != nil && c.Status == metav1.ConditionFalse {
			return refuse(inventory.ReasonSharedPublicKey, "another BMC presents the same TLS key, so the key cannot tell them apart")
		}
		return refuse(inventory.ReasonDiscoveryNotCurrent, "discovery has not compared this BMC's TLS key with other BMCs")
	}
	if !slices.Contains(systems[0].ResetTypes, string(op.Spec.Action)) {
		return refuse(inventory.ReasonResetTypeNotOffered, "the server does not offer "+string(op.Spec.Action))
	}
	return nil
}

// current reports whether a Device condition is True for the Device's
// current generation.
func current(status inventory.DeviceStatus, kind string, generation int64) bool {
	c := meta.FindStatusCondition(status.Conditions, kind)
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == generation
}
