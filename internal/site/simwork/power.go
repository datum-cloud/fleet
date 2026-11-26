package simwork

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

// ResetTypes are the power actions the simulated server offers.
var ResetTypes = []string{string(ops.PowerOn), string(ops.PowerForceOff), string(ops.PowerForceRestart), string(ops.PowerCycle)}

// Power is the simulator's DevicePowerOperation worker. Like the Redfish
// worker, it reads the server's power before it sends anything, sends at
// most one reset through the journal, and reports Succeeded only for the
// state it then reads.
type Power struct{}

var _ site.Kind = Power{}

// State is a simulated server's power: Off until something powers it on.
func State(server Server) ops.ObservedPowerState {
	if server.Power == string(ops.PowerStateOn) {
		return ops.PowerStateOn
	}
	return ops.PowerStateOff
}

func powerSpec(work *site.Work) (ops.DevicePowerOperationSpec, *custody.Refusal) {
	var spec ops.DevicePowerOperationSpec
	if err := json.Unmarshal(work.Inputs, &spec); err != nil || !slices.Contains(ops.PowerActions, spec.Action) {
		return spec, &custody.Refusal{Reason: site.ReasonInputMismatch, Message: "the admitted inputs are not a power operation"}
	}
	return spec, nil
}

func (Power) Check(_ context.Context, work *site.Work) *custody.Refusal {
	if connection(work) == nil {
		return &custody.Refusal{Reason: site.ReasonUnsupportedKind, Message: "this site does not run the simulator"}
	}
	_, refusal := powerSpec(work)
	return refusal
}

func (Power) Run(ctx context.Context, work *site.Work) (any, error) {
	spec, refusal := powerSpec(work)
	if refusal != nil {
		return nil, refusal
	}
	server, err := read(work)
	if err != nil {
		return nil, err
	}
	before := ops.PowerObservation{State: State(server), ObservedAt: metav1.NewTime(time.Now().UTC())}
	sentAt, journaled := work.JournaledAt("reset")
	switch {
	case journaled:
	case spec.Action == ops.PowerOn && before.State == ops.PowerStateOn, spec.Action == ops.PowerForceOff && before.State == ops.PowerStateOff:
		// Already in the requested state: nothing is sent.
		return ops.PowerEvidence{Power: &ops.PowerObservations{Before: &before, After: &before}}, nil
	case spec.Action.Resets() && before.State == ops.PowerStateOff:
		return nil, &custody.Refusal{Reason: custody.ReasonNotPoweredOn, Message: "a restart needs a server that is on"}
	}
	want := ops.PowerStateOn
	if spec.Action == ops.PowerForceOff {
		want = ops.PowerStateOff
	}
	// The server record keeps times to the second, so the send time does too.
	sent := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	if journaled {
		// A resumed run reports the command an earlier run sent.
		sent = metav1.NewTime(sentAt.UTC().Truncate(time.Second))
	}
	// A restart or power cycle leaves a server on, so only its reset time
	// moving to the command proves it.
	proven := func(server Server) bool {
		if State(server) != want {
			return false
		}
		return !spec.Action.Resets() || (server.LastReset != nil && !server.LastReset.Before(&sent))
	}
	if err := work.Step(ctx, site.Step{
		Name: "reset",
		Send: func(context.Context) error {
			server, err := read(work)
			if err != nil || server.Unresponsive {
				return err
			}
			server.Power = string(want)
			if spec.Action != ops.PowerForceOff {
				server.LastReset = &sent
			}
			return write(work, server)
		},
		Observe: func(context.Context, json.RawMessage) (bool, error) {
			server, err := read(work)
			return proven(server), err
		},
	}); err != nil {
		return nil, err
	}
	server, err = read(work)
	if err != nil {
		return nil, err
	}
	if !proven(server) {
		return nil, &custody.Refusal{Reason: site.ReasonStep, Message: "the simulated server did not reach the requested power state"}
	}
	after := ops.PowerObservation{State: want, ObservedAt: metav1.NewTime(time.Now().UTC())}
	evidence := ops.PowerEvidence{RequestSentAt: &sent, Power: &ops.PowerObservations{Before: &before, After: &after}}
	if spec.Action.Resets() {
		evidence.ResetEvidence = ops.ResetLastResetTime
	}
	return evidence, nil
}

// Observe is the simulator site's fixture discovery of power: each Device
// reports its simulated server's power and offers every power action.
func Observe(store *site.Store) func(device string) (string, []string, error) {
	return func(device string) (string, []string, error) {
		server, err := ReadServer(store, device)
		return string(State(server)), slices.Clone(ResetTypes), err
	}
}
