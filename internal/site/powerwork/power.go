// Package powerwork runs DevicePowerOperation over Redfish behind the site
// worker contract, as proposed in datum-cloud/fleet#173. A worker reads the
// server's power before it sends anything, sends at most one
// ComputerSystem.Reset, and reports Succeeded only for what it then
// observes. The framework owns the fence, journal, credential, pin, identity
// and deadline.
package powerwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The worker's refusal reasons, declared on the DevicePowerOperation kind.
const (
	ReasonNotPoweredOn         = custody.ReasonNotPoweredOn
	ReasonPowerStateNotSettled = custody.ReasonPowerStateNotSettled
	ReasonSystemUUIDMismatch   = custody.ReasonSystemUUIDMismatch
	ReasonResetTypeNotOffered  = custody.ReasonResetTypeNotOffered
)

// resetStep names the one journaled effect.
const resetStep = "reset"

// Defaults, from #173: the worker must start within two minutes of
// admission, and the outcome must be observed within three minutes of the
// command.
const (
	DefaultStartBy        = 2 * time.Minute
	DefaultObserveTimeout = 3 * time.Minute
	defaultPoll           = 2 * time.Second
	defaultOutage         = time.Minute
)

// Power is the DevicePowerOperation worker.
type Power struct {
	// Poll is how often the worker reads the server while it waits. It
	// defaults to two seconds.
	Poll time.Duration
	// ObserveTimeout bounds how long after the command the requested state
	// may take to appear. It defaults to three minutes.
	ObserveTimeout time.Duration
	// Outage bounds how long the BMC may be unreadable while the worker
	// waits. It defaults to one minute.
	Outage time.Duration
	Now    func() time.Time
}

var (
	_ site.Kind    = Power{}
	_ site.StartBy = Power{}
)

