package custody

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PowerDeadline bounds one power operation at the site: the two-minute
// start-by window, four BMC requests of at most 30 seconds each, the
// three-minute observation of the outcome, and a minute of margin.
const PowerDeadline = 8 * time.Minute

// Power refusal reasons. SystemUUIDMismatch and ResetTypeNotOffered are
// raised by the coordinator and the site worker, NotPoweredOn by the worker
// only, and PowerStateNotSettled by the worker before a command and by the
// settle hook. Stale is the site framework's shared reason.
const (
	ReasonSystemUUIDNotRecorded = "SystemUUIDNotRecorded"
	ReasonSystemUUIDMismatch    = "SystemUUIDMismatch"
	ReasonDiscoveryNotCurrent   = "DiscoveryNotCurrent"
	ReasonResetTypeNotOffered   = "ResetTypeNotOffered"
	ReasonNotPoweredOn          = "NotPoweredOn"
	// The reasons a DeviceRecovery Settle of a power hold is refused with.
	ReasonPowerEvidenceNotCurrent    = "PowerEvidenceNotCurrent"
	ReasonPowerEvidenceWrongEndpoint = "PowerEvidenceWrongEndpoint"
	ReasonPowerStateNotSettled       = "PowerStateNotSettled"
	ReasonPowerEvidenceMismatch      = "PowerEvidenceMismatch"
)

// Snapshot fields power adds at admission.
const (
	SnapshotResetTypes           = "resetTypes"
	SnapshotSystemID             = "systemID"
	SnapshotResetTypesObservedAt = "resetTypesObservedAt"
)

// SettlePowerState is the DeviceRecovery evidence field that cites the
// settled power state discovery read.
const SettlePowerState = "powerState"

const powerEvidenceLimit = 1024

// PowerName is the only name a provider power operation may have: its
// allocation and the Machine generation whose power intent it carries out.
func PowerName(allocation string, machineGeneration int64) string {
	return allocation + "-power-" + strconv.FormatInt(machineGeneration, 10)
}

func providerPowerName(operation ops.Operation) string {
	return PowerName(allocationName(operation), operation.(*ops.DevicePowerOperation).Spec.MachineGeneration)
}

// DevicePowerOperationKind is the registry entry for DevicePowerOperation.
func DevicePowerOperationKind() Kind {
	return Kind{
		GVK:     gv.WithKind("DevicePowerOperation"),
		New:     func() ops.Operation { return &ops.DevicePowerOperation{} },
		NewList: func() client.ObjectList { return &ops.DevicePowerOperationList{} },
		Items: func(list client.ObjectList) []ops.Operation {
			return items(list.(*ops.DevicePowerOperationList).Items)
		},
		Digest: func(operation ops.Operation) (string, error) {
			return SpecDigest(operation.(*ops.DevicePowerOperation).Spec)
		},
		// Staff operations have generated names. The provider row names its
		// operations from the Machine generation instead.
		Naming:    GeneratedName,
		Deadline:  PowerDeadline,
		Effectful: true,
		// A newer pending provider operation for the same allocation
		// replaces an older one, so only the latest power intent runs.
		Supersedes: true,
		Check:      checkPower,
		// A power success settles only a power hold, and only after a
		// DeviceRecovery with Retry. It settles no other kind's hold.
		Settles:   []SettleRule{{Held: "DevicePowerOperation", RequiresRetry: true}},
		Retryable: true,
		Reasons: []Reason{
			{Name: ReasonSystemUUIDNotRecorded, RaisedBy: RaisedByCoordinator},
			{Name: ReasonSystemUUIDMismatch, RaisedBy: RaisedByBoth},
			{Name: ReasonDiscoveryNotCurrent, RaisedBy: RaisedByCoordinator},
			{Name: ReasonResetTypeNotOffered, RaisedBy: RaisedByBoth},
			{Name: ReasonPowerEvidenceNotCurrent, RaisedBy: RaisedByCoordinator},
			{Name: ReasonPowerEvidenceWrongEndpoint, RaisedBy: RaisedByCoordinator},
			{Name: ReasonPowerStateNotSettled, RaisedBy: RaisedByBoth},
			{Name: ReasonNotPoweredOn, RaisedBy: RaisedByWorker},
			{Name: ReasonPowerEvidenceMismatch, RaisedBy: RaisedByCoordinator},
		},
		SnapshotExtra:    powerSnapshot,
		Evidence:         StrictEvidence(powerEvidenceLimit, validatePowerEvidence),
		EvidenceRequired: true,
		RecoverySettle:   settlePower,
	}
}

