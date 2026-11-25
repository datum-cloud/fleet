package custody

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Per-kind deadlines. The site worker stops at the deadline exactly; the
// coordinator declares OutcomeUnknown only after the deadline plus
// Config.SkewMargin.
const (
	// InstallationDeadline matches the Redfish executor's install bound.
	InstallationDeadline = 40 * time.Minute
	// WipeDeadline matches the Redfish executor's cleanup bound.
	WipeDeadline = 40 * time.Minute
	// RecoveryDeadline bounds how long a recovery waits for the sites to
	// confirm its fence.
	RecoveryDeadline = 10 * time.Minute
)

// Kind-specific refusal reasons.
const (
	ReasonBootstrapUnsupported = "BootstrapUnsupported"
	ReasonBootInterfaceUnknown = "BootInterfaceUnknown"
	ReasonHoldMismatch         = "HoldMismatch"
	ReasonNotSettleable        = "NotSettleable"
	ReasonNotRetryable         = "NotRetryable"
	ReasonRetryAuthorized      = "RetryAlreadyAuthorized"
	ReasonDisposalRequired     = "DisposalRequired"
	ReasonInvalidResolve       = "InvalidResolve"
	// Reasons every site's install and wipe workers raise.
	ReasonTargetUnsupported = "TargetUnsupported"
	ReasonTargetDisks       = "TargetDisksUnknown"
	ReasonInputsUnreadable  = "InputsUnreadable"
)

// workerReasons are the reasons every site's install and wipe workers
// raise before sending anything.
var workerReasons = []Reason{{Name: ReasonTargetUnsupported, RaisedBy: RaisedByWorker}, {Name: ReasonTargetDisks, RaisedBy: RaisedByWorker}, {Name: ReasonInputsUnreadable, RaisedBy: RaisedByWorker}}

const evidenceLimit = 4096

var gv = ops.GroupVersion

// InstallationName is the only name a DeviceInstallation may have: its
// allocation and the Installation it fulfills.
func InstallationName(allocation, requestUID string) string {
	sum := sha256.Sum256([]byte(requestUID))
	return allocation + "-install-" + hex.EncodeToString(sum[:])[:12]
}

// WipeName is the only name a DeviceWipe may have: its allocation and step.
func WipeName(allocation string, step int32) string {
	return allocation + "-wipe-" + strconv.Itoa(int(step))
}

func allocationName(operation ops.Operation) string {
	if reference := operation.Envelope().Authority.AllocationRef; reference != nil {
		return reference.Name
	}
	return ""
}

func validateSteps(steps []ops.StepEvidence) error {
	if len(steps) > 8 {
		return errors.New("evidence records at most 8 steps")
	}
	for _, step := range steps {
		if step.Name == "" || len(step.Name) > 63 {
			return errors.New("a step name is 1 to 63 characters")
		}
	}
	return nil
}

// DeviceInstallationKind is the registry entry for DeviceInstallation.
func DeviceInstallationKind() Kind {
	return Kind{
		GVK:     gv.WithKind("DeviceInstallation"),
		New:     func() ops.Operation { return &ops.DeviceInstallation{} },
		NewList: func() client.ObjectList { return &ops.DeviceInstallationList{} },
		Items: func(list client.ObjectList) []ops.Operation {
			return items(list.(*ops.DeviceInstallationList).Items)
		},
		Digest: func(operation ops.Operation) (string, error) {
			return SpecDigest(operation.(*ops.DeviceInstallation).Spec)
		},
		Naming: DeterministicName,
		Name: func(operation ops.Operation) string {
			return InstallationName(allocationName(operation), operation.(*ops.DeviceInstallation).Spec.RequestUID)
		},
		Deadline:      InstallationDeadline,
		Effectful:     true,
		BootInterface: func(operation ops.Operation) string { return operation.(*ops.DeviceInstallation).Spec.BootInterface },
		Check: func(_ context.Context, admission *Admission) *Refusal {
			installation := admission.Operation.(*ops.DeviceInstallation)
			// Bootstrap stays refused until the sealed store exists.
			if installation.Spec.Bootstrap != nil {
				return refuse(ReasonBootstrapUnsupported, "bootstrap payloads need the sealed store, which is not implemented")
			}
			for _, iface := range admission.Device.Spec.Interfaces {
				if iface.Name == installation.Spec.BootInterface && iface.MACAddress != "" {
					return nil
				}
			}
			return refuse(ReasonBootInterfaceUnknown, "the Device records no interface %q with a MAC address", installation.Spec.BootInterface)
		},
		Settles: []SettleRule{{Held: "DeviceInstallation"}},
		Reasons: append([]Reason{{Name: ReasonBootstrapUnsupported, RaisedBy: RaisedByBoth}, {Name: ReasonBootInterfaceUnknown, RaisedBy: RaisedByCoordinator}}, workerReasons...),
		Evidence: StrictEvidence(evidenceLimit, func(_ ops.Operation, evidence *ops.InstallationEvidence) error {
			return validateSteps(evidence.Steps)
		}),
	}
}

