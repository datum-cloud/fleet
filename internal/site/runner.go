package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// Assignment is the handoff payload: one admitted operation, its fence and
// deadline, the Device snapshot the coordinator captured at admission, and
// the exact inputs. Bootstrap, when it is supported, arrives as a sealed
// reference and digest, never as a payload. It replaces the request JSON of
// the old handoff in Increment C.
type Assignment struct {
	// Device is "<namespace>/<name>" in the provider project.
	Device      string                 `json:"device"`
	Operation   ops.OperationReference `json:"operation"`
	Fence       int64                  `json:"fence"`
	AdmittedAt  time.Time              `json:"admittedAt"`
	Deadline    time.Time              `json:"deadline"`
	Snapshot    ops.DeviceSnapshot     `json:"snapshot"`
	InputDigest string                 `json:"inputDigest"`
	// Inputs is the operation's spec exactly as admitted. Its SHA-256 is
	// InputDigest.
	Inputs json.RawMessage  `json:"inputs"`
	Sealed *ops.SealedInput `json:"sealed,omitempty"`
}

const maxInputs = 16 << 10

// Validate checks the payload's shape. It does not check the inputs
// against their digest; the worker does, before anything is sent.
func (a Assignment) Validate() error {
	namespace, name, ok := strings.Cut(a.Device, "/")
	switch {
	case !ok || namespace == "" || name == "" || strings.Contains(name, "/"):
		return errors.New("site: an assignment names one Device as namespace/name")
	case a.Operation.Kind == "" || a.Operation.Name == "" || a.Operation.UID == "":
		return errors.New("site: an assignment names one operation incarnation")
	case a.Fence < 1 || a.AdmittedAt.IsZero() || a.Deadline.IsZero() || !a.AdmittedAt.Before(a.Deadline) || a.InputDigest == "":
		return errors.New("site: an assignment carries a fence, its admission time, a later deadline and an input digest")
	case len(a.Inputs) == 0 || len(a.Inputs) > maxInputs:
		return errors.New("site: an assignment carries bounded inputs")
	case a.Snapshot.BMCEndpoint == "" || a.Snapshot.SystemUUID == "" || a.Snapshot.BMCPublicKeySHA256 == "":
		return errors.New("site: an assignment carries the Device snapshot")
	}
	return nil
}

// Reasons the site framework reports. They are shared reasons in custody,
// so every kind may report them. A refusal ends the operation Failed when
// nothing was sent for it, and OutcomeUnknown otherwise.
const (
	ReasonUnsupportedKind   = custody.ReasonUnsupportedKind
	ReasonInputMismatch     = custody.ReasonInputMismatch
	ReasonCredentialMissing = custody.ReasonCredentialMissing
	ReasonPinNotApproved    = custody.ReasonPinNotApproved
	ReasonPublicKeyMismatch = custody.ReasonPublicKeyMismatch
	ReasonNotEnrolled       = custody.ReasonNotEnrolled
	ReasonBMCUnavailable    = custody.ReasonBMCUnavailable
	ReasonWorkerRunning     = custody.ReasonWorkerRunning
	ReasonStale             = custody.ReasonStale
	ReasonSuperseded        = custody.ReasonSuperseded
	ReasonDeadline          = custody.ReasonDeadlineExceeded
	ReasonStep              = custody.ReasonStepUnknown
)

// Work is what a kind sees while it runs.
type Work struct {
	Assignment
	// Connection is what the site's Access prepared, such as a
	// *RedfishConnection. A kind asserts the type it supports.
	Connection any
	store      *Store
	sent       bool
}

// Kind is the site worker contract for one operation kind.
type Kind interface {
	// Check runs after the site's Access has checked the credential, the
	// pin and the identity, and before anything is sent. It may read the
	// server but must not change it.
	Check(ctx context.Context, work *Work) *custody.Refusal
	// Run performs the kind's effects, each through Work.Step, and returns
	// its typed evidence.
	Run(ctx context.Context, work *Work) (any, error)
}

// Step is one effect a kind sends to the server.
type Step struct {
	// Name is unique within the operation.
	Name string
	// Payload is the kind's bounded record of the step, journaled before it
	// is sent, such as the send time or the power state before it.
	Payload any
	Send    func(context.Context) error
	// Observe decides, from the server and the journaled payload, whether a
	// step found journaled but not done took effect.
	Observe func(ctx context.Context, payload json.RawMessage) (bool, error)
	// Idempotent marks a step that is safe to send again after a crash,
	// such as powering off before a boot of this phase was requested.
	Idempotent bool
}

