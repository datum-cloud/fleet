package custody

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Sites reports what each site serving a Device has stored. The site
// framework's Report implements it for one site; tests supply a fake.
type Sites interface {
	// Serving returns the sites explicitly configured to serve the Device.
	// The coordinator never infers a site from placement.
	Serving(device *inventory.Device) []string
	// Fence returns the site's fence report for the Device, and false if
	// the site has not reported one.
	Fence(ctx context.Context, site string, device *inventory.Device) (SiteFence, bool, error)
}

// SiteFence is one site's fence report for one Device.
type SiteFence struct {
	// Seen is the highest fence the site has stored. Admission and rebuild
	// set the new fence above it.
	Seen int64
	// Stopped is the highest fence for which the site confirmed that every
	// older worker stopped. A DeviceRecovery applies only once it reaches
	// the recovery's fence.
	Stopped int64
}

// Config holds the coordinator's tunable values. Zero values take the
// documented defaults.
type Config struct {
	// Namespace is the Device namespace in the provider project.
	Namespace  string
	Principals Principals
	// StaffExpiry is how long a staff operation may wait for admission.
	StaffExpiry time.Duration
	// SkewMargin is added to a deadline before the coordinator declares
	// OutcomeUnknown, so a site whose clock runs behind still stops first.
	SkewMargin time.Duration
	// Retention is how long finished operations are kept.
	Retention time.Duration
	// DiscoveryInterval is how often BMC discovery reads each BMC. Kind
	// checks receive it.
	DiscoveryInterval time.Duration
	// DiscoveryMaxAge is how old the last complete discovery may be for
	// identity to count as current. It defaults to twice DiscoveryInterval.
	DiscoveryMaxAge time.Duration
}

const (
	DefaultStaffExpiry = 5 * time.Minute
	DefaultSkewMargin  = 2 * time.Minute
	DefaultRetention   = 30 * 24 * time.Hour
	// DefaultDiscoveryInterval matches BMC discovery's default interval.
	DefaultDiscoveryInterval = time.Hour
	DefaultDiscoveryMaxAge   = 2 * DefaultDiscoveryInterval
)

func (c Config) withDefaults() Config {
	if c.Namespace == "" {
		c.Namespace = "default"
	}
	if c.StaffExpiry == 0 {
		c.StaffExpiry = DefaultStaffExpiry
	}
	if c.SkewMargin == 0 {
		c.SkewMargin = DefaultSkewMargin
	}
	if c.Retention == 0 {
		c.Retention = DefaultRetention
	}
	if c.DiscoveryInterval == 0 {
		c.DiscoveryInterval = DefaultDiscoveryInterval
	}
	if c.DiscoveryMaxAge == 0 {
		c.DiscoveryMaxAge = 2 * c.DiscoveryInterval
	}
	return c
}

// Coordinator admits operations by updating custody. It is the only writer
// of custody and of operation status.
type Coordinator struct {
	// Client must read through to the API server: decisions are made on the
	// resourceVersion just read, never on a cache.
	Client   client.Client
	Registry *Registry
	Sites    Sites
	Config   Config
	// Now is the clock. Tests inject one.
	Now func() time.Time
}

func (c *Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Coordinator) config() Config { return c.Config.withDefaults() }

// Outcome reports what one reconcile did.
type Outcome struct {
	// Changed is set when the reconcile wrote anything. Call again.
	Changed bool
	// RequeueAfter is when a time-based decision next falls due.
	RequeueAfter time.Duration
}

// Reconcile makes at most one custody write for the named Device and copies
// operation status from custody. Callers repeat it while Changed is set.
func (c *Coordinator) Reconcile(ctx context.Context, name string) (Outcome, error) {
	cfg := c.config()
	device, err := c.device(ctx, name)
	if err != nil {
		return Outcome{}, err
	}
	custody := &ops.DeviceCustody{}
	if err := c.Client.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: name}, custody); apierrors.IsNotFound(err) {
		if device == nil {
			return Outcome{}, nil
		}
		return c.rebuild(ctx, device)
	} else if err != nil {
		return Outcome{}, err
	}
	// Deleting custody only removes the finalizer once the holder is
	// Retired. Until then custody keeps working, so deadlines still pass.
	if !custody.DeletionTimestamp.IsZero() && custody.Spec.Holder.Type == ops.HolderRetired {
		if slices.Contains(custody.Finalizers, ops.CustodyFinalizer) {
			custody.Finalizers = slices.DeleteFunc(custody.Finalizers, func(f string) bool { return f == ops.CustodyFinalizer })
			return c.writeCustody(ctx, custody)
		}
		return Outcome{}, nil
	}
	if device == nil {
		if custody.Spec.Holder.Type == ops.HolderRetired {
			if err := c.Client.Delete(ctx, custody, client.Preconditions{UID: &custody.UID, ResourceVersion: &custody.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				return Outcome{}, err
			}
			return Outcome{Changed: true}, nil
		}
		return c.markUnverified(ctx, custody, ConflictDeviceMissing)
	}
	if changed, err := c.copyStatus(ctx, custody); err != nil || changed {
		return Outcome{Changed: changed}, err
	}
	if custody.Spec.DeviceRef.UID != string(device.UID) && !slices.Contains(custody.Spec.Conflicts, ConflictDeviceUID) {
		return c.markUnverified(ctx, custody, ConflictDeviceUID)
	}
	var requeue time.Duration
	steps := []func() (Outcome, error){
		func() (Outcome, error) {
			if custody.Spec.State != ops.CustodyUnverified || len(custody.Spec.Conflicts) != 0 {
				return Outcome{}, nil
			}
			return c.verify(ctx, custody, device)
		},
		func() (Outcome, error) {
			if custody.Spec.ActiveOperation == nil {
				return Outcome{}, nil
			}
			return c.progressActive(ctx, custody, device)
		},
		func() (Outcome, error) { return c.settleHolder(ctx, custody, device) },
		func() (Outcome, error) { return c.admit(ctx, custody, device) },
	}
	for _, step := range steps {
		outcome, err := step()
		if err != nil || outcome.Changed {
			return outcome, err
		}
		requeue = earliest(requeue, outcome.RequeueAfter)
	}
	return Outcome{RequeueAfter: requeue}, nil
}

