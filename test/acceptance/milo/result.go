package miloacceptance

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

type Status string

// ReasonCode is the complete evidence-safe vocabulary for phase outcomes.
// Cluster responses, object dumps, and error strings have no representation in
// the evidence schema. Adding a reason therefore requires a reviewed code
// change rather than copying external text into an artifact.
type ReasonCode string

// BlockerCode is the complete evidence-safe vocabulary for unmet production
// capabilities. Detailed dependency diagnostics remain outside acceptance
// evidence until a separately reviewed structured schema exists.
type BlockerCode string

const (
	StatusPassed  Status = "Passed"
	StatusFailed  Status = "Failed"
	StatusBlocked Status = "Blocked"
	StatusNotRun  Status = "NotRun"

	ReasonConfigurationInvalid       ReasonCode = "ConfigurationInvalid"
	ReasonProductionAccessNotAllowed ReasonCode = "ProductionAccessNotAllowed"
	ReasonProductionCapabilitiesGap  ReasonCode = "ProductionCapabilitiesGap"
	ReasonLiveAssertionFailed        ReasonCode = "LiveAssertionFailed"

	BlockerSourceAuthority    BlockerCode = "SourceAuthorityUnavailable"
	BlockerAdmissionFence     BlockerCode = "AdmissionFenceUnavailable"
	BlockerQuotaCustody       BlockerCode = "QuotaCustodyUnavailable"
	BlockerProjectPurgeGuard  BlockerCode = "ProjectPurgeGuardUnavailable"
	BlockerReplayableEvents   BlockerCode = "ReplayableEventsUnavailable"
	BlockerNetworkImageAuth   BlockerCode = "NetworkImageAuthorityUnavailable"
	BlockerNetworkEvidence    BlockerCode = "NetworkEvidenceUnavailable"
	BlockerLifecycleExecution BlockerCode = "LifecycleExecutionUnavailable"
)

var mandatoryPhases = [...]string{
	"configuration",
	"production-protocol-preflight",
	"tenancy-isolation",
	"competing-allocation",
	"network-handoff",
	"installation-recovery",
	"release-and-handoff",
	"cleanup-and-evidence",
}

type Phase struct {
	Name   string     `json:"name"`
	Status Status     `json:"status"`
	Reason ReasonCode `json:"reason,omitempty"`
}

type Environment struct {
	GitRevision  string   `json:"gitRevision,omitempty"`
	MiloRevision string   `json:"miloRevision,omitempty"`
	AuthorityID  string   `json:"authorityID,omitempty"`
	Projects     Projects `json:"projects,omitempty"`
	Images       Images   `json:"images,omitempty"`
}

// Result is deliberately allow-listed. Kubeconfig paths, credentials, Secret
// bodies and controller logs have no representation in the evidence schema.
type Result struct {
	SchemaVersion int           `json:"schemaVersion"`
	RunID         string        `json:"runID,omitempty"`
	Status        Status        `json:"status"`
	StartedAt     time.Time     `json:"startedAt"`
	CompletedAt   time.Time     `json:"completedAt"`
	Environment   Environment   `json:"environment"`
	Phases        []Phase       `json:"phases"`
	Blockers      []BlockerCode `json:"blockers,omitempty"`
}

func environmentFor(c Config) Environment {
	return Environment{GitRevision: c.GitRevision, MiloRevision: c.MiloRevision, AuthorityID: c.AuthorityID, Projects: c.Projects, Images: c.Images}
}

