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
)

var requiredRepositoryFiles = []string{
	"Taskfile.yaml",
	".github/workflows/milo-acceptance.yml",
	"test/acceptance/milo/README.md",
	"test/acceptance/milo/config.example.json",
	"test/acceptance/coverage.md",
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
	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/milo-acceptance.yml"))
	if err == nil {
		text := string(workflow)
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