// earliest returns the sooner of two requeue delays; zero means none.
func earliest(a, b time.Duration) time.Duration {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

func (c *Coordinator) device(ctx context.Context, name string) (*inventory.Device, error) {
	device := &inventory.Device{}
	if err := c.Client.Get(ctx, client.ObjectKey{Namespace: c.config().Namespace, Name: name}, device); apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return device, nil
}

// writeCustody is the only custody write. It carries the resourceVersion the
// caller read. A conflict is not an error: the next reconcile reads again and
// decides again.
func (c *Coordinator) writeCustody(ctx context.Context, custody *ops.DeviceCustody) (Outcome, error) {
	if err := c.Client.Update(ctx, custody); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return Outcome{Changed: true}, nil
		}
		return Outcome{}, err
	}
	return Outcome{Changed: true}, nil
}

func (c *Coordinator) markUnverified(ctx context.Context, custody *ops.DeviceCustody, conflict string) (Outcome, error) {
	if custody.Spec.State == ops.CustodyUnverified && slices.Contains(custody.Spec.Conflicts, conflict) {
		return Outcome{}, nil
	}
	custody.Spec.State = ops.CustodyUnverified
	if !slices.Contains(custody.Spec.Conflicts, conflict) {
		custody.Spec.Conflicts = append(custody.Spec.Conflicts, conflict)
	}
	return c.writeCustody(ctx, custody)
}

// settleHolder moves a holder whose work is done back to None, and retires a
// free Device that inventory retired.
func (c *Coordinator) settleHolder(ctx context.Context, custody *ops.DeviceCustody, device *inventory.Device) (Outcome, error) {
	spec := &custody.Spec
	if spec.State != ops.CustodyVerified || spec.ActiveOperation != nil || spec.RecoveryHold != nil {
		return Outcome{}, nil
	}
	switch {
	case spec.Holder.Type == ops.HolderService,
		spec.Holder.Type == ops.HolderRelease && len(spec.Holder.OpenGates) == 0:
		spec.Holder = ops.Holder{Type: ops.HolderNone}
		return c.writeCustody(ctx, custody)
	case spec.Holder.Type == ops.HolderNone && inventory.LifecycleOf(device) == inventory.DeviceRetired:
		spec.Holder = ops.Holder{Type: ops.HolderRetired}
		return c.writeCustody(ctx, custody)
	}
	return Outcome{}, nil
}

// get reads one operation. It returns nil if it does not exist or is a
// different incarnation.
func (c *Coordinator) get(ctx context.Context, reference ops.OperationReference) (ops.Operation, *Kind, error) {
	kind := c.Registry.Kind(reference.Kind)
	if kind == nil {
		return nil, nil, fmt.Errorf("custody names unregistered kind %s", reference.Kind)
	}
	operation := kind.New()
	if err := c.Client.Get(ctx, client.ObjectKey{Namespace: c.config().Namespace, Name: reference.Name}, operation); apierrors.IsNotFound(err) {
		return nil, kind, nil
	} else if err != nil {
		return nil, kind, err
	}
	if reference.UID != "" && string(operation.GetUID()) != reference.UID {
		return nil, kind, nil
	}
	return operation, kind, nil
}