func (p Power) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func positive(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// StartBy is the #173 start window. A worker that starts later, with
// nothing sent, refuses as Stale.
func (Power) StartBy() time.Duration { return DefaultStartBy }

// payload is what the reset step journals before it is sent: when the one
// command was sent, the power read before it, and the BMC's reset-time
// baseline that proves a restart.
type payload struct {
	RequestSentAt time.Time       `json:"requestSentAt"`
	Before        observation     `json:"before"`
	LastResetTime time.Time       `json:"lastResetTime,omitzero"`
	Action        ops.PowerAction `json:"action"`
}

type observation struct {
	State      ops.ObservedPowerState `json:"state"`
	ObservedAt time.Time              `json:"observedAt"`
}

func connection(work *site.Work) *site.RedfishConnection {
	connection, _ := work.Connection.(*site.RedfishConnection)
	return connection
}

func spec(work *site.Work) (ops.DevicePowerOperationSpec, error) {
	var spec ops.DevicePowerOperationSpec
	err := json.Unmarshal(work.Inputs, &spec)
	return spec, err
}

func refuse(reason, format string, args ...any) *custody.Refusal {
	return &custody.Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Check runs after the framework verified the credential, the pin and the
// System's identity. It re-checks the power inputs against what the BMC
// offers now and changes nothing.
func (Power) Check(_ context.Context, work *site.Work) *custody.Refusal {
	conn := connection(work)
	if conn == nil {
		return refuse(site.ReasonUnsupportedKind, "this site does not reach the server over Redfish")
	}
	spec, err := spec(work)
	if err != nil || !slices.Contains(ops.PowerActions, spec.Action) {
		return refuse(site.ReasonInputMismatch, "the admitted inputs are not a power operation")
	}
	if spec.ExpectedSystemUUID != "" && !strings.EqualFold(spec.ExpectedSystemUUID, conn.System.UUID) {
		return refuse(ReasonSystemUUIDMismatch, "the BMC reports a system other than the one staff expected")
	}
	if !slices.Contains(conn.System.ResetTypes, string(spec.Action)) {
		return refuse(ReasonResetTypeNotOffered, "the BMC does not offer %s", spec.Action)
	}
	return nil
}

// Run reads the server's power, sends at most one reset, and returns the
// typed evidence of what it then observed. A resumed worker reads the
// journaled payload instead of reading the server's state before again, and
// observes instead of sending again.
func (p Power) Run(ctx context.Context, work *site.Work) (any, error) {
	conn := connection(work)
	spec, err := spec(work)
	if err != nil {
		return nil, refuse(site.ReasonInputMismatch, "the admitted inputs are not a power operation")
	}
	action := spec.Action
	journaled, found, err := journaledPayload(work)
	if err != nil {
		return nil, err
	}
	var sent payload
	if found {
		sent = journaled
	} else {
		before, err := conn.Observe(ctx)
		if errors.Is(err, redfish.ErrIdentity) {
			return nil, refuse(ReasonSystemUUIDMismatch, "the BMC reports another system")
		}
		if err != nil {
			return nil, err
		}
		read := observation{State: ops.ObservedPowerState(before.State), ObservedAt: p.now()}
		switch {
		case !read.State.Settled():
			// The BMC's string is never copied into a result.
			return nil, refuse(ReasonPowerStateNotSettled, "the server reports a power state other than On or Off")
		case action == ops.PowerOn && read.State == ops.PowerStateOn, action == ops.PowerForceOff && read.State == ops.PowerStateOff:
			// Already in the requested state: nothing is sent.
			return evidence(nil, read, read, ""), nil
		case action.Resets() && read.State == ops.PowerStateOff:
			return nil, refuse(ReasonNotPoweredOn, "a %s needs a server that is on", action)
		}
		sent = payload{RequestSentAt: p.now(), Before: read, LastResetTime: before.LastResetTime, Action: action}
	}
	var result *outcome
	if err := work.Step(ctx, site.Step{
		Name:    resetStep,
		Payload: sent,
		Send:    func(ctx context.Context) error { return send(ctx, conn, action) },
		// After a crash or a lost response, the reset is never sent again:
		// the worker watches for its effect instead.
		Observe: func(ctx context.Context, _ json.RawMessage) (bool, error) {
			found, err := p.watch(ctx, conn, sent)
			result = found
			return found != nil, err
		},
	}); err != nil {
		return nil, err
	}
	if result == nil {
		if result, err = p.watch(ctx, conn, sent); err != nil {
			return nil, err
		}
	}
	if result == nil {
		return nil, refuse(site.ReasonStep, "the requested power result was not observed within %s of the command", positive(p.ObserveTimeout, DefaultObserveTimeout))
	}
	sentAt := sent.RequestSentAt
	return evidence(&sentAt, sent.Before, result.after, result.proof), nil
}

// journaledPayload returns the reset step's payload if an earlier worker for
// this admission journaled it. The journal is keyed by the operation and the
// fence it was admitted at.
func journaledPayload(work *site.Work) (payload, bool, error) {
	journal, err := work.Store().Journal(work.Device)
	if err != nil {
		return payload{}, false, err
	}
	for _, entry := range journal {
		if entry.Operation != work.Key() || entry.Step != resetStep {
			continue
		}
		var recorded payload
		if err := json.Unmarshal(entry.Payload, &recorded); err != nil {
			return payload{}, false, fmt.Errorf("the journaled reset payload is unreadable: %w", err)
		}
		return recorded, true, nil
	}
	return payload{}, false, nil
}

// send posts the one reset. A response the BMC returned refusing it is an
// error. A lost response is not: the command may have taken effect, so the
// worker watches for it instead.
func send(ctx context.Context, conn *site.RedfishConnection, action ops.PowerAction) error {
	err := conn.Reset(ctx, string(action))
	var status redfish.Status
	switch {
	case err == nil:
		return nil
	case errors.As(err, &status), errors.Is(err, redfish.ErrUnauthorized), errors.Is(err, redfish.ErrUnsupported),
		errors.Is(err, redfish.ErrIdentity), errors.Is(err, redfish.ErrPinMismatch):
		return err
	case ctx.Err() != nil:
		return context.Cause(ctx)
	}
	return nil
}

// outcome is a proven result: the power read at the end, and for a restart
// or power cycle, how the reset was proven.
type outcome struct {
	after observation
	proof ops.ResetEvidence
}

// watch reads the server until the requested result appears or the
// observation window after the command closes, and returns nil then. For On
// and ForceOff the requested state must appear. A restart or power cycle is
// proven only by the BMC's reset time moving past the journaled baseline, or
// by seeing the server off, and then the server must be on again. Without a
// baseline, a reset time proves nothing: it may be a stale one the BMC hid
// before the command.
func (p Power) watch(ctx context.Context, conn *site.RedfishConnection, sent payload) (*outcome, error) {
	poll := positive(p.Poll, defaultPoll)
	closes := sent.RequestSentAt.Add(positive(p.ObserveTimeout, DefaultObserveTimeout))
	outage := positive(p.Outage, defaultOutage)
	var failingSince time.Time
	seenOff := false
	for {
		power, err := conn.Observe(ctx)
		switch {
		case errors.Is(err, redfish.ErrIdentity):
			return nil, refuse(ReasonSystemUUIDMismatch, "the BMC began reporting another system")
		case err != nil:
			if ctx.Err() != nil {
				return nil, context.Cause(ctx)
			}
			if failingSince.IsZero() {
				failingSince = p.now()
			}
			if p.now().Sub(failingSince) > outage {
				return nil, fmt.Errorf("the BMC was unreadable while the power result was observed: %w", err)
			}
		default:
			failingSince = time.Time{}
			read := observation{State: ops.ObservedPowerState(power.State), ObservedAt: p.now()}
			switch sent.Action {
			case ops.PowerOn, ops.PowerForceOff:
				if (sent.Action == ops.PowerOn) == (read.State == ops.PowerStateOn) && read.State.Settled() {
					return &outcome{after: read}, nil
				}
			default:
				if read.State == ops.PowerStateOff {
					seenOff = true
				}
				moved := !sent.LastResetTime.IsZero() && power.LastResetTime.After(sent.LastResetTime)
				if (moved || seenOff) && read.State == ops.PowerStateOn {
					proof := ops.ResetObservedOff
					if moved {
						proof = ops.ResetLastResetTime
					}
					return &outcome{after: read, proof: proof}, nil
				}
			}
		}
		if !p.now().Before(closes) {
			return nil, nil
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
	}
}

func evidence(sentAt *time.Time, before, after observation, proof ops.ResetEvidence) ops.PowerEvidence {
	out := ops.PowerEvidence{
		Power:         &ops.PowerObservations{Before: observed(before), After: observed(after)},
		ResetEvidence: proof,
	}
	if sentAt != nil {
		at := metav1.NewTime(*sentAt)
		out.RequestSentAt = &at
	}
	return out
}

func observed(value observation) *ops.PowerObservation {
	return &ops.PowerObservation{State: value.State, ObservedAt: metav1.NewTime(value.ObservedAt)}
}
