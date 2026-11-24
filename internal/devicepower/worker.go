package devicepower

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/redfish"
)

// BMC is the Redfish surface a worker uses. It is built only after the
// credential's pin is known, and every connection checks that pin.
type BMC interface {
	FindPowerSystem(ctx context.Context, uuid string) (redfish.PowerSystem, error)
	ObservePower(ctx context.Context, sys redfish.PowerSystem) (redfish.PowerState, error)
	ResetPower(ctx context.Context, sys redfish.PowerSystem, kind string) error
}

// NewBMC builds a pinned BMC client.
type NewBMC func(endpoint, user, password, pin string, timeout time.Duration) (BMC, error)

// PinnedBMC is NewBMC over redfish.NewPinnedClient.
func PinnedBMC(endpoint, user, password, pin string, timeout time.Duration) (BMC, error) {
	return redfish.NewPinnedClient(endpoint, user, password, pin, timeout)
}

// Worker reasons, in addition to the acceptance reasons in the API.
const (
	ReasonStale                 = "Stale"
	ReasonCredentialNotFound    = "CredentialNotFound"
	ReasonCredentialInvalid     = "CredentialInvalid"
	ReasonPinNotApproved        = "PinNotApproved"
	ReasonCredentialMismatch    = "CredentialMismatch"
	ReasonPinMismatch           = "PinMismatch"
	ReasonCredentialRejected    = "CredentialRejected"
	ReasonBMCUnreachable        = "BMCUnreachable"
	ReasonPowerStateChanging    = "PowerStateChanging"
	ReasonSystemOff             = "SystemOff"
	ReasonAlreadyInState        = "AlreadyInState"
	ReasonRequestedStateReached = "RequestedStateObserved"
	ReasonResetObserved         = "ResetObserved"
	ReasonCommandRefused        = "CommandRefused"
	ReasonPowerChangedAnyway    = "PowerChangedAfterRefusal"
	ReasonNotObserved           = "OutcomeNotObserved"
	ReasonIdentityChanged       = "IdentityChanged"
	ReasonWorkerLost            = "WorkerLost"
	ReasonWorkerNotStarted      = "WorkerNotStarted"
	ReasonRecordLost            = "RecordLost"
)

