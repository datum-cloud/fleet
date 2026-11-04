package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	config := validCommandConfig()
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

func validCommandConfig() acceptance.Config {
	return acceptance.Config{
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

func TestLiveDoesNotPrintRawExecutionError(t *testing.T) {
	root := secureCommandTempDir(t)
	configPath, output := filepath.Join(root, "config.json"), filepath.Join(root, "evidence")
	body, err := json.Marshal(validCommandConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	oldRunner := runAcceptance
	runAcceptance = func(ctx context.Context, config acceptance.Config, now acceptance.Clock) (acceptance.Result, error) {
		result, _ := acceptance.Run(ctx, config, now)
		result.Status, result.Blockers = acceptance.StatusFailed, nil
		result.Phases[1] = acceptance.Phase{Name: result.Phases[1].Name, Status: acceptance.StatusFailed, Reason: acceptance.ReasonLiveAssertionFailed}
		return result, errors.New("Bearer caller-controlled-secret")
	}
	defer func() { runAcceptance = oldRunner }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = writer
	code := run([]string{"live", "--config", configPath, "--output", output})
	_ = writer.Close()
	os.Stderr = oldStderr
	logged, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if strings.Contains(string(logged), "caller-controlled-secret") || !strings.Contains(string(logged), "LiveAssertionFailed") {
		t.Fatalf("workflow log was not fixed and sanitized: %q", logged)
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

func TestLoadConfigRejectsOversizedFileBeforeDecode(t *testing.T) {
	path := filepath.Join(secureCommandTempDir(t), "oversized.json")
	// An opening brace followed by NUL bytes would fail JSON decoding. The
	// dedicated size error proves the stat boundary runs before decoding or
	// allocating for caller-controlled content.
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxConfigBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); !errors.Is(err, errConfigExceedsSizeLimit) {
		t.Fatalf("loadConfig() error = %v, want size-limit error", err)
	}
}

func TestLoadConfigRejectsUnsafePathsAndModes(t *testing.T) {
	root := secureCommandTempDir(t)
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(realDir, "config.json")
	if err := os.WriteFile(real, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(filepath.Join(alias, "config.json")); err == nil {
		t.Fatal("loadConfig accepted symlink parent")
	}
	if err := os.Chmod(real, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(real); err == nil {
		t.Fatal("loadConfig accepted group-readable config")
	}
}

func TestLoadConfigAncestorSwapCannotRedirectHeldWalk(t *testing.T) {
	root := secureCommandTempDir(t)
	ancestor := filepath.Join(root, "ancestor")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ancestor, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	_, err := loadConfigWithHook(path, func(component string) {
		if component != "ancestor" || swapped {
			return
		}
		swapped = true
		if renameErr := os.Rename(ancestor, ancestor+".held"); renameErr != nil {
			t.Fatal(renameErr)
		}
		if mkdirErr := os.Mkdir(ancestor, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(path, []byte(`{"caller":"attacker"}`), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	})
	if err != nil {
		t.Fatalf("held component walk was redirected by ancestor swap: %v", err)
	}
	if !swapped {
		t.Fatal("ancestor swap hook did not run")
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