// DeviceWipeKind is the registry entry for DeviceWipe.
func DeviceWipeKind() Kind {
	return Kind{
		GVK:     gv.WithKind("DeviceWipe"),
		New:     func() ops.Operation { return &ops.DeviceWipe{} },
		NewList: func() client.ObjectList { return &ops.DeviceWipeList{} },
		Items: func(list client.ObjectList) []ops.Operation {
			return items(list.(*ops.DeviceWipeList).Items)
		},
		Digest: func(operation ops.Operation) (string, error) {
			return SpecDigest(operation.(*ops.DeviceWipe).Spec)
		},
		Naming: DeterministicName,
		Name: func(operation ops.Operation) string {
			return WipeName(allocationName(operation), operation.(*ops.DeviceWipe).Spec.Step)
		},
		Deadline:  WipeDeadline,
		Effectful: true,
		// A wipe after release settles an installation hold. A held wipe is
		// settled by a later wipe only after a DeviceRecovery with Retry.
		Settles:   []SettleRule{{Held: "DeviceInstallation"}, {Held: "DeviceWipe", RequiresRetry: true}},
		Retryable: true,
		Reasons:   workerReasons,
		Evidence: StrictEvidence(evidenceLimit, func(_ ops.Operation, evidence *ops.WipeEvidence) error {
			if len(evidence.WipedDisks) > 16 {
				return errors.New("evidence records at most 16 disks")
			}
			return validateSteps(evidence.Steps)
		}),
	}
}

// DeviceRecoveryKind is the registry entry for DeviceRecovery. It sends
// nothing to the server. It is admitted like any operation, raising the
// fence, and applied only after every serving site reports that fence, so
// the held worker has stopped first.
func DeviceRecoveryKind() Kind {
	return Kind{
		GVK:     gv.WithKind("DeviceRecovery"),
		New:     func() ops.Operation { return &ops.DeviceRecovery{} },
		NewList: func() client.ObjectList { return &ops.DeviceRecoveryList{} },
		Items: func(list client.ObjectList) []ops.Operation {
			return items(list.(*ops.DeviceRecoveryList).Items)
		},
		Digest: func(operation ops.Operation) (string, error) {
			return SpecDigest(operation.(*ops.DeviceRecovery).Spec)
		},
		Naming:   GeneratedName,
		Deadline: RecoveryDeadline,
		Variant:  func(operation ops.Operation) string { return string(operation.(*ops.DeviceRecovery).Spec.Decision) },
		Reasons: []Reason{{Name: ReasonHoldMismatch, RaisedBy: RaisedByCoordinator}, {Name: ReasonNotSettleable, RaisedBy: RaisedByCoordinator}, {Name: ReasonNotRetryable, RaisedBy: RaisedByCoordinator},
			{Name: ReasonRetryAuthorized, RaisedBy: RaisedByCoordinator}, {Name: ReasonDisposalRequired, RaisedBy: RaisedByCoordinator}, {Name: ReasonInvalidResolve, RaisedBy: RaisedByCoordinator}},
		// Retire and Resolve must work on a server whose BMC is
		// unreachable. Settle and Retry still need current discovery.
		SkipsDiscovery: func(operation ops.Operation) bool {
			decision := operation.(*ops.DeviceRecovery).Spec.Decision
			return decision == ops.DecisionRetire || decision == ops.DecisionResolve
		},
		Check: func(ctx context.Context, admission *Admission) *Refusal {
			return checkRecovery(ctx, admission.Operation.(*ops.DeviceRecovery), admission.Custody, admission.Registry, admission)
		},
		Apply: applyRecovery,
		Clears: func(operation ops.Operation) *ops.HeldOperationReference {
			spec := operation.(*ops.DeviceRecovery).Spec
			if spec.Decision == ops.DecisionSettle || spec.Decision == ops.DecisionRetire {
				return spec.Hold
			}
			return nil
		},
		Retires: func(operation ops.Operation) bool {
			spec := operation.(*ops.DeviceRecovery).Spec
			return spec.Decision == ops.DecisionRetire || (spec.Decision == ops.DecisionResolve && spec.Resolve != nil && spec.Resolve.Holder == ops.HolderRetired)
		},
	}
}

