package miloacceptance

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

type Status string

const (
	StatusPassed  Status = "Passed"
	StatusFailed  Status = "Failed"
	StatusBlocked Status = "Blocked"
	StatusNotRun  Status = "NotRun"
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
	Name   string `json:"name"`
	Status Status `json:"status"`
	Reason string `json:"reason,omitempty"`
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
	SchemaVersion int         `json:"schemaVersion"`
	RunID         string      `json:"runID,omitempty"`
	Status        Status      `json:"status"`
	StartedAt     time.Time   `json:"startedAt"`
	CompletedAt   time.Time   `json:"completedAt"`
	Environment   Environment `json:"environment"`
	Phases        []Phase     `json:"phases"`
	Blockers      []string    `json:"blockers,omitempty"`
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
	nonPassed := false
	for index, phase := range r.Phases {
		if index >= len(mandatoryPhases) || phase.Name != mandatoryPhases[index] {
			problems = append(problems, errors.New("result phases are unknown, duplicated, or out of order"))
			break
		}
		if !validStatus(phase.Status) {
			problems = append(problems, fmt.Errorf("phase %s has invalid status", phase.Name))
		}
		if phase.Status != StatusPassed {
			nonPassed = true
			if strings.TrimSpace(phase.Reason) == "" || len(phase.Reason) > 512 || strings.ContainsAny(phase.Reason, "\r\n") {
				problems = append(problems, fmt.Errorf("phase %s needs a bounded single-line reason", phase.Name))
			}
		} else if phase.Reason != "" {
			problems = append(problems, fmt.Errorf("passed phase %s must not include a reason", phase.Name))
		}
	}
	switch r.Status {
	case StatusPassed:
		if !runID.MatchString(r.RunID) || environmentIdentityValid(r.Environment) != nil {
			problems = append(problems, errors.New("passed result requires valid exact environment identity"))
		}
		if len(r.Phases) != len(mandatoryPhases) || nonPassed || len(r.Blockers) != 0 {
			problems = append(problems, errors.New("passed result requires every exact mandatory phase passed and no blockers"))
		}
	case StatusBlocked:
		if !runID.MatchString(r.RunID) || environmentIdentityValid(r.Environment) != nil {
			problems = append(problems, errors.New("blocked result requires valid exact environment identity"))
		}
		if len(r.Blockers) == 0 || !hasPhaseStatus(r.Phases, StatusBlocked) {
			problems = append(problems, errors.New("blocked result requires blockers and a blocked phase"))
		}
	case StatusNotRun:
		if r.RunID != "" || !environmentEmpty(r.Environment) || len(r.Blockers) != 0 || !hasPhaseStatus(r.Phases, StatusNotRun) {
			problems = append(problems, errors.New("not-run result must contain no caller identity or blockers"))
		}
	case StatusFailed:
		if !runID.MatchString(r.RunID) || environmentIdentityValid(r.Environment) != nil || !hasPhaseStatus(r.Phases, StatusFailed) {
			problems = append(problems, errors.New("failed result requires exact environment identity and a failed phase"))
		}
	}
	for _, blocker := range r.Blockers {
		if strings.TrimSpace(blocker) == "" || len(blocker) > 256 || strings.ContainsAny(blocker, "\r\n") {
			problems = append(problems, errors.New("blockers must be bounded single-line values"))
		}
	}
	return errors.Join(problems...)
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