// copyStatus copies the active operation and the last result from custody to
// operation status. Custody is written first, so after a crash this copies
// again. A missing operation is skipped.
func (c *Coordinator) copyStatus(ctx context.Context, custody *ops.DeviceCustody) (bool, error) {
	if result := custody.Spec.LastResult; result != nil {
		operation, _, err := c.get(ctx, result.OperationReference)
		if err != nil {
			return false, err
		}
		// An ended status is final. It differs from the result only when a
		// racing refusal ended an operation that custody then withdrew.
		if operation != nil && !(operation.OperationStatus().Phase.Ended() && operation.OperationStatus().Phase != result.Phase) {
			want := resultStatus(*result, operation.OperationStatus())
			if changed, err := c.writeStatus(ctx, operation, want); err != nil || changed {
				return changed, err
			}
		}
	}
	// A held operation has ended, even if a rebuild found it unfinished. It
	// is never admitted again.
	if hold := custody.Spec.RecoveryHold; hold != nil {
		operation, _, err := c.get(ctx, hold.OperationReference)
		if err != nil {
			return false, err
		}
		if operation != nil && !operation.OperationStatus().Phase.Ended() {
			status := *operation.OperationStatus()
			finished := metav1.NewTime(c.now())
			status.Phase, status.Reason, status.Message, status.FinishedAt = ops.PhaseOutcomeUnknown, hold.Reason, "custody holds this operation because its outcome is unknown", &finished
			if changed, err := c.writeStatus(ctx, operation, status); err != nil || changed {
				return changed, err
			}
		}
	}
	active := custody.Spec.ActiveOperation
	if active == nil {
		return false, nil
	}
	operation, _, err := c.get(ctx, active.OperationReference)
	if err != nil {
		return false, err
	}
	// An operation refused by a racing coordinator, or deleted, before its
	// admission was copied has been delivered nowhere: delivery waits for
	// the Admitted status. Withdraw it.
	if active.Phase == ops.PhaseAdmitted && (operation == nil || operation.OperationStatus().Phase.Ended()) {
		custody.Spec.ActiveOperation = nil
		custody.Spec.LastResult = &ops.OperationResult{OperationReference: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseCancelled, Reason: ReasonWithdrawn, Message: "the operation ended or was deleted before its admission was recorded", FinishedAt: metav1.NewTime(c.now())}
		_, err := c.writeCustody(ctx, custody)
		return true, err
	}
	if operation == nil {
		return false, nil
	}
	return c.writeStatus(ctx, operation, activeStatus(*active))
}

func activeStatus(active ops.ActiveOperation) ops.OperationStatus {
	snapshot := active.Snapshot
	admitted, deadline := active.AdmittedAt, active.Deadline
	return ops.OperationStatus{Phase: active.Phase, Fence: active.Fence, AdmittedAt: &admitted, Deadline: &deadline, InputDigest: active.InputDigest, Snapshot: &snapshot}
}

func resultStatus(result ops.OperationResult, current *ops.OperationStatus) ops.OperationStatus {
	finished := result.FinishedAt
	status := ops.OperationStatus{Phase: result.Phase, Reason: result.Reason, Message: result.Message, Fence: result.Fence, FinishedAt: &finished, Evidence: result.Evidence}
	// Keep what admission recorded.
	status.AdmittedAt, status.Deadline, status.InputDigest, status.Snapshot = current.AdmittedAt, current.Deadline, current.InputDigest, current.Snapshot
	return status
}

// writeStatus is the only operation status write. It carries the
// resourceVersion read with the operation.
func (c *Coordinator) writeStatus(ctx context.Context, operation ops.Operation, want ops.OperationStatus) (bool, error) {
	if statusEqual(*operation.OperationStatus(), want) {
		return false, nil
	}
	*operation.OperationStatus() = want
	if err := c.Client.Status().Update(ctx, operation); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return true, nil
}

func statusEqual(left, right ops.OperationStatus) bool {
	normalize := func(status ops.OperationStatus) ops.OperationStatus {
		for _, value := range []**metav1.Time{&status.AdmittedAt, &status.Deadline, &status.FinishedAt} {
			if *value != nil {
				truncated := metav1.NewTime((*value).Time.UTC().Truncate(time.Second))
				*value = &truncated
			}
		}
		if status.Evidence != nil {
			status.Evidence = status.Evidence.DeepCopy()
			status.Evidence.Object = nil
		}
		return status
	}
	return reflect.DeepEqual(normalize(left), normalize(right))
}

// progressActive ends an active operation whose deadline has passed, and
// applies a non-effectful operation once every site confirms its fence.
func (c *Coordinator) progressActive(ctx context.Context, custody *ops.DeviceCustody, device *inventory.Device) (Outcome, error) {
	cfg := c.config()
	active := *custody.Spec.ActiveOperation
	kind := c.Registry.Kind(active.Kind)
	if kind == nil {
		return Outcome{}, fmt.Errorf("custody names unregistered kind %s", active.Kind)
	}
	now := c.now()
	due := active.Deadline.Add(cfg.SkewMargin)
	if !now.Before(due) {
		if kind.Effectful {
			c.holdUnknown(custody, active, ReasonDeadlineExceeded, "the deadline passed without a proven result", nil)
		} else {
			c.finishActive(custody, active, ops.PhaseFailed, ReasonSiteNotConfirmed, "no site confirmed the fence before the deadline; nothing was applied", nil)
		}
		return c.writeCustody(ctx, custody)
	}
	if kind.Effectful {
		return Outcome{RequeueAfter: due.Sub(now)}, nil
	}
	confirmed, highest, ok, err := c.siteFence(ctx, device, active.Fence)
	if err != nil || !ok {
		return Outcome{RequeueAfter: min(due.Sub(now), 5*time.Second)}, err
	}
	operation, _, err := c.get(ctx, active.OperationReference)
	if err != nil {
		return Outcome{}, err
	}
	if operation == nil {
		c.finishActive(custody, active, ops.PhaseCancelled, ReasonWithdrawn, "the operation was deleted before it was applied", nil)
		return c.writeCustody(ctx, custody)
	}
	spec, refusal := kind.Apply(ctx, &ApplyContext{Operation: operation, Custody: custody.DeepCopy(), Device: device, Registry: c.Registry, Reader: c.Client, ConfirmedFence: confirmed, HighestFence: highest, Now: now})
	if refusal = c.Registry.declaredRefusal(kind.GVK.Kind, settleHookKind(kind, Rule{Hold: HoldRequired}, custody), refusal); refusal != nil {
		c.finishActive(custody, active, ops.PhaseFailed, refusal.Reason, refusal.Message, nil)
		return c.writeCustody(ctx, custody)
	}
	spec.ActiveOperation = nil
	evidence, err := rawEvidence(ops.RecoveryResultEvidence{Decision: ops.RecoveryDecision(variantOf(kind, operation)), ConfirmedFence: confirmed})
	if err != nil {
		return Outcome{}, err
	}
	spec.LastResult = &ops.OperationResult{OperationReference: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseSucceeded, FinishedAt: metav1.NewTime(now), Evidence: evidence}
	custody.Spec = *spec
	return c.writeCustody(ctx, custody)
}

