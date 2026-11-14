package miloacceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var requiredRepositoryFiles = []string{
	"Taskfile.yaml",
	".github/milo-acceptance-policy.json",
	".github/workflows/ci.yml",
	".github/workflows/milo-acceptance.yml",
	"test/acceptance/milo/README.md",
	"test/acceptance/milo/github-environment.md",
	"test/acceptance/milo/config.example.json",
	"test/acceptance/coverage.md",
}

type githubAcceptancePolicy struct {
	Environment           string   `json:"environment"`
	TrustedRef            string   `json:"trustedRef"`
	RequiredReviewers     []string `json:"requiredReviewers"`
	PreventSelfReview     bool     `json:"preventSelfReview"`
	RunnerGroup           string   `json:"runnerGroup"`
	RestrictedToWorkflows bool     `json:"restrictedToWorkflows"`
	SelectedWorkflows     []string `json:"selectedWorkflows"`
	RunnerLabels          []string `json:"runnerLabels"`
	SelectedRepositories  []string `json:"selectedRepositories"`
}

type workflowDocument struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Needs       string         `yaml:"needs"`
	If          string         `yaml:"if"`
	Environment string         `yaml:"environment"`
	RunsOn      workflowRunsOn `yaml:"runs-on"`
	Steps       []workflowStep `yaml:"steps"`
}

type workflowRunsOn struct {
	Scalar string
	Group  string
	Labels []string
}

func (r *workflowRunsOn) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		r.Scalar = node.Value
		return nil
	case yaml.MappingNode:
		var value struct {
			Group  string   `yaml:"group"`
			Labels []string `yaml:"labels"`
		}
		if err := node.Decode(&value); err != nil {
			return err
		}
		r.Group, r.Labels = value.Group, value.Labels
		return nil
	default:
		return fmt.Errorf("unsupported runs-on shape")
	}
}

type workflowStep struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

// ValidateRepository checks the non-effectful acceptance packaging. It does not
// contact a cluster and is safe to run in pull-request CI.
func ValidateRepository(root string) error {
	var problems []error
	for _, name := range requiredRepositoryFiles {
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || !info.Mode().IsRegular() {
			problems = append(problems, fmt.Errorf("required regular file %s is missing", name))
		}
	}
	policy, policyErr := loadGitHubAcceptancePolicy(filepath.Join(root, ".github/milo-acceptance-policy.json"))
	if policyErr != nil {
		problems = append(problems, policyErr)
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/milo-acceptance.yml"))
	if err == nil {
		text := string(workflow)
		if policyErr == nil {
			if err := validateWorkflowTrustBoundary(workflow, policy); err != nil {
				problems = append(problems, err)
			}
		}
		if !strings.Contains(text, "workflow_dispatch:") || !strings.Contains(text, "environment: milo-acceptance") {
			problems = append(problems, errors.New("Milo workflow must be manually dispatched behind the milo-acceptance environment"))
		}
		for _, automatic := range []string{"pull_request:", "schedule:", "push:"} {
			if strings.Contains(text, automatic) {
				problems = append(problems, fmt.Errorf("Milo workflow must not contain automatic trigger %s", automatic))
			}
		}
		for _, forbidden := range []string{"--allow-local-", "kubectl patch", "subresource=status", "remove-finalizer"} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				problems = append(problems, fmt.Errorf("Milo workflow contains forbidden acceptance shortcut %q", forbidden))
			}
		}
		pins := []string{
			"actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6",
			"actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6",
			"actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6",
		}
		for _, pin := range pins {
			if !strings.Contains(text, pin) {
				problems = append(problems, fmt.Errorf("Milo workflow is missing reviewed action pin %s", pin))
			}
		}
		usesPin := regexp.MustCompile(`(?m)^\s*- uses: [^@\s]+@[a-f0-9]{40}(?:\s+#\s+v[0-9]+)?\s*$`)
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "- uses:") && !usesPin.MatchString(line) {
				problems = append(problems, errors.New("every workflow action must use a full reviewed commit pin"))
			}
		}
		liveIndex := strings.Index(text, "- name: Run protected live preflight")
		if liveIndex < 0 || strings.Contains(text[:max(liveIndex, 0)], "MILO_ACCEPTANCE_CONFIG") {
			problems = append(problems, errors.New("Milo acceptance config may be exposed only to the live step"))
		}
		if !strings.Contains(text, "fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}") || !strings.Contains(text, `test ! -e "$MILO_ACCEPTANCE_OUTPUT"`) {
			problems = append(problems, errors.New("Milo workflow must use an absent run-unique evidence directory"))
		}
		if !strings.Contains(text, "verify-evidence --output") || !strings.Contains(text, `echo "ready=true" >> "$GITHUB_OUTPUT"`) || !strings.Contains(text, "if: always() && steps.live.outputs.ready == 'true'") {
			problems = append(problems, errors.New("Milo workflow may upload only a freshly verified evidence snapshot"))
		}
		for _, artifact := range []string{"/summary.json", "/environment.json", "/junit.xml"} {
			if !strings.Contains(text, artifact) {
				problems = append(problems, fmt.Errorf("workflow upload is missing exact artifact %s", artifact))
			}
		}
	}
	ciWorkflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err == nil {
		if err := validateCIWorkflow(ciWorkflow); err != nil {
			problems = append(problems, err)
		}
	}
	example, err := os.Open(filepath.Join(root, "test/acceptance/milo/config.example.json"))
	if err == nil {
		defer example.Close()
		decoder := json.NewDecoder(example)
		decoder.DisallowUnknownFields()
		var config Config
		if err := decoder.Decode(&config); err != nil {
			problems = append(problems, fmt.Errorf("example config is invalid: %w", err))
		} else if err := config.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("example config is structurally invalid: %w", err))
		}
	}
	fixtureRoot := filepath.Join(root, "test", "acceptance", "milo", "testdata")
	_ = filepath.WalkDir(fixtureRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return fs.SkipDir
			}
			problems = append(problems, walkErr)
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			problems = append(problems, readErr)
			return nil
		}
		lower := strings.ToLower(string(body))
		for _, forbidden := range []string{"--allow-local-", "subresource: status", "remove-finalizer", "kind: secret", "image: latest", "image: :latest"} {
			if strings.Contains(lower, forbidden) {
				problems = append(problems, fmt.Errorf("%s contains forbidden acceptance shortcut %q", path, forbidden))
			}
		}
		return nil
	})
	return errors.Join(problems...)
}

