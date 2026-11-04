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
  live:
    environment: milo-acceptance
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
	}
	for name, workflow := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRepository(writeValidationRepo(t, workflow)); err == nil {
				t.Fatal("ValidateRepository() accepted adversarial workflow")
			}
		})
	}
}

func writeValidationRepo(t *testing.T, workflow string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"Taskfile.yaml":                         []byte("version: '3'\n"),
		".github/workflows/milo-acceptance.yml": []byte(workflow),
		"test/acceptance/milo/README.md":        []byte("acceptance\n"),
		"test/acceptance/coverage.md":           []byte("coverage\n"),
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