func variantOf(kind *Kind, operation ops.Operation) string {
	if kind.Variant == nil {
		return ""
	}
	return kind.Variant(operation)
}

// siteFence reports whether every site serving the Device has stored at
// least the fence, and the lowest and highest fences they report. A Device
// with no configured site is never confirmed.
func (c *Coordinator) siteFence(ctx context.Context, device *inventory.Device, fence int64) (int64, int64, bool, error) {
	lowest, highest := int64(-1), fence
	sites := c.Sites.Serving(device)
	if len(sites) == 0 {
		return 0, 0, false, nil
	}
	for _, site := range sites {
		report, reported, err := c.Sites.Fence(ctx, site, device)
		stored := report.Stopped
		if err != nil || !reported || stored < fence {
			return 0, 0, false, err
		}
		if lowest < 0 || stored < lowest {
			lowest = stored
		}
		highest = max(highest, report.Seen)
	}
	return lowest, highest, true, nil
}

// siteSeen returns the highest fence any serving site reports having seen.
func (c *Coordinator) siteSeen(ctx context.Context, device *inventory.Device) (int64, error) {
	var highest int64
	for _, site := range c.Sites.Serving(device) {
		report, reported, err := c.Sites.Fence(ctx, site, device)
		if err != nil {
			return 0, err
		}
		if reported {
			highest = max(highest, report.Seen, report.Stopped)
		}
	}
	return highest, nil
}

// finishActive ends the active operation without a hold. A staff holder
// with nothing left to settle returns to None.
func (c *Coordinator) finishActive(custody *ops.DeviceCustody, active ops.ActiveOperation, phase ops.Phase, reason, message string, evidence []byte) {
	spec := &custody.Spec
	spec.ActiveOperation = nil
	spec.LastResult = &ops.OperationResult{OperationReference: active.OperationReference, Fence: active.Fence, Phase: phase, Reason: reason, Message: message, FinishedAt: metav1.NewTime(c.now())}
	if evidence != nil {
		spec.LastResult.Evidence = rawExtension(evidence)
	}
	if spec.Holder.Type == ops.HolderService && spec.RecoveryHold == nil {
		spec.Holder = ops.Holder{Type: ops.HolderNone}
	}
}

// holdUnknown moves the active operation into the recovery hold. The holder
// stays. A settling operation that itself ends unknown replaces the hold it
// was settling.
func (c *Coordinator) holdUnknown(custody *ops.DeviceCustody, active ops.ActiveOperation, reason, message string, evidence []byte) {
	spec := &custody.Spec
	now := metav1.NewTime(c.now())
	spec.ActiveOperation = nil
	deadline, snapshot := active.Deadline, active.Snapshot
	next := &ops.RecoveryHold{OperationReference: active.OperationReference, Fence: active.Fence, Since: now, Reason: reason, Deadline: &deadline, Snapshot: &snapshot}
	// A retry that itself ends unknown keeps the original operation's name
	// and Retry count, so the next retry is named on from it.
	if previous := spec.RecoveryHold; previous != nil && active.Settles != nil && *active.Settles == previous.OperationReference && previous.Retries > 0 && active.Name == RetryName(*previous, previous.Retries) {
		next.RetryName, next.Retries = previous.RetryName, previous.Retries
		if next.RetryName == "" {
			next.RetryName = previous.Name
		}
	}
	spec.RecoveryHold = next
	spec.LastResult = &ops.OperationResult{OperationReference: active.OperationReference, Fence: active.Fence, Phase: ops.PhaseOutcomeUnknown, Reason: reason, Message: message, FinishedAt: now}
	if evidence != nil {
		spec.LastResult.Evidence = rawExtension(evidence)
	}
}

// candidate is one operation that has not ended, with its kind.
type candidate struct {
	operation ops.Operation
	kind      *Kind
}