func checkRecovery(ctx context.Context, recovery *ops.DeviceRecovery, custody *ops.DeviceCustody, registry *Registry, admission *Admission) *Refusal {
	spec := recovery.Spec
	if spec.Decision == ops.DecisionResolve {
		resolve := spec.Resolve
		switch {
		case resolve == nil:
			return refuse(ReasonInvalidResolve, "Resolve needs the holder to record")
		case resolve.Holder == ops.HolderAllocation || resolve.Holder == ops.HolderRelease:
			if resolve.AllocationRef == nil || resolve.AllocationRef.Name == "" {
				return refuse(ReasonInvalidResolve, "an %s holder needs its allocation", resolve.Holder)
			}
		case resolve.Holder == ops.HolderNone || resolve.Holder == ops.HolderRetired:
			if resolve.AllocationRef != nil {
				return refuse(ReasonInvalidResolve, "a %s holder has no allocation", resolve.Holder)
			}
		default:
			return refuse(ReasonInvalidResolve, "Resolve cannot record holder %s", resolve.Holder)
		}
		return resolveMatchesAllocations(ctx, admission, resolve)
	}
	hold := custody.Spec.RecoveryHold
	if hold == nil || spec.Hold == nil || spec.Hold.Kind != hold.Kind || spec.Hold.Name != hold.Name || spec.Hold.Fence != hold.Fence {
		return refuse(ReasonHoldMismatch, "the recovery does not name the current hold")
	}
	switch spec.Decision {
	case ops.DecisionSettle:
		held := registry.Kind(hold.Kind)
		if held == nil || held.RecoverySettle == nil {
			return refuse(ReasonNotSettleable, "a %s hold is settled by its settling operations, not by a recovery", hold.Kind)
		}
		return held.RecoverySettle(ctx, &RecoverySettleContext{Hold: *hold, Recovery: recovery, Device: admission.Device, Custody: custody, Now: admission.Now})
	case ops.DecisionRetry:
		if hold.RetryAuthorizedBy != nil {
			return refuse(ReasonRetryAuthorized, "a retry is already authorized for this hold")
		}
		if held := registry.Kind(hold.Kind); held == nil || !held.Retryable {
			return refuse(ReasonNotRetryable, "a DeviceRecovery with Retry does not apply to a %s hold", hold.Kind)
		}
		for _, kind := range registry.Kinds() {
			for _, rule := range kind.Settles {
				if rule.Held == hold.Kind && rule.RequiresRetry {
					return nil
				}
			}
		}
		return refuse(ReasonNotRetryable, "no kind settles a %s hold after a retry", hold.Kind)
	case ops.DecisionRetire:
		if custody.Spec.Holder.Type == ops.HolderRelease && (spec.DiskDisposal == nil || spec.DiskDisposal.Reference == "") {
			return refuse(ReasonDisposalRequired, "retiring from Release records how the disks were disposed of")
		}
		return nil
	}
	return refuse(ReasonCreatorNotPermitted, "unknown decision %q", spec.Decision)
}

// resolveMatchesAllocations requires the holder a Resolve records to agree
// with the Device's live MachineAllocations: none for None or Retired, and
// exactly the one named, Releasing for Release, for Allocation and Release.
func resolveMatchesAllocations(ctx context.Context, admission *Admission, resolve *ops.ResolveInput) *Refusal {
	if admission.Reader == nil {
		return refuse(ReasonInvalidResolve, "the live allocations could not be read")
	}
	allocations, err := LiveAllocations(ctx, admission.Reader, admission.Device)
	if err != nil {
		return refuse(ReasonInvalidResolve, "the live allocations could not be read")
	}
	switch {
	case len(allocations) > 1:
		return refuse(ReasonInvalidResolve, "%d live allocations name the Device; end all but one first", len(allocations))
	case len(allocations) == 0:
		if resolve.Holder != ops.HolderNone && resolve.Holder != ops.HolderRetired {
			return refuse(ReasonInvalidResolve, "no live allocation names the Device, so the holder is None or Retired")
		}
	default:
		allocation := allocations[0]
		want := ops.HolderAllocation
		if allocation.Status.Phase == "Releasing" {
			want = ops.HolderRelease
		}
		if resolve.Holder != want || resolve.AllocationRef == nil || resolve.AllocationRef.Name != allocation.Name {
			return refuse(ReasonInvalidResolve, "allocation %s names the Device, so the holder is %s with that allocation", allocation.Name, want)
		}
	}
	return nil
}

