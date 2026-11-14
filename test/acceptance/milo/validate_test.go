package miloacceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validWorkflow = `name: milo-acceptance
on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  trust-gate:
    runs-on: ubuntu-latest
    steps:
      - name: Reject untrusted ref
        env:
          TRUSTED_REF: refs/heads/main
        run: test "$GITHUB_REF" = "$TRUSTED_REF"
  live:
    needs: trust-gate
    if: github.ref == 'refs/heads/main'
    environment: milo-acceptance
    runs-on:
      group: fleet-milo-acceptance
      labels: [self-hosted, datum-cloud-fleet, fleet-milo-acceptance]
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
      - name: Run protected live preflight
        id: live
        env:
          MILO_ACCEPTANCE_CONFIG: configured
          MILO_ACCEPTANCE_OUTPUT: fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}
        run: |
          test ! -e "$MILO_ACCEPTANCE_OUTPUT"
          acceptance
          verify-evidence --output "$MILO_ACCEPTANCE_OUTPUT"
          echo "ready=true" >> "$GITHUB_OUTPUT"
      - uses: actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6
        if: always() && steps.live.outputs.ready == 'true'
        with:
          path: |
            fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}/summary.json
            fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}/environment.json
            fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}/junit.xml
`

func TestValidateRepositoryProtectsWorkflowBoundary(t *testing.T) {
	if err := ValidateRepository(writeValidationRepo(t, validWorkflow)); err != nil {
		t.Fatalf("valid repository rejected: %v", err)
	}
	tests := map[string]string{
		"mutable action":       strings.Replace(validWorkflow, "actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6", "actions/checkout@v6", 1),
		"job credential env":   strings.Replace(validWorkflow, "    steps:\n", "    env:\n      MILO_ACCEPTANCE_CONFIG: exposed\n    steps:\n", 1),
		"broad artifact":       strings.Replace(validWorkflow, "fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}/summary.json\n            fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}/environment.json\n            fleet-milo-evidence-${{ github.run_id }}-${{ github.run_attempt }}/junit.xml", "fleet-milo-evidence", 1),
		"stale output":         strings.Replace(validWorkflow, `test ! -e "$MILO_ACCEPTANCE_OUTPUT"`, "true", 1),
		"shared output":        strings.ReplaceAll(validWorkflow, "-${{ github.run_id }}-${{ github.run_attempt }}", ""),
		"forged passed upload": strings.Replace(validWorkflow, "if: always() && steps.live.outputs.ready == 'true'", "if: always()", 1),
		"no trusted ref":       strings.Replace(validWorkflow, "    if: github.ref == 'refs/heads/main'\n", "", 1),
		"no trust gate":        strings.Replace(validWorkflow, "      - name: Reject untrusted ref", "      - name: Unprotected dispatch", 1),
		"gate bypass or true":  strings.Replace(validWorkflow, `run: test "$GITHUB_REF" = "$TRUSTED_REF"`, `run: test "$GITHUB_REF" = "$TRUSTED_REF" || true`, 1),
		"gate second command":  strings.Replace(validWorkflow, `run: test "$GITHUB_REF" = "$TRUSTED_REF"`, `run: test "$GITHUB_REF" = "$TRUSTED_REF"; true`, 1),
		"shared runner":        strings.Replace(validWorkflow, "      group: fleet-milo-acceptance\n", "", 1),
		"generic labels":       strings.Replace(validWorkflow, ", datum-cloud-fleet, fleet-milo-acceptance", "", 1),
	}
	for name, workflow := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRepository(writeValidationRepo(t, workflow)); err == nil {
				t.Fatal("ValidateRepository() accepted adversarial workflow")
			}
		})
	}
}

func TestValidateRepositoryProtectsGitHubSettingsContract(t *testing.T) {
	tests := map[string]string{
		"untrusted branch":    strings.Replace(validGitHubPolicy, "refs/heads/main", "refs/heads/feature", 1),
		"self review":         strings.Replace(validGitHubPolicy, `"preventSelfReview": true`, `"preventSelfReview": false`, 1),
		"reviewers":           strings.Replace(validGitHubPolicy, `["ecv", "kevwilliams"]`, `["ecv"]`, 1),
		"shared group":        strings.Replace(validGitHubPolicy, `"runnerGroup": "fleet-milo-acceptance"`, `"runnerGroup": "shared"`, 1),
		"all workflows":       strings.Replace(validGitHubPolicy, `"restrictedToWorkflows": true`, `"restrictedToWorkflows": false`, 1),
		"feature workflow":    strings.Replace(validGitHubPolicy, `milo-acceptance.yml@refs/heads/main`, `milo-acceptance.yml@refs/heads/feature`, 1),
		"additional workflow": strings.Replace(validGitHubPolicy, `"datum-cloud/fleet/.github/workflows/milo-acceptance.yml@refs/heads/main"]`, `"datum-cloud/fleet/.github/workflows/milo-acceptance.yml@refs/heads/main", "datum-cloud/fleet/.github/workflows/other.yml@refs/heads/main"]`, 1),
		"shared repo":         strings.Replace(validGitHubPolicy, `["datum-cloud/fleet"]`, `["datum-cloud/fleet", "example/untrusted"]`, 1),
	}
	for name, policy := range tests {
		t.Run(name, func(t *testing.T) {
			root := writeValidationRepo(t, validWorkflow)
			if err := os.WriteFile(filepath.Join(root, ".github/milo-acceptance-policy.json"), []byte(policy), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ValidateRepository(root); err == nil {
				t.Fatal("ValidateRepository() accepted unsafe GitHub settings policy")
			}
		})
	}
}

func TestValidateRepositoryRequiresCheckedInPolicyValidationInCI(t *testing.T) {
	root := writeValidationRepo(t, validWorkflow)
	path := filepath.Join(root, ".github/workflows/ci.yml")
	if err := os.WriteFile(path, []byte("jobs:\n  validate:\n    steps:\n      - run: go test ./...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepository(root); err == nil {
		t.Fatal("ValidateRepository() accepted CI that never validates the checked-in workflow and policy")
	}
}

func writeValidationRepo(t *testing.T, workflow string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"Taskfile.yaml":                              []byte("version: '3'\n"),
		".github/milo-acceptance-policy.json":        []byte(validGitHubPolicy),
		".github/workflows/ci.yml":                   []byte("jobs:\n  validate:\n    steps:\n      - run: go run ./test/acceptance/milo/cmd validate --root .\n"),
		".github/workflows/milo-acceptance.yml":      []byte(workflow),
		"test/acceptance/milo/README.md":             []byte("acceptance\n"),
		"test/acceptance/milo/github-environment.md": []byte("settings\n"),
		"test/acceptance/coverage.md":                []byte("coverage\n"),
	}
	config, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	files["test/acceptance/milo/config.example.json"] = config
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const validGitHubPolicy = `{
  "environment": "milo-acceptance",
  "trustedRef": "refs/heads/main",
  "requiredReviewers": ["ecv", "kevwilliams"],
  "preventSelfReview": true,
  "runnerGroup": "fleet-milo-acceptance",
  "restrictedToWorkflows": true,
  "selectedWorkflows": ["datum-cloud/fleet/.github/workflows/milo-acceptance.yml@refs/heads/main"],
  "runnerLabels": ["self-hosted", "datum-cloud-fleet", "fleet-milo-acceptance"],
  "selectedRepositories": ["datum-cloud/fleet"]
}`