// candidates returns the operations for the Device that have not ended and
// that custody does not name as active, oldest first. Ties break on kind,
// name, then UID, so every coordinator orders them the same way.
func (c *Coordinator) candidates(ctx context.Context, custody *ops.DeviceCustody) ([]candidate, error) {
	var out []candidate
	for _, kind := range c.Registry.Kinds() {
		list := kind.NewList()
		if err := c.Client.List(ctx, list, client.InNamespace(c.config().Namespace)); err != nil {
			if meta.IsNoMatchError(err) {
				continue
			}
			return nil, err
		}
		for _, operation := range kind.Items(list) {
			if operation.Envelope().DeviceRef.Name != custody.Name || operation.OperationStatus().Phase.Ended() || !operation.GetDeletionTimestamp().IsZero() {
				continue
			}
			if active := custody.Spec.ActiveOperation; active != nil && active.Kind == kind.GVK.Kind && active.UID == string(operation.GetUID()) {
				continue
			}
			if hold := custody.Spec.RecoveryHold; hold != nil && hold.Kind == kind.GVK.Kind && hold.UID == string(operation.GetUID()) {
				continue
			}
			out = append(out, candidate{operation: operation, kind: kind})
		}
	}
	orderCandidates(out)
	return out, nil
}

// orderCandidates sorts oldest first. Ties break on kind, name, then UID.
func orderCandidates(out []candidate) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		at, bt := a.operation.GetCreationTimestamp(), b.operation.GetCreationTimestamp()
		if !at.Equal(&bt) {
			return at.Before(&bt)
		}
		if a.kind.GVK.Kind != b.kind.GVK.Kind {
			return a.kind.GVK.Kind < b.kind.GVK.Kind
		}
		if a.operation.GetName() != b.operation.GetName() {
			return a.operation.GetName() < b.operation.GetName()
		}
		return a.operation.GetUID() < b.operation.GetUID()
	})
}

// decision is what admission decided for one candidate.
type decision struct {
	phase   ops.Phase // Pending, Failed or Cancelled; empty to admit
	reason  string
	message string
	row     Rule
	settles *ops.OperationReference
	extra   map[string]string
}

// admit evaluates pending candidates and admits at most one with a single
// conditional custody update.
func (c *Coordinator) admit(ctx context.Context, custody *ops.DeviceCustody, device *inventory.Device) (Outcome, error) {
	candidates, err := c.candidates(ctx, custody)
	if err != nil {
		return Outcome{}, err
	}
	// Decide only on custody that is still current after the list. A holder
	// change that landed between the custody read and the list, such as a
	// claim followed by its installation's create, is then seen before any
	// refusal is written.
	fresh := &ops.DeviceCustody{}
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(custody), fresh); err != nil {
		return Outcome{}, err
	}
	if fresh.ResourceVersion != custody.ResourceVersion {
		return Outcome{Changed: true}, nil
	}
	superseded := c.superseded(candidates, custody)
	queued := false
	changed := false
	for _, candidate := range candidates {
		operation, kind := candidate.operation, candidate.kind
		var d decision
		if superseded[operation.GetUID()] {
			d = decision{phase: ops.PhaseCancelled, reason: ReasonSuperseded, message: "a newer pending operation for the same authority replaced it"}
		} else {
			d = c.decide(ctx, kind, operation, custody, device, queued)
		}
		if d.phase == "" {
			// Every precondition holds on the custody just read.
			outcome, err := c.admitOne(ctx, custody, device, kind, operation, d)
			outcome.Changed = true
			return outcome, err
		}
		// An operation waiting for the hold to settle does not hold back
		// the queue: the operation that settles it goes first.
		if d.phase == ops.PhasePending && d.reason != ReasonRecoveryHold {
			queued = true
		}
		wrote, err := c.mark(ctx, operation, d)
		if err != nil {
			return Outcome{}, err
		}
		changed = changed || wrote
	}
	return Outcome{Changed: changed}, nil
}

// superseded returns pending operations replaced by a newer one of the same
// superseding kind and authority.
func (c *Coordinator) superseded(candidates []candidate, custody *ops.DeviceCustody) map[types.UID]bool {
	out := map[types.UID]bool{}
	newest := map[string]ops.Operation{}
	hold := custody.Spec.RecoveryHold
	for _, candidate := range candidates {
		operation, kind := candidate.operation, candidate.kind
		// Only an operation that passes the creator, authority and holder
		// checks on a queue row takes part: staff rows never queue, so
		// staff requests never replace one another, and a request naming
		// another's allocation replaces nothing. An operation that settles
		// the hold is never replaced.
		row, ok := c.rowFor(kind, operation, custody)
		if !kind.Supersedes || !ok || !row.Queue || (hold != nil && (c.settlesHold(kind, *hold, operation) || isRetry(kind, hold, operation))) {
			continue
		}
		key := kind.GVK.Kind + "/" + authorityKey(operation.Envelope().Authority)
		if previous, ok := newest[key]; ok {
			out[previous.GetUID()] = true
		}
		newest[key] = operation
	}
	return out
}

// rowFor returns the row that admits the operation from the current custody
// state and holder, if its creator, authority and allocation match.
func (c *Coordinator) rowFor(kind *Kind, operation ops.Operation, custody *ops.DeviceCustody) (Rule, bool) {
	envelope := operation.Envelope()
	rows := c.Registry.matchRows(kind, operation, c.config().Principals.Classify(envelope.Creator))
	holder := custody.Spec.Holder
	rows = slices.DeleteFunc(rows, func(rule Rule) bool {
		return rule.custodyState() != custody.Spec.State || !rule.admitsHolder(holder.Type)
	})
	if len(rows) == 0 || (envelope.Authority.AllocationRef != nil && (holder.AllocationRef == nil || holder.AllocationRef.Name != envelope.Authority.AllocationRef.Name)) {
		return Rule{}, false
	}
	return rows[0], true
}