// applyRecovery applies a confirmed recovery. It re-checks against the
// custody read now, because custody may have changed since admission.
func applyRecovery(ctx context.Context, apply *ApplyContext) (*ops.DeviceCustodySpec, *Refusal) {
	recovery := apply.Operation.(*ops.DeviceRecovery)
	custody := apply.Custody
	if refusal := checkRecovery(ctx, recovery, custody, apply.Registry, &Admission{Operation: recovery, Custody: custody, Device: apply.Device, Registry: apply.Registry, Reader: apply.Reader, Now: apply.Now}); refusal != nil {
		return nil, refusal
	}
	spec := custody.Spec
	reference := ops.OperationReference{Kind: "DeviceRecovery", Name: recovery.Name, UID: string(recovery.UID)}
	switch recovery.Spec.Decision {
	case ops.DecisionSettle:
		spec.RecoveryHold = nil
		if spec.Holder.Type == ops.HolderService {
			spec.Holder = ops.Holder{Type: ops.HolderNone}
		}
	case ops.DecisionRetry:
		hold := *spec.RecoveryHold
		hold.RetryAuthorizedBy = &reference
		hold.Retries++
		spec.RecoveryHold = &hold
	case ops.DecisionRetire:
		spec.RecoveryHold = nil
		spec.Holder = ops.Holder{Type: ops.HolderRetired}
	case ops.DecisionResolve:
		resolve := recovery.Spec.Resolve
		spec.State = ops.CustodyVerified
		spec.Conflicts = nil
		spec.DeviceRef.UID = string(apply.Device.UID)
		spec.Fence = max(spec.Fence, apply.HighestFence)
		spec.Holder = ops.Holder{Type: resolve.Holder}
		if resolve.AllocationRef != nil {
			spec.Holder.AllocationRef = &ops.LocalReference{Name: resolve.AllocationRef.Name}
		}
		if resolve.Holder == ops.HolderRelease {
			spec.Holder.OpenGates = slices.Clone(ops.ReleaseGates)
		}
	}
	return &spec, nil
}

// DefaultRegistry registers the built-in kinds and the operations table rows
// for them.
func DefaultRegistry() (*Registry, error) {
	registry := NewRegistry()
	for _, kind := range []Kind{DeviceInstallationKind(), DeviceWipeKind(), DeviceRecoveryKind()} {
		if err := registry.Register(kind); err != nil {
			return nil, err
		}
	}
	for _, rule := range DefaultRules() {
		if err := registry.AddRule(rule); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// DefaultRules is the operations table of #174 for the built-in kinds.
func DefaultRules() []Rule {
	return []Rule{
		{Kind: "DeviceInstallation", Creator: CreatorMetalProvider, Authority: AuthorityAllocation, Holders: []ops.HolderType{ops.HolderAllocation}, Queue: true},
		{Kind: "DeviceWipe", Creator: CreatorReleaseController, Authority: AuthorityAllocation, Holders: []ops.HolderType{ops.HolderRelease}, Queue: true},
		{Kind: "DeviceRecovery", Variant: string(ops.DecisionSettle), Creator: CreatorStaff, Authority: AuthorityService, Hold: HoldRequired},
		{Kind: "DeviceRecovery", Variant: string(ops.DecisionRetry), Creator: CreatorStaff, Authority: AuthorityService, Hold: HoldRequired},
		{Kind: "DeviceRecovery", Variant: string(ops.DecisionRetire), Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderService, ops.HolderRelease}, Hold: HoldRequired},
		{Kind: "DeviceRecovery", Variant: string(ops.DecisionResolve), Creator: CreatorStaff, Authority: AuthorityService, Custody: ops.CustodyUnverified, Hold: HoldIgnored},
	}
}

// items adapts a typed item slice to operations.
func items[T any, P interface {
	*T
	ops.Operation
}](list []T) []ops.Operation {
	out := make([]ops.Operation, 0, len(list))
	for index := range list {
		out = append(out, P(&list[index]))
	}
	return out
}