func (r Result) Validate() error {
	var problems []error
	if r.SchemaVersion != 1 {
		problems = append(problems, errors.New("unsupported result schemaVersion"))
	}
	if !validStatus(r.Status) {
		problems = append(problems, errors.New("result status is invalid"))
	}
	if !validEvidenceTime(r.StartedAt) || !validEvidenceTime(r.CompletedAt) || r.CompletedAt.Before(r.StartedAt) {
		problems = append(problems, errors.New("result times must be nonzero UTC and ordered"))
	}
	if len(r.Phases) == 0 || len(r.Phases) > len(mandatoryPhases) {
		problems = append(problems, errors.New("result phases must be a nonempty prefix of the mandatory phase set"))
	}
	terminal := -1
	for index, phase := range r.Phases {
		if index >= len(mandatoryPhases) || phase.Name != mandatoryPhases[index] {
			problems = append(problems, errors.New("result phases are unknown, duplicated, or out of order"))
			break
		}
		if !validStatus(phase.Status) {
			problems = append(problems, fmt.Errorf("phase %s has invalid status", phase.Name))
		}
		if phase.Status != StatusPassed {
			if !reasonAllowed(phase.Status, phase.Reason) {
				problems = append(problems, fmt.Errorf("phase %s has an unreviewed reason code", phase.Name))
			}
		} else if phase.Reason != "" {
			problems = append(problems, fmt.Errorf("passed phase %s must not include a reason", phase.Name))
		}
		if phase.Status == StatusBlocked || phase.Status == StatusFailed {
			if terminal != -1 {
				problems = append(problems, errors.New("result phases contain multiple terminal outcomes"))
			}
			terminal = index
		} else if phase.Status == StatusPassed && terminal != -1 {
			problems = append(problems, errors.New("passed phases must form a prefix"))
		} else if phase.Status == StatusNotRun && terminal == -1 && r.Status != StatusNotRun {
			problems = append(problems, errors.New("not-run phases must form a suffix after the terminal outcome"))
		} else if phase.Status == StatusNotRun && r.Status != StatusNotRun && phase.Reason != ReasonProductionAccessNotAllowed {
			problems = append(problems, errors.New("not-run suffix phases require the reviewed production-access reason"))
		}
	}
	switch r.Status {
	case StatusPassed:
		if !runID.MatchString(r.RunID) || environmentIdentityValid(r.Environment) != nil {
			problems = append(problems, errors.New("passed result requires valid exact environment identity"))
		}
		if len(r.Phases) != len(mandatoryPhases) || terminal != -1 || hasPhaseStatus(r.Phases, StatusNotRun) || len(r.Blockers) != 0 {
			problems = append(problems, errors.New("passed result requires every exact mandatory phase passed and no blockers"))
		}
	case StatusBlocked:
		if !runID.MatchString(r.RunID) || environmentIdentityValid(r.Environment) != nil {
			problems = append(problems, errors.New("blocked result requires valid exact environment identity"))
		}
		if len(r.Phases) != len(mandatoryPhases) || len(r.Blockers) == 0 || terminal < 0 || r.Phases[terminal].Status != StatusBlocked || r.Phases[terminal].Reason != ReasonProductionCapabilitiesGap {
			problems = append(problems, errors.New("blocked result requires blockers and a blocked phase"))
		}
	case StatusNotRun:
		if r.RunID != "" || !environmentEmpty(r.Environment) || len(r.Blockers) != 0 || len(r.Phases) != 1 || r.Phases[0] != (Phase{Name: "configuration", Status: StatusNotRun, Reason: ReasonConfigurationInvalid}) {
			problems = append(problems, errors.New("not-run result must contain no caller identity or blockers"))
		}
	case StatusFailed:
		if len(r.Phases) != len(mandatoryPhases) || !runID.MatchString(r.RunID) || environmentIdentityValid(r.Environment) != nil || terminal < 0 || r.Phases[terminal].Status != StatusFailed || r.Phases[terminal].Reason != ReasonLiveAssertionFailed {
			problems = append(problems, errors.New("failed result requires exact environment identity and a failed phase"))
		}
	}
	if r.Status != StatusBlocked && len(r.Blockers) != 0 {
		problems = append(problems, errors.New("only a blocked result may contain blockers"))
	}
	seenBlockers := map[BlockerCode]struct{}{}
	for _, blocker := range r.Blockers {
		if !validBlocker(blocker) {
			problems = append(problems, errors.New("result contains an unreviewed blocker code"))
		}
		if _, exists := seenBlockers[blocker]; exists {
			problems = append(problems, errors.New("result contains a duplicate blocker code"))
		}
		seenBlockers[blocker] = struct{}{}
	}
	return errors.Join(problems...)
}