// StartBy is implemented by a kind whose work must begin soon after
// admission. A worker that has sent nothing and starts later than the
// window after the admission time refuses as Stale.
type StartBy interface {
	StartBy() time.Duration
}

// Store is the site's store, for a kind's per-operation records.
func (w *Work) Store() *Store { return w.store }

// Journaled reports whether a step of this operation is journaled done.
func (w *Work) Journaled(step string) bool {
	_, done := w.JournaledAt(step)
	return done
}

// JournaledAt returns when a step of this operation was journaled done.
func (w *Work) JournaledAt(step string) (time.Time, bool) {
	journal, err := w.store.Journal(w.Device)
	if err != nil {
		return time.Time{}, false
	}
	for _, entry := range journal {
		if entry.Operation == w.Operation.UID && entry.Step == step && entry.State == StepDone {
			return entry.At, true
		}
	}
	return time.Time{}, false
}

// Step journals a step, then sends it, then records it done. A step found
// journaled but not done, after a crash or a lost response, is observed
// instead of sent again, unless it is Idempotent; if the observation cannot
// show it took effect, the outcome is unknown. A step below the stored fence
// is refused.
func (w *Work) Step(ctx context.Context, step Step) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	var payload json.RawMessage
	if step.Payload != nil {
		data, err := json.Marshal(step.Payload)
		if err != nil {
			return err
		}
		payload = data
	}
	name := step.Name
	prior, recorded, err := w.store.IntendWithPayload(w.Device, w.Fence, w.Operation.UID, name, payload)
	switch {
	case errors.Is(err, ErrStaleFence):
		return errSuperseded
	case errors.Is(err, ErrStepReplayed) && step.Idempotent:
		w.sent = true
		if err := step.Send(ctx); err != nil {
			return err
		}
	case errors.Is(err, ErrStepReplayed):
		w.sent = true
		if step.Observe == nil {
			return fmt.Errorf("%w: %s", errStepUnknown, name)
		}
		done, err := step.Observe(ctx, recorded)
		if err != nil {
			return err
		}
		if !done {
			return fmt.Errorf("%w: %s", errStepUnknown, name)
		}
	case err != nil:
		return err
	case prior == StepDone:
		return nil
	default:
		w.sent = true
		// The fence is checked again just before the send, so a newer
		// admission stored after the step was journaled stops it.
		if beforeSend != nil {
			beforeSend()
		}
		if fence, _, err := w.store.Current(w.Device); err != nil {
			return err
		} else if fence > w.Fence {
			return errSuperseded
		}
		if err := step.Send(ctx); err != nil {
			return err
		}
	}
	if err := w.store.Done(w.Device, w.Fence, w.Operation.UID, name); errors.Is(err, ErrStaleFence) {
		return errSuperseded
	} else if err != nil {
		return err
	}
	return nil
}

// beforeSend runs between journaling a step and re-checking the fence. Only
// tests set it.
var beforeSend func()

var (
	errSuperseded  = errors.New("a newer operation was admitted for the Device")
	errStepUnknown = errors.New("a journaled step's outcome could not be observed")
)

// Runner runs assignments at one site.
type Runner struct {
	Site    string
	Store   *Store
	Results *Results
	// Access checks that it is safe to reach the Device's server and opens
	// the connection kinds use. Redfish sites use RedfishAccess; the local
	// VM and simulator executors add their own in Increment C.
	Access Access
	Kinds  map[string]Kind
	// Poll is how often a worker checks for a newer fence and for the run
	// lock. It defaults to one second.
	Poll time.Duration
	Now  func() time.Time
	// waiting, when set, is called each time a worker waits for the run
	// lock. Tests use it to know a copy is waiting.
	waiting func()
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) poll() time.Duration {
	if r.Poll > 0 {
		return r.Poll
	}
	return time.Second
}

// Fence raises the Device's fence for an assignment, waits until no older
// worker runs, and records the stop confirmation the coordinator reads. A
// DeviceRecovery is delivered this way and runs nothing else.
func (r *Runner) Fence(ctx context.Context, assignment Assignment) error {
	if err := assignment.Validate(); err != nil {
		return err
	}
	lock, err := r.acquire(ctx, assignment)
	if err != nil {
		return err
	}
	lock.release()
	return nil
}