// PowerRules are the two power rows of the #174 operations table. The staff
// row "None, or Service under a power hold" is two rows.
func PowerRules() []Rule {
	return []Rule{
		{Kind: "DevicePowerOperation", Creator: CreatorMetalProvider, Authority: AuthorityAllocation, Holders: []ops.HolderType{ops.HolderAllocation}, Queue: true,
			Naming: DeterministicName, Name: providerPowerName},
		{Kind: "DevicePowerOperation", Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderNone}},
		{Kind: "DevicePowerOperation", Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderService}, Hold: HoldSettlesRequired},
	}
}

// discoveredSystem returns the system discovery read with the Device's
// recorded system UUID, or nil.
func discoveredSystem(device *inventory.Device) *inventory.ObservedSystem {
	discovery := device.Status.Discovery
	uuid := device.Spec.Identity.SystemUUID
	if discovery == nil || discovery.Inventory == nil || uuid == "" {
		return nil
	}
	for index := range discovery.Inventory.Systems {
		if system := &discovery.Inventory.Systems[index]; strings.EqualFold(system.UUID, uuid) {
			return system
		}
	}
	return nil
}

// checkPower is power's rule 7: its own inputs, and the discovery facts
// they are checked against.
func checkPower(_ context.Context, admission *Admission) *Refusal {
	power := admission.Operation.(*ops.DevicePowerOperation)
	device := admission.Device
	recorded := device.Spec.Identity.SystemUUID
	if power.Spec.Authority.ServiceRequest != nil {
		if recorded == "" {
			return refuse(ReasonSystemUUIDNotRecorded, "the Device has no system UUID on record")
		}
		if !strings.EqualFold(power.Spec.ExpectedSystemUUID, recorded) {
			return refuse(ReasonSystemUUIDMismatch, "expectedSystemUUID %s is not the recorded system UUID", power.Spec.ExpectedSystemUUID)
		}
	}
	// The facts below must come from a read of the current BMC within twice
	// the discovery interval.
	discovery := device.Status.Discovery
	maxAge := 2 * admission.DiscoveryInterval
	if discovery == nil || discovery.Inventory == nil || device.Spec.BMC == nil || discovery.Inventory.Endpoint != device.Spec.BMC.Endpoint {
		return refuse(ReasonDiscoveryNotCurrent, "discovery has not read the inventory at the current BMC endpoint")
	}
	if age := admission.Now.Sub(discovery.Inventory.ObservedAt.Time); age > maxAge {
		return refuse(ReasonDiscoveryNotCurrent, "discovery last read the BMC %s ago; the limit is %s", age.Truncate(time.Second), maxAge)
	}
	system := discoveredSystem(device)
	if system == nil {
		return refuse(ReasonDiscoveryNotCurrent, "discovery has not read the system with the recorded UUID")
	}
	if !slices.Contains(system.ResetTypes, string(power.Spec.Action)) {
		return refuse(ReasonResetTypeNotOffered, "the BMC does not offer %s for system %s", power.Spec.Action, system.ID)
	}
	return nil
}

// powerSnapshot records what the reset-type check read: the power actions
// the system offered, the system, and when discovery read them.
func powerSnapshot(admission *Admission) map[string]string {
	system := discoveredSystem(admission.Device)
	if system == nil {
		return nil
	}
	var offered []string
	for _, action := range ops.PowerActions {
		if slices.Contains(system.ResetTypes, string(action)) {
			offered = append(offered, string(action))
		}
	}
	return map[string]string{
		SnapshotResetTypes:           strings.Join(offered, ","),
		SnapshotSystemID:             system.ID,
		SnapshotResetTypesObservedAt: admission.Device.Status.Discovery.Inventory.ObservedAt.UTC().Format(time.RFC3339),
	}
}