func authorityKey(authority ops.Authority) string {
	if authority.AllocationRef != nil {
		return "allocation:" + authority.AllocationRef.Name
	}
	return "service"
}

// mark records a decision that is not an admission. It never writes custody.
func (c *Coordinator) mark(ctx context.Context, operation ops.Operation, d decision) (bool, error) {
	current := operation.OperationStatus()
	want := ops.OperationStatus{Phase: d.phase, Reason: d.reason, Message: d.message}
	if d.phase.Ended() {
		finished := metav1.NewTime(c.now())
		want.FinishedAt = &finished
	} else if current.Phase == d.phase && current.Reason == d.reason {
		return false, nil
	}
	return c.writeStatus(ctx, operation, want)
}

// decide evaluates the admission preconditions for one candidate in order.
// The first that fails decides. Queue rows wait in Pending for conditions
// that can clear; staff rows and everything else end.
func (c *Coordinator) decide(ctx context.Context, kind *Kind, operation ops.Operation, custody *ops.DeviceCustody, device *inventory.Device, queueAhead bool) decision {
	cfg := c.config()
	now := c.now()
	envelope := operation.Envelope()
	class := cfg.Principals.Classify(envelope.Creator)
	pending := operation.OperationStatus().Phase == ops.PhasePending
	fail := func(reason, format string, args ...any) decision {
		return decision{phase: ops.PhaseFailed, reason: reason, message: fmt.Sprintf(format, args...)}
	}
	// An operation named for a Retry waits for it: a staff retry created
	// before its Retry is applied neither fails nor expires, or its name
	// would be used up and the hold could not be settled.
	retry := isRetry(kind, custody.Spec.RecoveryHold, operation)
	if class == CreatorStaff && !retry && !now.Before(operation.GetCreationTimestamp().Add(cfg.StaffExpiry)) {
		return decision{phase: ops.PhaseCancelled, reason: ReasonExpired, message: "a staff operation must be admitted within " + cfg.StaffExpiry.String()}
	}
	rows := c.Registry.matchRows(kind, operation, class)
	if len(rows) == 0 {
		return fail(ReasonCreatorNotPermitted, "no row admits %s from a %s creator with this authority", kind.GVK.Kind, class)
	}
	if envelope.DeviceRef.UID != string(device.UID) {
		return fail(ReasonDeviceIdentityMismatch, "deviceRef.uid does not match the Device")
	}
	// Precondition 1: custody state.
	rows = slices.DeleteFunc(rows, func(rule Rule) bool { return rule.custodyState() != custody.Spec.State })
	if len(rows) == 0 {
		return c.wait(class, ReasonCustodyUnverified, "custody is Unverified and admits only a DeviceRecovery with Resolve")
	}
	// Preconditions 2 and 3: holder and allocation.
	holder := custody.Spec.Holder
	rows = slices.DeleteFunc(rows, func(rule Rule) bool { return !rule.admitsHolder(holder.Type) })
	if len(rows) == 0 || (envelope.Authority.AllocationRef != nil && (holder.AllocationRef == nil || holder.AllocationRef.Name != envelope.Authority.AllocationRef.Name)) {
		d := fail(ReasonHolderMismatch, "the holder is %s; this operation needs a different holder or allocation", holder.Type)
		if pending {
			d.phase = ops.PhaseCancelled
		}
		return d
	}
	row := rows[0]
	// Names follow the row's rule, or the kind's. An operation created after
	// a Retry is named for the Retry instead.
	naming, name := kind.Naming, kind.Name
	if row.Naming != KindNaming {
		naming, name = row.Naming, row.Name
	}
	switch {
	case retry:
	case naming == DeterministicName:
		if want := name(operation); operation.GetName() != want {
			return fail(ReasonNameMismatch, "the operation must be named %s", want)
		}
	case naming == GeneratedName:
		if operation.GetGenerateName() == "" {
			return fail(ReasonNameMismatch, "this operation must be created with a generated name")
		}
	}
	// Recovery holds.
	var settles *ops.OperationReference
	switch hold := custody.Spec.RecoveryHold; {
	case row.Hold == HoldIgnored:
	case row.Hold == HoldRequired && hold == nil:
		return fail(ReasonRecoveryHold, "this operation decides a recovery hold, and there is none")
	case row.Hold == HoldRequired:
	case hold != nil && retry && !c.settlesHold(kind, *hold, operation):
		return decision{phase: ops.PhasePending, reason: ReasonRecoveryHold, message: "waiting for the Retry this operation is named for"}
	case row.Hold == HoldSettlesRequired && (hold == nil || !c.settlesHold(kind, *hold, operation)):
		return fail(ReasonHolderMismatch, "from this holder, the operation is admitted only to settle a hold of its kind")
	case hold != nil:
		if !c.settlesHold(kind, *hold, operation) {
			if row.Queue {
				return decision{phase: ops.PhasePending, reason: ReasonRecoveryHold, message: "waiting: " + hold.Kind + " " + hold.Name + " is held, and only its settling operations are admitted"}
			}
			return fail(ReasonRecoveryHold, "%s %s is held; only its settling operations are admitted", hold.Kind, hold.Name)
		}
		reference := hold.OperationReference
		settles = &reference
	}
	// Precondition 4: one active operation.
	if custody.Spec.ActiveOperation != nil || (row.Queue && queueAhead) {
		if row.Queue {
			return decision{phase: ops.PhasePending, reason: ReasonOperationInProgress, message: "waiting for the running operation"}
		}
		return fail(ReasonOperationInProgress, "another operation is running, and staff operations never wait")
	}
	admission := &Admission{Operation: operation, Custody: custody.DeepCopy(), Device: device, Row: row, Registry: c.Registry, Reader: c.Client, Now: now, DiscoveryInterval: cfg.DiscoveryInterval}
	// Rule 7, the kind's own checks, runs before the shared identity and
	// key checks, so a kind's more specific reason, such as a system UUID
	// that is not recorded, is the one reported.
	if kind.Check != nil {
		if refusal := c.Registry.declaredRefusal(kind.GVK.Kind, settleHookKind(kind, row, custody), kind.Check(ctx, admission)); refusal != nil {
			return fail(refusal.Reason, "%s", refusal.Message)
		}
	}
	// Preconditions 5 and 6: current discovery and a unique BMC key.
	if kind.checksDiscovery(operation) {
		if refusal := c.identityCurrent(device, now); refusal != nil {
			if row.Queue {
				return decision{phase: ops.PhasePending, reason: refusal.Reason, message: refusal.Message}
			}
			return fail(refusal.Reason, "%s", refusal.Message)
		}
		if refusal := c.sharedKey(ctx, device); refusal != nil {
			return fail(refusal.Reason, "%s", refusal.Message)
		}
	}
	// Kind fields for the snapshot come from the same Device and clock the
	// checks saw.
	var extra map[string]string
	if kind.SnapshotExtra != nil {
		extra = kind.SnapshotExtra(admission)
		if err := validExtra(extra); err != nil {
			return fail(ReasonSnapshotInvalid, "%s", err.Error())
		}
	}
	return decision{row: row, settles: settles, extra: extra}
}

