package custody

import (
	"cmp"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Conflicts that keep custody Unverified until an operator resolves them with
// a DeviceRecovery Resolve.
const (
	ConflictDeviceMissing       = "DeviceMissing"
	ConflictDeviceUID           = "DeviceUIDMismatch"
	ConflictHolderSources       = "HolderSourcesDisagree"
	ConflictMultipleAllocations = "MultipleAllocations"
	ConflictMultipleUnfinished  = "MultipleUnfinishedOperations"
)

var retrySuffix = regexp.MustCompile(`^(.+)-retry-([1-9][0-9]{0,8})$`)

// retryBase is the name of the operation a retry was named from.
func retryBase(name string) string {
	if match := retrySuffix.FindStringSubmatch(name); match != nil {
		return match[1]
	}
	return name
}

// ReasonRebuilt is the hold reason for an operation found unfinished when
// custody was rebuilt.
const ReasonRebuilt = "Rebuilt"

// rebuild creates missing custody as Unverified from the Device's inventory
// lifecycle and Retire recoveries, its live MachineAllocations, and its
// operations that have not ended. Site fences are added when it is verified.
func (c *Coordinator) rebuild(ctx context.Context, device *inventory.Device) (Outcome, error) {
	spec, err := c.rebuiltSpec(ctx, device)
	if err != nil {
		return Outcome{}, err
	}
	custody := &ops.DeviceCustody{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: device.Namespace, Finalizers: []string{ops.CustodyFinalizer}}, Spec: spec}
	setDeviceUID(custody, device)
	if err := c.Client.Create(ctx, custody); err != nil && !apierrors.IsAlreadyExists(err) {
		return Outcome{}, err
	}
	return Outcome{Changed: true}, nil
}

func (c *Coordinator) rebuiltSpec(ctx context.Context, device *inventory.Device) (ops.DeviceCustodySpec, error) {
	spec := ops.DeviceCustodySpec{DeviceRef: ops.DeviceReference{Name: device.Name}, State: ops.CustodyUnverified, Holder: ops.Holder{Type: ops.HolderNone}}
	holders := map[ops.HolderType]bool{}
	if inventory.LifecycleOf(device) == inventory.DeviceRetired {
		holders[ops.HolderRetired] = true
	}
	allocations, err := LiveAllocations(ctx, c.Client, device)
	if err != nil {
		return spec, err
	}
	if len(allocations) > 1 {
		spec.Conflicts = append(spec.Conflicts, ConflictMultipleAllocations)
	}
	for _, allocation := range allocations {
		if allocation.Status.Phase == "Releasing" {
			holders[ops.HolderRelease] = true
			spec.Holder = ops.Holder{Type: ops.HolderRelease, AllocationRef: &ops.LocalReference{Name: allocation.Name}, OpenGates: slices.Clone(ops.ReleaseGates)}
		} else {
			holders[ops.HolderAllocation] = true
			spec.Holder = ops.Holder{Type: ops.HolderAllocation, AllocationRef: &ops.LocalReference{Name: allocation.Name}}
		}
	}
	type found struct {
		operation ops.Operation
		kind      *Kind
	}
	var all []found
	for _, kind := range c.Registry.Kinds() {
		list := kind.NewList()
		if err := c.Client.List(ctx, list, client.InNamespace(device.Namespace)); err != nil {
			return spec, err
		}
		for _, operation := range kind.Items(list) {
			envelope := operation.Envelope()
			if envelope.DeviceRef.Name != device.Name || operation.GetLabels()[ops.LabelDeviceUID] != string(device.UID) {
				continue
			}
			status := operation.OperationStatus()
			spec.Fence = max(spec.Fence, status.Fence, admissionFence(operation))
			if kind.Retires != nil && status.Phase == ops.PhaseSucceeded && kind.Retires(operation) {
				holders[ops.HolderRetired] = true
			}
			all = append(all, found{operation: operation, kind: kind})
		}
	}
	// settled reports whether a later success settled an unknown outcome:
	// an operation whose kind settles the held kind, or a recovery that
	// cleared that exact hold.
	settled := func(held found) bool {
		fence := admissionFence(held.operation)
		for _, other := range all {
			// A later retry of the same operation continues its hold.
			if other.kind == held.kind && retryBase(other.operation.GetName()) == retryBase(held.operation.GetName()) && other.operation.GetName() != held.operation.GetName() && admissionFence(other.operation) > fence {
				return true
			}
			status := other.operation.OperationStatus()
			if status.Phase != ops.PhaseSucceeded {
				continue
			}
			if other.kind.Clears != nil {
				if cleared := other.kind.Clears(other.operation); cleared != nil && cleared.Kind == held.kind.GVK.Kind && cleared.Name == held.operation.GetName() && cleared.Fence == fence {
					return true
				}
			}
			if admissionFence(other.operation) > fence && slices.ContainsFunc(other.kind.Settles, func(rule SettleRule) bool { return rule.Held == held.kind.GVK.Kind }) {
				return true
			}
		}
		return false
	}
	// An operation records its admission before custody names it, so one
	// without an admission record was never admitted. Every admitted
	// operation that has not ended may already have executed, even if its
	// status says Pending, because custody is written before the rest of its
	// status. An unknown outcome nothing later settled is held too.
	var held []found
	for _, candidate := range all {
		phase := candidate.operation.OperationStatus().Phase
		if candidate.operation.OperationStatus().Admission == nil || phase.Ended() && (phase != ops.PhaseOutcomeUnknown || settled(candidate)) {
			continue
		}
		held = append(held, candidate)
		envelope := candidate.operation.Envelope()
		switch {
		case envelope.Authority.ServiceRequest != nil && candidate.kind.Effectful:
			holders[ops.HolderService] = true
		case envelope.Authority.AllocationRef != nil:
			if spec.Holder.AllocationRef == nil || spec.Holder.AllocationRef.Name != envelope.Authority.AllocationRef.Name {
				spec.Conflicts = appendOnce(spec.Conflicts, ConflictHolderSources)
			}
		}
	}
	if len(held) > 0 {
		// With several, the newest is held and a Resolve must decide, so the
		// hold is never dropped.
		slices.SortStableFunc(held, func(a, b found) int {
			return cmp.Compare(admissionFence(b.operation), admissionFence(a.operation))
		})
		newest := held[0]
		status := newest.operation.OperationStatus()
		spec.RecoveryHold = &ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: newest.kind.GVK.Kind, Name: newest.operation.GetName()}, Fence: admissionFence(newest.operation), Since: metav1.NewTime(c.now()), Reason: ReasonRebuilt, Deadline: status.Deadline, Snapshot: status.Snapshot}
		// A held retry keeps its lineage: its name is the original
		// operation's name, "-retry-" and the Retry count.
		if match := retrySuffix.FindStringSubmatch(newest.operation.GetName()); match != nil && slices.ContainsFunc(all, func(other found) bool {
			return other.kind == newest.kind && other.operation.GetName() == match[1]
		}) {
			if count, err := strconv.Atoi(match[2]); err == nil {
				spec.RecoveryHold.RetryName, spec.RecoveryHold.Retries = match[1], int32(count)
			}
		}
		if len(held) > 1 {
			spec.Conflicts = appendOnce(spec.Conflicts, ConflictMultipleUnfinished)
		}
	}
	if len(holders) > 1 {
		spec.Conflicts = appendOnce(spec.Conflicts, ConflictHolderSources)
	}
	switch {
	case holders[ops.HolderRetired]:
		spec.Holder = ops.Holder{Type: ops.HolderRetired}
	case holders[ops.HolderService]:
		spec.Holder = ops.Holder{Type: ops.HolderService}
	}
	return spec, nil
}