func validateCIWorkflow(body []byte) error {
	var document workflowDocument
	if err := yaml.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode CI workflow: %w", err)
	}
	for _, job := range document.Jobs {
		for _, step := range job.Steps {
			if normalizeShellCommand(step.Run) == "go run ./test/acceptance/milo/cmd validate --root ." {
				return nil
			}
		}
	}
	return errors.New("CI workflow must validate the checked-in Milo workflow and policy")
}

func validateWorkflowTrustBoundary(body []byte, policy githubAcceptancePolicy) error {
	var document workflowDocument
	if err := yaml.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode Milo workflow: %w", err)
	}
	trust, ok := document.Jobs["trust-gate"]
	if !ok || trust.RunsOn.Scalar != "ubuntu-latest" || trust.Environment != "" {
		return errors.New("Milo workflow requires an unprivileged trust-gate job")
	}
	var gateFound bool
	for _, step := range trust.Steps {
		if step.Name == "Reject untrusted ref" && step.Env["TRUSTED_REF"] == policy.TrustedRef && normalizeShellCommand(step.Run) == `test "$GITHUB_REF" = "$TRUSTED_REF"` {
			gateFound = true
		}
	}
	if !gateFound {
		return errors.New("Milo trust-gate must reject every ref except the reviewed trusted ref")
	}
	live, ok := document.Jobs["live-preflight"]
	if !ok {
		// Tests use a shortened name while retaining the same structural contract.
		live, ok = document.Jobs["live"]
	}
	if !ok || live.Needs != "trust-gate" || live.If != "github.ref == '"+policy.TrustedRef+"'" || live.Environment != policy.Environment {
		return errors.New("protected Milo job must depend on the trust gate and repeat the main-only condition")
	}
	if live.RunsOn.Group != policy.RunnerGroup || strings.Join(live.RunsOn.Labels, ",") != strings.Join(policy.RunnerLabels, ",") {
		return errors.New("protected Milo job must use the dedicated runner group and exact labels")
	}
	return nil
}

// normalizeShellCommand accounts only for YAML block-scalar line endings and
// surrounding whitespace. The trust gate deliberately accepts one exact shell
// command; suffixes such as `|| true`, extra commands, and alternate expressions
// are rejected rather than interpreted as equivalent.
func normalizeShellCommand(command string) string {
	return strings.TrimSpace(strings.ReplaceAll(command, "\r\n", "\n"))
}

func loadGitHubAcceptancePolicy(path string) (githubAcceptancePolicy, error) {
	var policy githubAcceptancePolicy
	file, err := os.Open(path)
	if err != nil {
		return policy, fmt.Errorf("open GitHub acceptance policy: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("decode GitHub acceptance policy: %w", err)
	}
	if policy.Environment != "milo-acceptance" || policy.TrustedRef != "refs/heads/main" || policy.RunnerGroup != "fleet-milo-acceptance" || !policy.PreventSelfReview {
		return policy, errors.New("GitHub acceptance policy must use the protected main-only Milo environment")
	}
	if strings.Join(policy.RequiredReviewers, ",") != "ecv,kevwilliams" {
		return policy, errors.New("GitHub acceptance policy must retain the designated reviewers")
	}
	if !policy.RestrictedToWorkflows {
		return policy, errors.New("GitHub acceptance runner group must be restricted to selected workflows")
	}
	if strings.Join(policy.SelectedWorkflows, ",") != "datum-cloud/fleet/.github/workflows/milo-acceptance.yml@refs/heads/main" {
		return policy, errors.New("GitHub acceptance runner group must select only the main Milo acceptance workflow")
	}
	if strings.Join(policy.RunnerLabels, ",") != "self-hosted,datum-cloud-fleet,fleet-milo-acceptance" {
		return policy, errors.New("GitHub acceptance policy must retain the dedicated runner labels")
	}
	if strings.Join(policy.SelectedRepositories, ",") != "datum-cloud/fleet" {
		return policy, errors.New("GitHub acceptance runner group must be restricted to datum-cloud/fleet")
	}
	return policy, nil
}
