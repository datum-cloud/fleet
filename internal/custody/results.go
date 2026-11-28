package custody

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	verifyPoll   = 5 * time.Second
	maxConflicts = 8
)

var (
	// ErrStaleFence means the result's fence or operation does not match
	// the active operation. The site deletes its copy; custody records the
	// rejection for monitoring and never changes the holder for it.
	ErrStaleFence = errors.New("result does not match the active operation's fence")
	// ErrUnavailable means custody does not allow the holder change now.
	ErrUnavailable = errors.New("custody does not allow this change")
	// errContended means custody kept changing under every attempt.
	errContended = errors.New("custody changed under every attempt")
)

// SiteResult is what a site reports for one operation. It always carries the
// operation's fence.
type SiteResult struct {
	Site      string                 `json:"site"`
	Device    string                 `json:"device"`
	Operation ops.OperationReference `json:"operation"`
	Fence     int64                  `json:"fence"`
	// Phase is Dispatched, Verifying, Succeeded, Failed or OutcomeUnknown.
	Phase   ops.Phase `json:"phase"`
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	// Evidence is the kind's typed evidence as JSON.
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

// update reads custody, applies change and writes it with the
// resourceVersion just read. On a conflict it reads again and calls change
// again on the new custody: it never retries a stale decision.
func (c *Coordinator) update(ctx context.Context, name string, change func(*ops.DeviceCustody) error) error {
	for range maxConflicts {
		custody := &ops.DeviceCustody{}
		if err := c.Client.Get(ctx, client.ObjectKey{Namespace: c.config().Namespace, Name: name}, custody); err != nil {
			return err
		}
		if err := change(custody); err != nil {
			return err
		}
		if err := c.Client.Update(ctx, custody); err == nil {
			return nil
		} else if !apierrors.IsConflict(err) {
			return err
		}
	}
	return errContended
}

// errNoChange stops update without writing.
var errNoChange = errors.New("no change")

func ignoreNoChange(err error) error {
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

// OperationKey is the site's name for one admission of an operation: its
// lowercased kind, name and fence. A same-name operation admitted later has
// a higher fence and so another key. It is a valid site record and file
// name: a name too long for that is replaced by its digest.
func OperationKey(reference ops.OperationReference, fence int64) string {
	kind, suffix := strings.ToLower(reference.Kind), "."+strconv.FormatInt(fence, 10)
	if key := kind + "." + reference.Name + suffix; len(key) <= maxOperationKey {
		return key
	}
	sum := sha256.Sum256([]byte(reference.Name))
	return kind + "." + hex.EncodeToString(sum[:])[:32] + suffix
}

// maxOperationKey is the site store's limit on a record name.
const maxOperationKey = 128

// AcceptResult records a site result in custody. A result whose fence or
// operation does not match the active operation is rejected and recorded as
// a rejected fence; ErrStaleFence is returned either way, so the site
// deletes its copy.
func (c *Coordinator) AcceptResult(ctx context.Context, result SiteResult) error {
	// The evidence hook validates against the operation it is for. It is
	// read only when there is evidence to check; a result for an
	// unregistered kind is rejected as stale below.
	var operation ops.Operation
	if kind := c.Registry.Kind(result.Operation.Kind); kind != nil && len(result.Evidence) != 0 {
		found, _, err := c.get(ctx, result.Operation, result.Fence)
		if err != nil {
			return err
		}
		operation = found
	}
	var err error
	stale := false
	err = c.update(ctx, result.Device, func(custody *ops.DeviceCustody) error {
		stale = false
		spec := &custody.Spec
		active := spec.ActiveOperation
		kind := c.Registry.Kind(result.Operation.Kind)
		// A retry after a lost response to an accepted result is accepted
		// again without a write.
		if last := spec.LastResult; active == nil && last != nil && last.OperationReference == result.Operation && last.Fence == result.Fence && last.Phase == result.Phase {
			return errNoChange
		}
		if active == nil || active.OperationReference != result.Operation || active.Fence != result.Fence || kind == nil || !kind.Effectful {
			stale = true
			count := int64(1)
			if spec.RejectedResults != nil {
				count = spec.RejectedResults.Count + 1
			}
			spec.RejectedResults = &ops.RejectedResults{Count: count, Last: ops.RejectedResult{Kind: result.Operation.Kind, Name: result.Operation.Name, Fence: result.Fence, Site: result.Site, RejectedAt: metav1.NewTime(c.now())}}
			return nil
		}
		switch result.Phase {
		case ops.PhaseDispatched, ops.PhaseVerifying:
			if progressRank(result.Phase) <= progressRank(active.Phase) {
				return errNoChange
			}
			active.Phase = result.Phase
			return nil
		case ops.PhaseSucceeded, ops.PhaseFailed, ops.PhaseOutcomeUnknown:
		default:
			return fmt.Errorf("a site cannot report phase %q", result.Phase)
		}
		// A worker reports only reasons that are shared or that its kind
		// declares.
		reason, message := result.Reason, result.Message
		if reason != "" && !c.Registry.Declared(kind.GVK.Kind, reason, RaisedByWorker) {
			reason, message = ReasonUndeclared, fmt.Sprintf("the site reported %s, which %s does not declare", reason, kind.GVK.Kind)
		}
		result.Reason, result.Message = reason, message
		var evidence []byte
		if result.Phase == ops.PhaseSucceeded && kind.EvidenceRequired && len(result.Evidence) == 0 {
			c.holdUnknown(custody, *active, ReasonInvalidEvidence, "the kind requires evidence, and the site sent none", nil)
			return nil
		}
		if len(result.Evidence) != 0 {
			if kind.Evidence == nil {
				c.holdUnknown(custody, *active, ReasonInvalidEvidence, "the kind records no evidence, and the site sent some", nil)
				return nil
			}
			if operation == nil {
				c.holdUnknown(custody, *active, ReasonInvalidEvidence, "the operation is missing, so its evidence cannot be checked", nil)
				return nil
			}
			canonical, err := kind.Evidence(operation, result.Evidence)
			if err != nil {
				c.holdUnknown(custody, *active, ReasonInvalidEvidence, err.Error(), nil)
				return nil
			}
			evidence = canonical
		}
		switch result.Phase {
		case ops.PhaseOutcomeUnknown:
			reason := result.Reason
			if reason == "" {
				reason = ReasonSiteUnknown
			}
			c.holdUnknown(custody, *active, reason, result.Message, evidence)
		case ops.PhaseFailed:
			c.finishActive(custody, *active, ops.PhaseFailed, result.Reason, result.Message, evidence)
		case ops.PhaseSucceeded:
			if hold := spec.RecoveryHold; hold != nil && active.Settles != nil && *active.Settles == heldReference(*hold) {
				spec.RecoveryHold = nil
			}
			c.finishActive(custody, *active, ops.PhaseSucceeded, result.Reason, result.Message, evidence)
		}
		return nil
	})
	if err := ignoreNoChange(err); err != nil {
		return err
	}
	if stale {
		return ErrStaleFence
	}
	return nil
}

func progressRank(phase ops.Phase) int {
	return slices.Index([]ops.Phase{ops.PhaseAdmitted, ops.PhaseDispatched, ops.PhaseVerifying}, phase)
}

// Claim moves a free Device to the allocation with one conditional update.
// The allocator decides with this update, never with the advisory Available
// condition. A claim that already names the allocation succeeds, so a retry
// after a lost response does not claim a second server.
//
// deviceUID is the Device incarnation the allocation was made against, from
// its device-uid label. Each attempt of the conditional update reads the
// Device again and requires both it and custody's label to name that
// incarnation, so a Device recreated after selection is never claimed for an
// allocation made against the earlier one.
func (c *Coordinator) Claim(ctx context.Context, deviceName string, deviceUID types.UID, allocation string) error {
	if deviceUID == "" {
		return fmt.Errorf("%w: the allocation names no Device incarnation", ErrUnavailable)
	}
	return ignoreNoChange(c.update(ctx, deviceName, func(custody *ops.DeviceCustody) error {
		device, err := c.device(ctx, deviceName)
		if err != nil {
			return err
		}
		switch {
		case device == nil, device.UID != deviceUID:
			return fmt.Errorf("%w: the Device is not the incarnation the allocation names", ErrUnavailable)
		case !custody.DeletionTimestamp.IsZero(), custody.Labels[ops.LabelDeviceUID] != string(deviceUID):
			return fmt.Errorf("%w: custody is for another Device incarnation", ErrUnavailable)
		}
		spec := &custody.Spec
		if spec.Holder.Type == ops.HolderAllocation && spec.Holder.AllocationRef != nil && spec.Holder.AllocationRef.Name == allocation {
			return errNoChange
		}
		switch {
		case inventory.LifecycleOf(device) != inventory.DeviceActive:
			return fmt.Errorf("%w: the Device is not Active", ErrUnavailable)
		case spec.State != ops.CustodyVerified:
			return fmt.Errorf("%w: custody is Unverified", ErrUnavailable)
		case spec.Holder.Type != ops.HolderNone:
			return fmt.Errorf("%w: the holder is %s", ErrUnavailable, spec.Holder.Type)
		case spec.RecoveryHold != nil, spec.ActiveOperation != nil:
			return fmt.Errorf("%w: the Device is held or busy", ErrUnavailable)
		}
		spec.Holder = ops.Holder{Type: ops.HolderAllocation, AllocationRef: &ops.LocalReference{Name: allocation}}
		return nil
	}))
}

// BeginRelease moves an allocated Device to Release with every gate open. A
// hold moves with it, and an active operation keeps running under the same
// fence.
func (c *Coordinator) BeginRelease(ctx context.Context, deviceName, allocation string) error {
	return ignoreNoChange(c.update(ctx, deviceName, func(custody *ops.DeviceCustody) error {
		spec := &custody.Spec
		if spec.Holder.AllocationRef == nil || spec.Holder.AllocationRef.Name != allocation {
			return fmt.Errorf("%w: the allocation does not hold the Device", ErrUnavailable)
		}
		switch spec.Holder.Type {
		case ops.HolderRelease:
			return errNoChange
		case ops.HolderAllocation:
		default:
			return fmt.Errorf("%w: the holder is %s", ErrUnavailable, spec.Holder.Type)
		}
		spec.Holder = ops.Holder{Type: ops.HolderRelease, AllocationRef: spec.Holder.AllocationRef, OpenGates: slices.Clone(ops.ReleaseGates)}
		return nil
	}))
}

// CloseGate removes one release gate after the release controller reports
// its evidence. A gate the coordinator does not know stays open. The Device
// returns to None on a later reconcile, once no gate, hold or operation
// remains.
func (c *Coordinator) CloseGate(ctx context.Context, deviceName, allocation string, gate ops.ReleaseGate) error {
	if !slices.Contains(ops.ReleaseGates, gate) {
		return fmt.Errorf("%w: unknown release gate %q stays open", ErrUnavailable, gate)
	}
	return ignoreNoChange(c.update(ctx, deviceName, func(custody *ops.DeviceCustody) error {
		spec := &custody.Spec
		if spec.Holder.Type != ops.HolderRelease || spec.Holder.AllocationRef == nil || spec.Holder.AllocationRef.Name != allocation {
			return fmt.Errorf("%w: the allocation is not releasing the Device", ErrUnavailable)
		}
		if !slices.Contains(spec.Holder.OpenGates, gate) {
			return errNoChange
		}
		spec.Holder.OpenGates = slices.DeleteFunc(slices.Clone(spec.Holder.OpenGates), func(open ops.ReleaseGate) bool { return open == gate })
		return nil
	}))
}