func appendOnce(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

// LiveAllocations returns the MachineAllocations in the Device's namespace
// that are not Released and name this Device incarnation.
func LiveAllocations(ctx context.Context, reader client.Reader, device *inventory.Device) ([]metal.MachineAllocation, error) {
	var allocations metal.MachineAllocationList
	if err := reader.List(ctx, &allocations, client.InNamespace(device.Namespace)); err != nil {
		return nil, err
	}
	var out []metal.MachineAllocation
	for _, allocation := range allocations.Items {
		reference := allocation.Spec.DeviceRef
		if allocation.Status.Phase != metal.AllocationReleased && reference.Name == device.Name && allocation.Labels[metal.LabelDeviceUID] == string(device.UID) {
			out = append(out, allocation)
		}
	}
	return out, nil
}

// verify marks Unverified custody without conflicts Verified once every site
// serving the Device reports its stored fence. The new fence is above every
// fence already used.
func (c *Coordinator) verify(ctx context.Context, custody *ops.DeviceCustody, device *inventory.Device) (Outcome, error) {
	highest := custody.Spec.Fence
	sites := c.Sites.Serving(device)
	// A Device no configured site serves has nothing to fence it, so it
	// stays Unverified and unallocatable until a site is configured.
	if len(sites) == 0 {
		return Outcome{RequeueAfter: verifyPoll}, nil
	}
	for _, site := range sites {
		report, reported, err := c.Sites.Fence(ctx, site, device)
		if err != nil {
			return Outcome{}, err
		}
		if !reported {
			return Outcome{RequeueAfter: verifyPoll}, nil
		}
		highest = max(highest, report.Seen, report.Stopped)
	}
	custody.Spec.State = ops.CustodyVerified
	custody.Spec.Fence = highest + 1
	return c.writeCustody(ctx, custody)
}

func rawEvidence(value any) (*runtime.RawExtension, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return rawExtension(data), nil
}

func rawExtension(data []byte) *runtime.RawExtension {
	return &runtime.RawExtension{Raw: append([]byte(nil), data...)}
}