// acquire raises the fence, waits for the run lock until the deadline, and
// confirms that older workers stopped.
func (r *Runner) acquire(ctx context.Context, assignment Assignment) (*runLock, error) {
	if err := r.Store.Raise(assignment.Device, assignment.Fence); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(ctx, assignment.Deadline)
	defer cancel()
	for {
		lock, err := r.Store.tryRunLock(assignment.Device)
		if err != nil {
			return nil, err
		}
		if lock != nil {
			if err := r.Store.ConfirmStopped(assignment.Device, assignment.Fence); err != nil {
				lock.release()
				return nil, err
			}
			return lock, nil
		}
		if r.waiting != nil {
			r.waiting()
		}
		select {
		case <-ctx.Done():
			return nil, errWorkerRunning
		case <-time.After(r.poll()):
		}
	}
}

var errWorkerRunning = errors.New("an older worker did not stop before the deadline")

// ErrAlreadyRunning means another worker already runs this same assignment.
// No result is stored; that worker reports it.
var ErrAlreadyRunning = errors.New("site: another worker runs this assignment")

// Run executes one assignment and stores its result. It returns
// ErrStaleFence, and stores nothing, for an assignment below the Device's
// stored fence.
func (r *Runner) Run(ctx context.Context, assignment Assignment) (custody.SiteResult, error) {
	if err := assignment.Validate(); err != nil {
		return custody.SiteResult{}, err
	}
	// A redelivered assignment that already has a result returns it.
	if stored, found, err := r.Results.Get(assignment.Device, assignment.Operation.UID, assignment.Fence); err != nil || found {
		return stored, err
	}
	result := custody.SiteResult{Site: r.Site, Device: deviceName(assignment.Device), Operation: assignment.Operation, Fence: assignment.Fence}
	finish := func(phase ops.Phase, reason, message string, evidence []byte) (custody.SiteResult, error) {
		result.Phase, result.Reason, result.Message, result.Evidence = phase, reason, message, evidence
		return r.Results.Put(assignment.Device, result)
	}
	// Whether anything may have been sent comes from the journal, not from
	// this process: an earlier worker for the same operation may have sent
	// steps before it was interrupted, or may still be running.
	sent := func() (bool, error) {
		journal, err := r.Store.Journal(assignment.Device)
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(journal, func(entry Entry) bool { return entry.Operation == assignment.Operation.UID }), nil
	}
	lock, err := r.acquire(ctx, assignment)
	switch {
	case err != nil && ctx.Err() != nil:
		// Stopped from outside while waiting: store nothing.
		return custody.SiteResult{}, context.Cause(ctx)
	case errors.Is(err, ErrStaleFence):
		return custody.SiteResult{}, err
	case errors.Is(err, errWorkerRunning):
		// A worker at this assignment's own fence took the run lock: it is
		// running this same operation and will report it. An older worker
		// can never take the lock once this fence is stored.
		if _, stopped, serr := r.Store.Current(assignment.Device); serr == nil && stopped == assignment.Fence {
			return custody.SiteResult{}, ErrAlreadyRunning
		}
		journaled, jerr := sent()
		if jerr != nil {
			return custody.SiteResult{}, jerr
		}
		if journaled {
			return finish(ops.PhaseOutcomeUnknown, ReasonWorkerRunning, err.Error(), nil)
		}
		return finish(ops.PhaseFailed, ReasonWorkerRunning, err.Error(), nil)
	case err != nil:
		return custody.SiteResult{}, err
	}
	defer lock.release()
	// A worker for this same assignment may have stored its result while
	// this one waited for the lock. Return that result; running the kind
	// again could send to the server after the outcome was recorded.
	if stored, found, err := r.Results.Get(assignment.Device, assignment.Operation.UID, assignment.Fence); err != nil || found {
		return stored, err
	}
	parent := ctx
	// The worker stops at its deadline exactly, and as soon as a newer fence
	// is stored for the Device.
	ctx, cancel := context.WithDeadlineCause(ctx, assignment.Deadline, errDeadline)
	defer cancel()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	go r.watch(ctx, assignment, stop)

	work := &Work{Assignment: assignment, store: r.Store}
	if work.sent, err = sent(); err != nil {
		return custody.SiteResult{}, err
	}
	end := func(reason, message string) (custody.SiteResult, error) {
		if work.sent {
			return finish(ops.PhaseOutcomeUnknown, reason, message, nil)
		}
		return finish(ops.PhaseFailed, reason, message, nil)
	}
	if !r.now().Before(assignment.Deadline) {
		return end(ReasonDeadline, errDeadline.Error())
	}
	// A kind that must start soon after admission refuses a late start, but
	// a resumed worker finishes what an earlier one began.
	if kind, ok := r.Kinds[assignment.Operation.Kind].(StartBy); ok && !work.sent && r.now().After(assignment.AdmittedAt.Add(kind.StartBy())) {
		return end(ReasonStale, "the worker started too long after admission")
	}
	if refusal := r.prepare(ctx, work); refusal != nil {
		if parent.Err() != nil {
			return custody.SiteResult{}, context.Cause(parent)
		}
		return end(refusal.Reason, refusal.Message)
	}
	evidence, err := r.Kinds[assignment.Operation.Kind].Run(ctx, work)
	// A worker stopped from outside, such as a Pod being shut down, stores
	// no result: its replacement resumes from the journal.
	if err != nil && parent.Err() != nil {
		return custody.SiteResult{}, context.Cause(parent)
	}
	if err != nil {
		var unsent *Unsent
		if errors.As(err, &unsent) {
			return finish(ops.PhaseFailed, unsent.Refusal.Reason, unsent.Refusal.Message, nil)
		}
		var refusal *custody.Refusal
		if errors.As(err, &refusal) {
			return end(refusal.Reason, refusal.Message)
		}
		// With nothing journaled for the operation, nothing was sent.
		return end(outcomeReason(ctx, err), sanitize(err))
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return finish(ops.PhaseOutcomeUnknown, ReasonStep, "the evidence could not be encoded", nil)
	}
	return finish(ops.PhaseSucceeded, "", "", data)
}

