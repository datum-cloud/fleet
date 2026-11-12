package miloacceptance

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotRun  = errors.New("Milo acceptance was not run")
	ErrBlocked = errors.New("Milo acceptance is blocked")
)

var requiredProductionCapabilities = []BlockerCode{
	BlockerSourceAuthority,
	BlockerAdmissionFence,
	BlockerQuotaCustody,
	BlockerProjectPurgeGuard,
	BlockerReplayableEvents,
	BlockerNetworkImageAuth,
	BlockerNetworkEvidence,
	BlockerLifecycleExecution,
}

type Clock func() time.Time

// Run evaluates whether live acceptance can begin. The current implementation
// intentionally has no cluster writer: production integration probes and the
// effectful scenario executor do not exist yet, so every structurally valid run
// is blocked before a kubeconfig is loaded or an API mutation is attempted.
func Run(_ context.Context, config Config, now Clock) (Result, error) {
	started := now().UTC()
	result := Result{SchemaVersion: 1, Status: StatusNotRun, StartedAt: started}
	config = config.Canonicalized()
	if err := config.Validate(); err != nil {
		result.CompletedAt = now().UTC()
		result.Phases = []Phase{{Name: "configuration", Status: StatusNotRun, Reason: ReasonConfigurationInvalid}}
		return result, errors.Join(ErrNotRun, err)
	}
	result.RunID = config.RunID
	result.Environment = environmentFor(config)
	result.Status = StatusBlocked
	result.Blockers = append([]BlockerCode(nil), requiredProductionCapabilities...)
	result.Phases = make([]Phase, 0, len(mandatoryPhases))
	for index, name := range mandatoryPhases {
		phase := Phase{Name: name, Status: StatusNotRun, Reason: ReasonProductionAccessNotAllowed}
		if index == 0 {
			phase = Phase{Name: name, Status: StatusPassed}
		} else if index == 1 {
			phase = Phase{Name: name, Status: StatusBlocked, Reason: ReasonProductionCapabilitiesGap}
		}
		result.Phases = append(result.Phases, phase)
	}
	result.CompletedAt = now().UTC()
	return result, ErrBlocked
}
