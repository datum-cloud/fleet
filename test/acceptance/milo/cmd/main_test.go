package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acceptance "go.datum.net/fleet/test/acceptance/milo"
)

func TestLiveReturnsBlockedAndWritesEvidence(t *testing.T) {
	root := secureCommandTempDir(t)
	configPath := filepath.Join(root, "config.json")
	output := filepath.Join(root, "evidence")
	config := acceptance.Config{
		SchemaVersion: 1,
		RunID:         "run-1",
		GitRevision:   strings.Repeat("a", 40),
		MiloRevision:  strings.Repeat("b", 40),
		AuthorityID:   "milo-test",
		Projects: acceptance.Projects{
			Provider:  acceptance.Project{Name: "provider", UID: "10000000-0000-0000-0000-000000000001"},
			ConsumerA: acceptance.Project{Name: "consumer-a", UID: "10000000-0000-0000-0000-000000000002"},
			ConsumerB: acceptance.Project{Name: "consumer-b", UID: "10000000-0000-0000-0000-000000000003"},
		},
		Images: acceptance.Images{Manager: "sha256:" + strings.Repeat("c", 64), Simulator: "sha256:" + strings.Repeat("d", 64)},
		Kubeconfigs: acceptance.KubeconfigRefs{
			MiloAdmin: "/credentials/milo", Infrastructure: "/credentials/infrastructure",
			Provider: "/credentials/provider", ConsumerA: "/credentials/a", ConsumerB: "/credentials/b",
		},
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"live", "--config", configPath, "--output", output}); code != 3 {
		t.Fatalf("run() code = %d, want 3", code)
	}
	var result acceptance.Result
	evidence, err := os.ReadFile(filepath.Join(output, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(evidence, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != acceptance.StatusBlocked {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestLiveRequiresExplicitPaths(t *testing.T) {
	if code := run([]string{"live"}); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if code := run([]string{"live", "--config", "relative.json", "--output", "/tmp/evidence"}); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}

func TestLiveInvalidConfigDoesNotCopyCallerFields(t *testing.T) {
	root := secureCommandTempDir(t)
	configPath := filepath.Join(root, "config.json")
	output := filepath.Join(root, "evidence")
	callerRunID := "secret\nclient-key-data"
	callerAuthority := "SECRET-AUTHORITY"
	config := acceptance.Config{SchemaVersion: 1, RunID: callerRunID, AuthorityID: callerAuthority}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"live", "--config", configPath, "--output", output}); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	evidence, err := os.ReadFile(filepath.Join(output, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(evidence), callerRunID) || strings.Contains(string(evidence), callerAuthority) {
		t.Fatalf("NotRun evidence copied caller fields: %s", evidence)
	}
}

func TestLoadConfigRejectsUnknownFieldsAndTrailingObjects(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":  `{"schemaVersion":1,"unexpected":true}`,
		"trailing": `{}` + "\n" + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadConfig(path); err == nil {
				t.Fatal("loadConfig() succeeded")
			}
		})
	}
}

func secureCommandTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
