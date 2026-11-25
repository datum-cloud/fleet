package custody

import (
	"context"
	"encoding/json"
	"slices"

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
	if err := c.Client.Create(ctx, custody); err != nil && !apierrors.IsAlreadyExists(err) {
		return Outcome{}, err
	}
	return Outcome{Changed: true}, nil
}

func (c *Coordinator) rebuiltSpec(ctx context.Context, device *inventory.Device) (ops.DeviceCustodySpec, error) {
	spec := ops.DeviceCustodySpec{DeviceRef: ops.DeviceReference{Name: device.Name, UID: string(device.UID)}, State: ops.CustodyUnverified, Holder: ops.Holder{Type: ops.HolderNone}}
	holders := map[ops.HolderType]bool{}
	if inventory.LifecycleOf(device) == inventory.DeviceRetired {
		holders[ops.HolderRetired] = true
	}
	allocations, err := c.liveAllocations(ctx, device)
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
	var unfinished []ops.Operation
	var unfinishedKinds []string
	for _, kind := range c.Registry.Kinds() {
		list := kind.NewList()
		if err := c.Client.List(ctx, list, client.InNamespace(device.Namespace)); err != nil {
			return spec, err
		}
		for _, operation := range kind.Items(list) {
			envelope := operation.Envelope()
			if envelope.DeviceRef.Name != device.Name || envelope.DeviceRef.UID != string(device.UID) {
				continue
			}
			status := operation.OperationStatus()
			spec.Fence = max(spec.Fence, status.Fence)
			if kind.Retires != nil && status.Phase == ops.PhaseSucceeded && kind.Retires(operation) {
				holders[ops.HolderRetired] = true
			}
			if status.Phase.Ended() {
				continue
			}
			unfinished = append(unfinished, operation)
			unfinishedKinds = append(unfinishedKinds, kind.GVK.Kind)
			switch {
			case envelope.Authority.ServiceRequest != nil && kind.Effectful:
				holders[ops.HolderService] = true
			case envelope.Authority.AllocationRef != nil:
				if spec.Holder.AllocationRef == nil || spec.Holder.AllocationRef.Name != envelope.Authority.AllocationRef.Name {
					spec.Conflicts = appendOnce(spec.Conflicts, ConflictHolderSources)
				}
			}
		}
	}
	// Every unfinished operation, even one whose status says Pending, may
	// already have been admitted and executed, so it is held.
	switch len(unfinished) {
	case 0:
	case 1:
		operation := unfinished[0]
		spec.RecoveryHold = &ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: unfinishedKinds[0], Name: operation.GetName(), UID: string(operation.GetUID())}, Fence: operation.OperationStatus().Fence, Since: metav1.NewTime(c.now()), Reason: ReasonRebuilt}
	default:
		spec.Conflicts = appendOnce(spec.Conflicts, ConflictMultipleUnfinished)
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

// liveAllocations returns the MachineAllocations that are not Released and
// whose MachineCapacity is backed by the Device.
func (c *Coordinator) liveAllocations(ctx context.Context, device *inventory.Device) ([]metal.MachineAllocation, error) {
	var allocations metal.MachineAllocationList
	if err := c.Client.List(ctx, &allocations); err != nil {
		return nil, err
	}
	var out []metal.MachineAllocation
	for _, allocation := range allocations.Items {
		if allocation.Status.Phase == "Released" {
			continue
		}
		capacity := &metal.MachineCapacity{}
		reference := allocation.Spec.MachineCapacityRef
		if err := c.Client.Get(ctx, client.ObjectKey{Namespace: reference.Namespace, Name: reference.Name}, capacity); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if capacity.Namespace == device.Namespace && capacity.Spec.DeviceRef.Name == device.Name {
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
		stored, reported, err := c.Sites.Fence(ctx, site, device)
		if err != nil {
			return Outcome{}, err
		}
		if !reported {
			return Outcome{RequeueAfter: verifyPoll}, nil
		}
		highest = max(highest, stored)
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