func reasonAllowed(status Status, reason ReasonCode) bool {
	switch status {
	case StatusNotRun:
		return reason == ReasonConfigurationInvalid || reason == ReasonProductionAccessNotAllowed
	case StatusBlocked:
		return reason == ReasonProductionCapabilitiesGap
	case StatusFailed:
		return reason == ReasonLiveAssertionFailed
	default:
		return false
	}
}

func validBlocker(blocker BlockerCode) bool {
	switch blocker {
	case BlockerSourceAuthority, BlockerAdmissionFence, BlockerQuotaCustody,
		BlockerProjectPurgeGuard, BlockerReplayableEvents, BlockerNetworkImageAuth,
		BlockerNetworkEvidence, BlockerLifecycleExecution:
		return true
	default:
		return false
	}
}

// ValidateOutcome binds the serialized status to the returned execution error.
// Callers must validate this pair before selecting an exit code or publishing
// evidence; validating either half independently is insufficient.
func ValidateOutcome(result Result, outcomeErr error) error {
	if err := result.Validate(); err != nil {
		return err
	}
	blocked := errors.Is(outcomeErr, ErrBlocked)
	notRun := errors.Is(outcomeErr, ErrNotRun)
	if blocked && notRun {
		return errors.New("outcome error cannot carry multiple status sentinels")
	}
	consistent := false
	switch result.Status {
	case StatusPassed:
		consistent = outcomeErr == nil
	case StatusBlocked:
		consistent = outcomeErr != nil && blocked
	case StatusNotRun:
		consistent = outcomeErr != nil && notRun
	case StatusFailed:
		consistent = outcomeErr != nil && !blocked && !notRun
	}
	if !consistent {
		return fmt.Errorf("result status %s is inconsistent with execution error", result.Status)
	}
	return nil
}

func validStatus(status Status) bool {
	return status == StatusPassed || status == StatusFailed || status == StatusBlocked || status == StatusNotRun
}

func validEvidenceTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0
}

func hasPhaseStatus(phases []Phase, status Status) bool {
	for _, phase := range phases {
		if phase.Status == status {
			return true
		}
	}
	return false
}

func environmentIdentityValid(environment Environment) error {
	var problems []error
	if !hexRevision.MatchString(environment.GitRevision) || !hexRevision.MatchString(environment.MiloRevision) {
		problems = append(problems, errors.New("environment revisions are invalid"))
	}
	if errs := validation.IsDNS1123Subdomain(environment.AuthorityID); len(errs) != 0 {
		problems = append(problems, errors.New("environment authority is invalid"))
	}
	projects := []Project{environment.Projects.Provider, environment.Projects.ConsumerA, environment.Projects.ConsumerB}
	seenNames, seenUIDs := map[string]struct{}{}, map[string]struct{}{}
	for _, project := range projects {
		if errs := validation.IsDNS1123Subdomain(project.Name); len(errs) != 0 || !projectUID.MatchString(project.UID) {
			problems = append(problems, errors.New("environment Project identity is invalid"))
		}
		if _, exists := seenNames[project.Name]; exists {
			problems = append(problems, errors.New("environment Project names are not distinct"))
		}
		if _, exists := seenUIDs[project.UID]; exists {
			problems = append(problems, errors.New("environment Project UIDs are not distinct"))
		}
		seenNames[project.Name], seenUIDs[project.UID] = struct{}{}, struct{}{}
	}
	if !imageDigest.MatchString(environment.Images.Manager) || !imageDigest.MatchString(environment.Images.Simulator) {
		problems = append(problems, errors.New("environment image digests are invalid"))
	}
	return errors.Join(problems...)
}

func environmentEmpty(environment Environment) bool {
	return environment == (Environment{})
}