// validatePowerEvidence bounds power result evidence and checks it against
// the operation's action. The hook does not see the result's phase, so it
// checks only what holds for a success and a refusal alike.
func validatePowerEvidence(operation ops.Operation, evidence *ops.PowerEvidence) error {
	observation := func(name string, value *ops.PowerObservation) error {
		if value == nil {
			return nil
		}
		switch value.State {
		case ops.PowerStateOn, ops.PowerStateOff, ops.PowerStatePoweringOn, ops.PowerStatePoweringOff, ops.PowerStatePaused:
		default:
			return errors.New("power." + name + ".state is not a Redfish power state")
		}
		if value.ObservedAt.IsZero() {
			return errors.New("power." + name + ".observedAt is required")
		}
		return nil
	}
	var before, after *ops.PowerObservation
	if evidence.Power != nil {
		before, after = evidence.Power.Before, evidence.Power.After
	}
	if err := observation("before", before); err != nil {
		return err
	}
	if err := observation("after", after); err != nil {
		return err
	}
	sent := evidence.RequestSentAt
	switch {
	case sent != nil && sent.IsZero():
		return errors.New("requestSentAt is set but empty")
	case before != nil && after != nil && after.ObservedAt.Before(&before.ObservedAt):
		return errors.New("power.after was observed before power.before")
	case sent != nil && before != nil && sent.Before(&before.ObservedAt):
		return errors.New("the command was sent before power.before was read")
	case sent != nil && after != nil && after.ObservedAt.Before(sent):
		return errors.New("power.after was read before the command was sent")
	}
	switch evidence.ResetEvidence {
	case "":
	case ops.ResetLastResetTime, ops.ResetObservedOff:
		if sent == nil {
			return errors.New("resetEvidence needs the command it proves")
		}
		if action := operation.(*ops.DevicePowerOperation).Spec.Action; !action.Resets() {
			return errors.New("resetEvidence proves a restart or power cycle, not " + string(action))
		}
	default:
		return errors.New("resetEvidence is LastResetTime or ObservedOff")
	}
	return nil
}

// settlePower accepts a DeviceRecovery Settle of a power hold only when the
// Device's discovery, read after the held operation ended, from the Device's
// current BMC endpoint, shows a settled power state, and the recovery cites
// that state.
func settlePower(_ context.Context, settle *RecoverySettleContext) *Refusal {
	device := settle.Device
	if device == nil || device.Spec.BMC == nil || device.Status.Discovery == nil || device.Status.Discovery.Inventory == nil {
		return refuse(ReasonPowerEvidenceNotCurrent, "discovery has not read the BMC since the power operation ended")
	}
	read := device.Status.Discovery.Inventory
	if !read.ObservedAt.After(settle.Hold.Since.Time) {
		return refuse(ReasonPowerEvidenceNotCurrent, "discovery last read the BMC at %s, before the power operation ended at %s", read.ObservedAt.UTC().Format(time.RFC3339), settle.Hold.Since.UTC().Format(time.RFC3339))
	}
	if read.Endpoint != device.Spec.BMC.Endpoint {
		return refuse(ReasonPowerEvidenceWrongEndpoint, "discovery read %s, not the Device's current BMC endpoint", read.Endpoint)
	}
	system := discoveredSystem(device)
	if system == nil {
		return refuse(ReasonPowerEvidenceNotCurrent, "discovery has not read the system with the recorded UUID")
	}
	state := ops.ObservedPowerState(system.PowerState)
	if !state.Settled() {
		return refuse(ReasonPowerStateNotSettled, "discovery reads power state %q; it must be On or Off", system.PowerState)
	}
	evidence := settle.Recovery.Spec.Evidence
	if evidence.Data[SettlePowerState] != string(state) {
		return refuse(ReasonPowerEvidenceMismatch, "the recovery cites power state %q; discovery reads %s", evidence.Data[SettlePowerState], state)
	}
	if !evidence.ObservedAt.After(settle.Hold.Since.Time) || read.ObservedAt.Before(&evidence.ObservedAt) {
		return refuse(ReasonPowerEvidenceMismatch, "the recovery must cite a discovery read made after the operation ended")
	}
	return nil
}