// Worker runs one operation from its stage record.
type Worker struct {
	// Records reads and updates exactly one stage record.
	Records client.Client
	Record  types.NamespacedName
	// PodUID is this worker's claim identity.
	PodUID string
	// CredentialDir holds the mounted credential Secret's files.
	CredentialDir string
	NewBMC        NewBMC
	Now           func() time.Time
	// Poll is the observation interval; Sleep waits for it.
	Poll  time.Duration
	Sleep func(context.Context, time.Duration)
	Log   func(format string, args ...any)
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Worker) sleep(ctx context.Context, d time.Duration) {
	if w.Sleep != nil {
		w.Sleep(ctx, d)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (w *Worker) logf(format string, args ...any) {
	if w.Log != nil {
		w.Log(format, args...)
	}
}

// ErrNotClaimed means another worker, or the dispatcher's fence, holds the
// record. This worker contacted no BMC.
var ErrNotClaimed = errors.New("the operation was already claimed; no BMC was contacted")

// run state of one worker.
type run struct {
	w   *Worker
	cm  *corev1.ConfigMap
	in  Inputs
	p   Progress
	ctx context.Context
}

// save writes progress with the record's optimistic lock. A conflict means
// the dispatcher fenced the record, or another writer changed it: the worker
// then stops without any further BMC request.
func (r *run) save(p Progress) error {
	cm := r.cm.DeepCopy()
	setProgress(cm, p)
	if err := r.w.Records.Update(r.ctx, cm); err != nil {
		return err
	}
	r.cm, r.p = cm, p
	return nil
}

// finish records the result. On a conflict it re-reads once and gives up
// if the record is no longer this worker's.
func (r *run) finish(result inventory.PowerResult, reason, message string) error {
	p := r.p
	p.Stage, p.Result, p.Reason, p.Message = StageFinished, result, reason, message
	p.FinishedAt = ptrTime(r.w.now())
	r.w.logf("result %s reason %s", result, reason)
	err := r.save(p)
	for attempt := 0; err != nil && attempt < 5; attempt++ {
		if !apierrors.IsConflict(err) {
			r.w.sleep(r.ctx, time.Second)
		} else {
			var cm corev1.ConfigMap
			if err := r.w.Records.Get(r.ctx, r.w.Record, &cm); err != nil {
				continue
			}
			_, current, decodeErr := decodeRecord(&cm)
			if decodeErr != nil || current.ClaimedBy != r.w.PodUID || current.Stage == StageAbandoned || current.Stage == StageFinished {
				return fmt.Errorf("record taken over before the result was saved: %w", err)
			}
			r.cm = &cm
		}
		err = r.save(p)
	}
	return err
}

func ptrTime(t time.Time) *metav1.Time {
	v := metav1.NewTime(t.Truncate(time.Second))
	return &v
}

// Run claims the operation, re-checks the server, sends at most one command
// and records the outcome. It returns ErrNotClaimed, without contacting a BMC,
// when the record is not Pending.
func (w *Worker) Run(ctx context.Context) error {
	var cm corev1.ConfigMap
	if err := w.Records.Get(ctx, w.Record, &cm); err != nil {
		return fmt.Errorf("read stage record: %w", err)
	}
	in, p, err := decodeRecord(&cm)
	if err != nil {
		return err
	}
	if p.Stage != StagePending || w.PodUID == "" {
		return ErrNotClaimed
	}
	r := &run{w: w, cm: &cm, in: in, p: p, ctx: ctx}
	// The claim. Of two workers for one operation, at most one wins this
	// update; the other stops here.
	if err := r.save(Progress{Stage: StageClaimed, ClaimedBy: w.PodUID, ClaimedAt: ptrTime(w.now())}); err != nil {
		if apierrors.IsConflict(err) {
			return ErrNotClaimed
		}
		return fmt.Errorf("claim the operation: %w", err)
	}
	w.logf("claimed operation %s/%s action %s", in.Namespace, in.Operation, in.Action)
	return r.execute()
}

func (r *run) execute() error {
	in, w := r.in, r.w
	if w.now().Sub(in.CapacityCheckedAt.Time) > StaleAfter {
		return r.finish(inventory.PowerFailed, ReasonStale, "the worker started more than two minutes after the dispatcher's last capacity check; nothing was sent")
	}
	user, password, pin, reason := readCredential(w.CredentialDir)
	switch {
	case reason != "":
		return r.finish(inventory.PowerFailed, reason, "the worker has no usable credential for this Device; nothing was sent")
	case pin == "":
		return r.finish(inventory.PowerFailed, ReasonPinNotApproved, "the credential has no approved TLS key; nothing was sent")
	case pin != in.PublicKeySHA256:
		return r.finish(inventory.PowerFailed, ReasonCredentialMismatch, "the credential's approved TLS key is not the key discovery observed for this Device; nothing was sent")
	}
	bmc, err := w.NewBMC(in.Endpoint, user, password, pin, in.RequestTimeout.Duration)
	if err != nil {
		return r.finish(inventory.PowerFailed, ReasonCredentialInvalid, "the credential or endpoint is not usable; nothing was sent")
	}
	sys, err := bmc.FindPowerSystem(r.ctx, in.SystemUUID)
	if err != nil {
		reason, message := refusal(err)
		return r.finish(inventory.PowerFailed, reason, message+"; nothing was sent")
	}
	action := string(in.Action)
	if !slices.Contains(sys.ResetTypes, action) {
		return r.finish(inventory.PowerFailed, inventory.ReasonResetTypeNotOffered, "the server no longer offers "+action+"; nothing was sent")
	}
	before, err := bmc.ObservePower(r.ctx, sys)
	if err != nil {
		reason, message := refusal(err)
		return r.finish(inventory.PowerFailed, reason, message+"; nothing was sent")
	}
	r.p.Before = observation(before)
	switch {
	case before.State != "On" && before.State != "Off":
		return r.finish(inventory.PowerFailed, ReasonPowerStateChanging, "the power state is "+safeState(before.State)+", not On or Off; nothing was sent")
	case (in.Action == inventory.PowerOn && before.State == "On") || (in.Action == inventory.PowerForceOff && before.State == "Off"):
		r.p.After = r.p.Before
		return r.finish(inventory.PowerSucceeded, ReasonAlreadyInState, "the server was already "+before.State+"; nothing was sent")
	case (in.Action == inventory.PowerForceRestart || in.Action == inventory.PowerCycle) && before.State == "Off":
		return r.finish(inventory.PowerFailed, ReasonSystemOff, "the server is off; a restart or power cycle is refused; nothing was sent")
	}
	// Write ahead: from this record on, the outcome is Unknown unless
	// proven. If it cannot be written, nothing is sent.
	sending := r.p
	sending.Stage, sending.RequestSentAt = StageSending, ptrTime(w.now())
	if err := r.save(sending); err != nil {
		return fmt.Errorf("record the send before sending; nothing was sent: %w", err)
	}
	outcome := sendOutcome(bmc.ResetPower(r.ctx, sys, action))
	w.logf("command %s %s", action, outcome)
	sent := r.p
	sent.Stage, sent.SendOutcome = StageSent, outcome
	if err := r.save(sent); err != nil {
		// Keep observing: the result write below is what matters.
		w.logf("record the send outcome: %v", err)
		r.p = sent
	}
	return r.observe(bmc, sys, before, outcome)
}

// observe decides the result from what the BMC shows after the command.
func (r *run) observe(bmc BMC, sys redfish.PowerSystem, before redfish.PowerState, outcome string) error {
	in, w := r.in, r.w
	if outcome == SendRefused {
		w.sleep(r.ctx, w.Poll)
		after, err := bmc.ObservePower(r.ctx, sys)
		if err != nil {
			return r.finish(inventory.PowerUnknown, ReasonNotObserved, "the BMC refused the command and the power state could not be read afterwards")
		}
		r.p.After = observation(after)
		if after.State == before.State && after.LastResetTime.Equal(before.LastResetTime) {
			return r.finish(inventory.PowerFailed, ReasonCommandRefused, "the BMC refused the command and the power did not change")
		}
		return r.finish(inventory.PowerUnknown, ReasonPowerChangedAnyway, "the BMC refused the command but the power changed")
	}
	lost := ""
	if outcome == SendLost {
		lost = " after the BMC's response was lost; the command was not resent"
	}
	deadline := r.p.RequestSentAt.Add(in.ObserveTimeout.Duration)
	for {
		after, err := bmc.ObservePower(r.ctx, sys)
		if errors.Is(err, redfish.ErrIdentity) {
			return r.finish(inventory.PowerUnknown, ReasonIdentityChanged, "the server behind the BMC changed identity after the command")
		}
		if err == nil {
			r.p.After = observation(after)
			switch in.Action {
			case inventory.PowerOn, inventory.PowerForceOff:
				if want := map[inventory.PowerAction]string{inventory.PowerOn: "On", inventory.PowerForceOff: "Off"}[in.Action]; after.State == want {
					return r.finish(inventory.PowerSucceeded, ReasonRequestedStateReached, "the server reached "+want+lost)
				}
			default:
				if after.State == "On" && !after.LastResetTime.IsZero() && after.LastResetTime.After(before.LastResetTime) {
					return r.finish(inventory.PowerSucceeded, ReasonResetObserved, "the BMC reports a reset after the command and the server is on"+lost)
				}
			}
		}
		if !w.now().Before(deadline) || r.ctx.Err() != nil {
			return r.finish(inventory.PowerUnknown, ReasonNotObserved, "the expected result was not observed within "+in.ObserveTimeout.Duration.String()+lost)
		}
		w.sleep(r.ctx, w.Poll)
	}
}

// sendOutcome classifies the one Reset's response. A definite client-side
// refusal is Refused; a 5xx, a timeout or a broken connection may have been
// applied and is Lost.
func sendOutcome(err error) string {
	var status redfish.Status
	switch {
	case err == nil:
		return SendAnswered
	case errors.Is(err, redfish.ErrUnauthorized), errors.Is(err, redfish.ErrPrecondition), errors.Is(err, redfish.ErrUnsupported):
		return SendRefused
	case errors.As(err, &status) && status.Code >= 400 && status.Code < 500:
		return SendRefused
	default:
		return SendLost
	}
}

// refusal maps an error before any command to a Failed reason.
func refusal(err error) (reason, message string) {
	switch {
	case errors.Is(err, redfish.ErrPinMismatch):
		return ReasonPinMismatch, "the BMC presented a TLS key other than the approved pin, so the credential was not sent"
	case errors.Is(err, redfish.ErrUnauthorized):
		return ReasonCredentialRejected, "the BMC rejected the credential"
	case errors.Is(err, redfish.ErrIdentity):
		return inventory.ReasonSystemUUIDMismatch, "the BMC does not report exactly one server with the expected UUID"
	case errors.Is(err, redfish.ErrUnsupported):
		return inventory.ReasonResetTypeNotOffered, "the server offers no reset action on itself"
	default:
		return ReasonBMCUnreachable, "the BMC could not be read"
	}
}

func observation(p redfish.PowerState) *inventory.PowerObservation {
	return &inventory.PowerObservation{State: safeState(p.State), ObservedAt: metav1.NewTime(p.ObservedAt.Truncate(time.Second))}
}

// safeState keeps a reported power state short and printable.
func safeState(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, s)
	if len(s) > 63 {
		s = s[:63]
	}
	if s == "" {
		return "Unreported"
	}
	return s
}

// readCredential reads the mounted credential Secret. A missing Secret has
// no files, because the volume is optional.
func readCredential(dir string) (user, password, pin, reason string) {
	read := func(key string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(dir, key))
		return strings.TrimSpace(string(b)), err == nil
	}
	user, okUser := read("username")
	password, okPassword := read("password")
	if !okUser && !okPassword {
		return "", "", "", ReasonCredentialNotFound
	}
	if user == "" || password == "" || strings.ContainsAny(user, ":\r\n") || strings.ContainsAny(password, "\r\n") {
		return "", "", "", ReasonCredentialInvalid
	}
	if raw, ok := read("tlsPublicKeySHA256"); ok && raw != "" {
		parsed, err := redfish.ParsePin(raw)
		if err != nil {
			return "", "", "", ReasonCredentialInvalid
		}
		pin = parsed
	}
	return user, password, pin, ""
}