// wait returns Pending for queue rows and Failed otherwise.
func (c *Coordinator) wait(class CreatorClass, reason, message string) decision {
	if class != CreatorStaff {
		return decision{phase: ops.PhasePending, reason: reason, message: message}
	}
	return decision{phase: ops.PhaseFailed, reason: reason, message: message}
}

// settlesHold reports whether the operation settles the hold. A settle rule
// that requires a Retry admits only the operation named for the current
// Retry, after that Retry is applied.
func (c *Coordinator) settlesHold(kind *Kind, hold ops.RecoveryHold, operation ops.Operation) bool {
	for _, rule := range kind.Settles {
		if rule.Held != hold.Kind {
			continue
		}
		if !rule.RequiresRetry || (hold.RetryAuthorizedBy != nil && operation.GetName() == RetryName(hold, hold.Retries)) {
			return true
		}
	}
	return false
}

// retries reports whether the kind settles the hold only after a Retry.
func retries(kind *Kind, hold *ops.RecoveryHold) bool {
	return hold != nil && slices.ContainsFunc(kind.Settles, func(rule SettleRule) bool { return rule.Held == hold.Kind && rule.RequiresRetry })
}

// RetryName is the name of the operation created after the count-th Retry
// of a hold: the held operation's name, "-retry-" and the count.
func RetryName(hold ops.RecoveryHold, count int32) string {
	base := hold.RetryName
	if base == "" {
		base = hold.Name
	}
	return base + "-retry-" + strconv.Itoa(int(count))
}

// isRetry reports whether the operation is named as a retry of the hold,
// for the Retry already applied or the next one.
func isRetry(kind *Kind, hold *ops.RecoveryHold, operation ops.Operation) bool {
	if !retries(kind, hold) {
		return false
	}
	name := operation.GetName()
	return name == RetryName(*hold, hold.Retries) || name == RetryName(*hold, hold.Retries+1)
}

// identityCurrent is precondition 5's discovery half. Identity is current when
// BMC discovery matched the recorded identity against this Device
// generation, completed an inventory read within DiscoveryMaxAge, and
// recorded the BMC key at the current endpoint.
func (c *Coordinator) identityCurrent(device *inventory.Device, now time.Time) *Refusal {
	if device.Spec.BMC == nil || device.Spec.Identity.SystemUUID == "" {
		return refuse(ReasonDeviceIdentityMismatch, "the Device records no BMC endpoint or system UUID")
	}
	condition := meta.FindStatusCondition(device.Status.Conditions, inventory.ConditionIdentityMatched)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != device.Generation {
		return refuse(ReasonDeviceIdentityMismatch, "discovery has not matched the Device identity at its current generation")
	}
	discovery := device.Status.Discovery
	if discovery == nil || discovery.Inventory == nil || now.Sub(discovery.Inventory.ObservedAt.Time) > c.config().DiscoveryMaxAge {
		return refuse(ReasonDeviceIdentityMismatch, "discovery is older than %s", c.config().DiscoveryMaxAge)
	}
	if discovery.TLS == nil || discovery.TLS.PublicKeySHA256 == "" || discovery.TLS.Endpoint != device.Spec.BMC.Endpoint {
		return refuse(ReasonDeviceIdentityMismatch, "discovery has not recorded the BMC key at the current endpoint")
	}
	return nil
}