var errDeadline = errors.New("the operation's deadline passed")

// Unsent is a refusal a kind raises after it journaled steps that it knows
// took no effect anywhere: each was refused outright by the service it was
// sent to, and none reached the server. The operation ends Failed rather
// than unknown. A kind raises it only when its journal proves that.
type Unsent struct{ Refusal *custody.Refusal }

func (u *Unsent) Error() string { return u.Refusal.Reason + ": " + u.Refusal.Message }

// DeadlinePassed reports whether ctx ended because the operation's deadline
// passed. No replacement worker can resume such an operation.
func DeadlinePassed(ctx context.Context) bool { return errors.Is(context.Cause(ctx), errDeadline) }

func outcomeReason(ctx context.Context, err error) string {
	switch cause := context.Cause(ctx); {
	case errors.Is(err, errSuperseded) || errors.Is(cause, errSuperseded):
		return ReasonSuperseded
	case errors.Is(cause, errDeadline):
		return ReasonDeadline
	}
	return ReasonStep
}

// sanitize keeps a result's message free of BMC responses and endpoints.
func sanitize(err error) string {
	switch {
	case errors.Is(err, errSuperseded):
		return errSuperseded.Error()
	case errors.Is(err, errStepUnknown):
		return errStepUnknown.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errDeadline):
		return errDeadline.Error()
	}
	return "the worker stopped before it could prove the outcome"
}

// watch cancels the worker when a newer fence is stored for the Device.
func (r *Runner) watch(ctx context.Context, assignment Assignment, stop context.CancelCauseFunc) {
	ticker := time.NewTicker(r.poll())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if fence, _, err := r.Store.Current(assignment.Device); err == nil && fence > assignment.Fence {
				stop(errSuperseded)
				return
			}
		}
	}
}

// prepare runs every check the framework owns before a kind runs. Each
// refuses with a named reason and sends nothing.
func (r *Runner) prepare(ctx context.Context, work *Work) *custody.Refusal {
	assignment := work.Assignment
	kind, ok := r.Kinds[assignment.Operation.Kind]
	if !ok {
		return &custody.Refusal{Reason: ReasonUnsupportedKind, Message: "this site runs no worker for the kind"}
	}
	if sum := sha256.Sum256(assignment.Inputs); "sha256:"+hex.EncodeToString(sum[:]) != assignment.InputDigest {
		return &custody.Refusal{Reason: ReasonInputMismatch, Message: "the inputs do not match the admitted digest"}
	}
	connection, refusal := r.Access.Prepare(ctx, assignment)
	if refusal != nil {
		return refusal
	}
	work.Connection = connection
	return kind.Check(ctx, work)
}

func deviceName(device string) string {
	_, name, _ := strings.Cut(device, "/")
	return name
}