// sharedKey is precondition 6: no other Device that is not retired records
// the same BMC key digest.
func (c *Coordinator) sharedKey(ctx context.Context, device *inventory.Device) *Refusal {
	digest := device.Status.Discovery.TLS.PublicKeySHA256
	var devices inventory.DeviceList
	if err := c.Client.List(ctx, &devices); err != nil {
		return refuse(ReasonSharedPublicKey, "the other Devices could not be read, so the BMC key cannot be shown unique")
	}
	for index := range devices.Items {
		other := &devices.Items[index]
		if other.UID == device.UID || inventory.LifecycleOf(other) == inventory.DeviceRetired {
			continue
		}
		if discovery := other.Status.Discovery; discovery != nil && discovery.TLS != nil && discovery.TLS.PublicKeySHA256 == digest {
			return refuse(ReasonSharedPublicKey, "another Device records the same BMC key")
		}
	}
	return nil
}

// admitOne writes the admission: one conditional custody update that names
// the operation, raises the fence, sets the deadline and retains the exact
// inputs and Device snapshot. Status is copied on the next reconcile.
func (c *Coordinator) admitOne(ctx context.Context, custody *ops.DeviceCustody, device *inventory.Device, kind *Kind, operation ops.Operation, d decision) (Outcome, error) {
	digest, err := kind.Digest(operation)
	if err != nil {
		return Outcome{}, err
	}
	// The Device must not change between the checks and the custody update.
	// If it did, discovery may have moved, so decide again.
	current := &inventory.Device{}
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(device), current); err != nil {
		return Outcome{}, err
	}
	if current.ResourceVersion != device.ResourceVersion {
		return Outcome{Changed: true}, nil
	}
	now := c.now()
	spec := &custody.Spec
	// The fence rises above every fence a site has seen, so a site report at
	// or above it proves the site received this admission.
	seen, err := c.siteSeen(ctx, device)
	if err != nil {
		return Outcome{}, err
	}
	spec.Fence = max(spec.Fence, seen) + 1
	deadline := kind.Deadline
	if kind.DeadlineFor != nil {
		deadline = kind.DeadlineFor(operation)
	}
	active := &ops.ActiveOperation{
		OperationReference: ops.OperationReference{Kind: kind.GVK.Kind, Name: operation.GetName(), UID: string(operation.GetUID())},
		Fence:              spec.Fence,
		Phase:              ops.PhaseAdmitted,
		AdmittedAt:         metav1.NewTime(now),
		Deadline:           metav1.NewTime(now.Add(deadline)),
		InputDigest:        digest,
		Snapshot:           snapshot(kind, operation, device, d.extra),
		Creator:            operation.Envelope().Creator,
		Settles:            d.settles,
	}
	spec.ActiveOperation = active
	if d.row.Creator == CreatorStaff && spec.Holder.Type == ops.HolderNone {
		spec.Holder = ops.Holder{Type: ops.HolderService}
	}
	// A Retry authorizes one settling operation.
	if hold := spec.RecoveryHold; hold != nil && d.settles != nil {
		for _, rule := range kind.Settles {
			if rule.Held == hold.Kind && rule.RequiresRetry {
				hold.RetryAuthorizedBy = nil
			}
		}
	}
	outcome, err := c.writeCustody(ctx, custody)
	outcome.RequeueAfter = 0
	return outcome, err
}

func snapshot(kind *Kind, operation ops.Operation, device *inventory.Device, extra map[string]string) ops.DeviceSnapshot {
	out := ops.DeviceSnapshot{SystemUUID: device.Spec.Identity.SystemUUID, DeviceGeneration: device.Generation, Extra: extra}
	if device.Spec.BMC != nil {
		out.BMCEndpoint = device.Spec.BMC.Endpoint
	}
	if discovery := device.Status.Discovery; discovery != nil {
		if discovery.TLS != nil {
			out.BMCPublicKeySHA256 = discovery.TLS.PublicKeySHA256
		}
		if discovery.Inventory != nil {
			out.DiscoveryObservedAt = discovery.Inventory.ObservedAt.UTC().Format(time.RFC3339)
		}
	}
	if kind.BootInterface != nil {
		name := kind.BootInterface(operation)
		for _, iface := range device.Spec.Interfaces {
			if iface.Name == name {
				out.BootMACAddress = iface.MACAddress
			}
		}
	}
	return out
}

var extraKey = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,62}$`)

// validExtra bounds the fields a kind adds to the snapshot.
func validExtra(extra map[string]string) error {
	if len(extra) > 16 {
		return errors.New("a kind adds at most 16 snapshot fields")
	}
	for key, value := range extra {
		if !extraKey.MatchString(key) || len(value) > 256 {
			return fmt.Errorf("snapshot field %q is not a camelCase name with a value of at most 256 bytes", key)
		}
	}
	return nil
}
