package miloacceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		SchemaVersion: 1,
		RunID:         "acceptance-001",
		GitRevision:   strings.Repeat("a", 40),
		MiloRevision:  strings.Repeat("b", 40),
		AuthorityID:   "milo-test",
		Projects: Projects{
			Provider:  Project{Name: "provider", UID: "10000000-0000-0000-0000-000000000001"},
			ConsumerA: Project{Name: "consumer-a", UID: "10000000-0000-0000-0000-000000000002"},
			ConsumerB: Project{Name: "consumer-b", UID: "10000000-0000-0000-0000-000000000003"},
		},
		Images: Images{Manager: "sha256:" + strings.Repeat("c", 64), Simulator: "sha256:" + strings.Repeat("d", 64)},
		Kubeconfigs: KubeconfigRefs{
			MiloAdmin: "/credentials/milo-admin", Infrastructure: "/credentials/infrastructure",
			Provider: "/credentials/provider", ConsumerA: "/credentials/consumer-a", ConsumerB: "/credentials/consumer-b",
		},
	}
}

func TestRunBlocksBeforeLiveScenario(t *testing.T) {
	config := validConfig()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	result, err := Run(context.Background(), config, func() time.Time { return now })
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("Run() error = %v, want ErrBlocked", err)
	}
	if result.Status != StatusBlocked || len(result.Blockers) != len(requiredProductionCapabilities) {
		t.Fatalf("Run() result = %#v", result)
	}
	for _, phase := range result.Phases {
		if phase.Name == "live-scenario" && phase.Status == StatusPassed {
			t.Fatal("live scenario must not pass before production preflight exists")
		}
	}
}

func TestRunInvalidConfigIsNotRun(t *testing.T) {
	config := validConfig()
	config.RunID = "caller-secret-run-id"
	config.AuthorityID = "caller-secret-authority"
	config.Projects.Provider.UID = "caller-secret-project-uid"
	config.Kubeconfigs.ConsumerB = "/caller-secret/kubeconfig"
	config.Images.Manager = "caller-secret-image"
	result, err := Run(context.Background(), config, time.Now)
	if !errors.Is(err, ErrNotRun) || result.Status != StatusNotRun {
		t.Fatalf("Run() = (%#v, %v), want NotRun", result, err)
	}
	body, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, callerValue := range []string{config.RunID, config.AuthorityID, config.Projects.Provider.UID, config.Kubeconfigs.ConsumerB, config.Images.Manager} {
		if strings.Contains(string(body), callerValue) {
			t.Fatalf("invalid evidence contains caller value %q: %s", callerValue, body)
		}
	}
	if validationErr := result.Validate(); validationErr != nil {
		t.Fatalf("NotRun result is invalid: %v", validationErr)
	}
}

func TestConfigRejectsLifetimeAndDigestSubstitution(t *testing.T) {
	tests := map[string]func(*Config){
		"duplicate project UID": func(c *Config) { c.Projects.ConsumerB.UID = c.Projects.ConsumerA.UID },
		"mutable image tag":     func(c *Config) { c.Images.Manager = "registry.example/fleet:latest" },
		"ambient kubeconfig":    func(c *Config) { c.Kubeconfigs.Provider = "" },
		"relative kubeconfig":   func(c *Config) { c.Kubeconfigs.Provider = "provider.yaml" },
		"shared credentials":    func(c *Config) { c.Kubeconfigs.ConsumerB = c.Kubeconfigs.ConsumerA },
		"unsafe evidence ID":    func(c *Config) { c.RunID = "run\nclient-key-data: secret" },
		"non-UID evidence":      func(c *Config) { c.Projects.Provider.UID = "token-shaped-value" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}

func TestEvidenceExcludesCredentialPathsAndUsesRestrictedModes(t *testing.T) {
	config := validConfig()
	result, _ := Run(context.Background(), config, time.Now)
	dir := filepath.Join(secureTempDir(t), "evidence")
	if err := WriteEvidence(dir, result, ErrBlocked); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("evidence directory mode = %o", info.Mode().Perm())
	}
	for _, name := range []string{"summary.json", "environment.json", "junit.xml"} {
		path := filepath.Join(dir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, secretPath := range []string{config.Kubeconfigs.MiloAdmin, config.Kubeconfigs.Infrastructure, config.Kubeconfigs.Provider, config.Kubeconfigs.ConsumerA, config.Kubeconfigs.ConsumerB} {
			if strings.Contains(string(body), secretPath) {
				t.Fatalf("%s contains credential path", name)
			}
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", name, fileInfo.Mode().Perm())
		}
	}
	contains, err := EvidenceContainsCredentialMaterial(dir)
	if err != nil || contains {
		t.Fatalf("credential scan = (%v, %v)", contains, err)
	}
	var decoded Result
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "summary.json")), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != StatusBlocked {
		t.Fatalf("decoded status = %q", decoded.Status)
	}
	if junit := string(mustRead(t, filepath.Join(dir, "junit.xml"))); !strings.Contains(junit, `errors="1"`) || !strings.Contains(junit, `<error`) {
		t.Fatalf("blocked JUnit can appear green:\n%s", junit)
	}
}

func TestResultPassedRequiresExactCompletePhases(t *testing.T) {
	valid := validPassedResult(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("complete passed result is invalid: %v", err)
	}
	tests := map[string]func(*Result){
		"missing phase":       func(r *Result) { r.Phases = r.Phases[:len(r.Phases)-1] },
		"unknown phase":       func(r *Result) { r.Phases[2].Name = "invented" },
		"blocked phase":       func(r *Result) { r.Phases[2].Status, r.Phases[2].Reason = StatusBlocked, "blocked" },
		"retained blocker":    func(r *Result) { r.Blockers = []string{"still blocked"} },
		"missing identity":    func(r *Result) { r.Environment.AuthorityID = "" },
		"reversed time":       func(r *Result) { r.CompletedAt = r.StartedAt.Add(-time.Second) },
		"non-UTC time":        func(r *Result) { r.StartedAt = r.StartedAt.In(time.FixedZone("offset", 3600)) },
		"failed overall only": func(r *Result) { r.Status = StatusFailed },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			result := validPassedResult(t)
			mutate(&result)
			if err := result.Validate(); err == nil {
				t.Fatal("Validate() accepted false success")
			}
			dir := filepath.Join(secureTempDir(t), "evidence")
			if err := WriteEvidence(dir, result, nil); err == nil {
				t.Fatal("WriteEvidence() accepted false success")
			}
		})
	}
}

func TestValidateOutcomeRejectsEveryStatusErrorMismatch(t *testing.T) {
	passed := validPassedResult(t)
	blocked, blockedErr := Run(context.Background(), validConfig(), time.Now)
	invalidConfig := validConfig()
	invalidConfig.RunID = "invalid\nrun"
	notRun, notRunErr := Run(context.Background(), invalidConfig, time.Now)
	failed := validPassedResult(t)
	failed.Status = StatusFailed
	failed.Phases[3] = Phase{Name: mandatoryPhases[3], Status: StatusFailed, Reason: "live assertion failed"}
	genericErr := errors.New("live assertion failed")
	type outcome struct {
		name   string
		result Result
	}
	outcomes := []outcome{{"passed", passed}, {"blocked", blocked}, {"not-run", notRun}, {"failed", failed}}
	errorsToTest := []struct {
		name string
		err  error
	}{{"nil", nil}, {"blocked", ErrBlocked}, {"not-run", ErrNotRun}, {"generic", genericErr}, {"ambiguous", errors.Join(ErrBlocked, ErrNotRun)}}
	wanted := map[string]string{"passed": "nil", "blocked": "blocked", "not-run": "not-run", "failed": "generic"}
	if !errors.Is(blockedErr, ErrBlocked) || !errors.Is(notRunErr, ErrNotRun) {
		t.Fatal("test setup produced wrong sentinel errors")
	}
	for _, outcome := range outcomes {
		for _, candidate := range errorsToTest {
			t.Run(outcome.name+"/"+candidate.name, func(t *testing.T) {
				err := ValidateOutcome(outcome.result, candidate.err)
				if candidate.name == wanted[outcome.name] && err != nil {
					t.Fatalf("matching outcome rejected: %v", err)
				}
				if candidate.name != wanted[outcome.name] && err == nil {
					t.Fatal("mismatched status/error pair accepted")
				}
				if candidate.name != wanted[outcome.name] {
					dir := filepath.Join(secureTempDir(t), "evidence")
					if writeErr := WriteEvidence(dir, outcome.result, candidate.err); writeErr == nil {
						t.Fatal("WriteEvidence() serialized a mismatched status/error pair")
					}
				}
			})
		}
	}
}

func TestEvidenceDirectoryMustBeFreshAndExact(t *testing.T) {
	result := validPassedResult(t)
	for name, prepare := range map[string]func(string) error{
		"extra file": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "config.json"), []byte("credential"), 0o600)
		},
		"nested directory": func(dir string) error { return os.Mkdir(filepath.Join(dir, "nested"), 0o700) },
		"symlink entry": func(dir string) error {
			return os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "credential"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(secureTempDir(t), "evidence")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := prepare(dir); err != nil {
				t.Fatal(err)
			}
			if err := WriteEvidence(dir, result, nil); err == nil {
				t.Fatal("WriteEvidence() accepted a nonempty output directory")
			}
		})
	}
	dir := filepath.Join(secureTempDir(t), "evidence")
	if err := WriteEvidence(dir, result, nil); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidenceDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unexpected"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidenceDirectory(dir); err == nil {
		t.Fatal("VerifyEvidenceDirectory() accepted an extra artifact")
	}
}

func TestWriteEvidenceRejectsParentSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	if err := WriteEvidence(filepath.Join(aliasParent, "evidence"), validPassedResult(t), nil); err == nil {
		t.Fatal("WriteEvidence() accepted a symlink in the parent chain")
	}
}

func TestWriteEvidenceRejectsCredentialMarkerBeforePublish(t *testing.T) {
	result, err := Run(context.Background(), validConfig(), time.Now)
	if !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	result.Phases[1].Reason = "client-key-data: caller-controlled"
	dir := filepath.Join(secureTempDir(t), "evidence")
	if err := WriteEvidence(dir, result, ErrBlocked); err == nil {
		t.Fatal("WriteEvidence() published credential-marked evidence")
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected evidence was published: %v", err)
	}
}

func TestCredentialFilesRejectLexicalAndFilesystemAliases(t *testing.T) {
	newFiles := func(t *testing.T) Config {
		t.Helper()
		config := validConfig()
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "credentials")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		paths := []*string{&config.Kubeconfigs.MiloAdmin, &config.Kubeconfigs.Infrastructure, &config.Kubeconfigs.Provider, &config.Kubeconfigs.ConsumerA, &config.Kubeconfigs.ConsumerB}
		for index, path := range paths {
			*path = filepath.Join(dir, fmt.Sprintf("credential-%d", index))
			if err := os.WriteFile(*path, []byte("synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return config
	}
	t.Run("distinct", func(t *testing.T) {
		files, err := OpenCredentialFiles(newFiles(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := files.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("lexical", func(t *testing.T) {
		config := newFiles(t)
		config.Kubeconfigs.ConsumerB = filepath.Join(filepath.Dir(config.Kubeconfigs.ConsumerA), "unused", "..", filepath.Base(config.Kubeconfigs.ConsumerA))
		if err := config.Validate(); err == nil {
			t.Fatal("Validate() accepted a cleaned path alias")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		config := newFiles(t)
		if err := os.Remove(config.Kubeconfigs.ConsumerB); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(config.Kubeconfigs.ConsumerA, config.Kubeconfigs.ConsumerB); err != nil {
			t.Fatal(err)
		}
		if files, err := OpenCredentialFiles(config); err == nil {
			_ = files.Close()
			t.Fatal("OpenCredentialFiles() accepted a hardlink alias")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		config := newFiles(t)
		if err := os.Remove(config.Kubeconfigs.ConsumerB); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(config.Kubeconfigs.ConsumerA, config.Kubeconfigs.ConsumerB); err != nil {
			t.Fatal(err)
		}
		if files, err := OpenCredentialFiles(config); err == nil {
			_ = files.Close()
			t.Fatal("OpenCredentialFiles() accepted a symlink credential")
		}
	})
	t.Run("parent symlink", func(t *testing.T) {
		config := newFiles(t)
		realParent := filepath.Dir(config.Kubeconfigs.ConsumerB)
		aliasParent := filepath.Join(filepath.Dir(realParent), "credential-alias")
		if err := os.Symlink(realParent, aliasParent); err != nil {
			t.Fatal(err)
		}
		config.Kubeconfigs.ConsumerB = filepath.Join(aliasParent, filepath.Base(config.Kubeconfigs.ConsumerB))
		if files, err := OpenCredentialFiles(config); err == nil {
			_ = files.Close()
			t.Fatal("OpenCredentialFiles() accepted a parent symlink")
		}
	})
	t.Run("group readable", func(t *testing.T) {
		config := newFiles(t)
		if err := os.Chmod(config.Kubeconfigs.ConsumerB, 0o640); err != nil {
			t.Fatal(err)
		}
		if files, err := OpenCredentialFiles(config); err == nil {
			_ = files.Close()
			t.Fatal("OpenCredentialFiles() accepted group-readable credentials")
		}
	})
}

func TestWriteEvidenceRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteEvidence(link, validPassedResult(t), nil); err == nil {
		t.Fatal("WriteEvidence() accepted a symlink directory")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func validPassedResult(t *testing.T) Result {
	t.Helper()
	config := validConfig()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	phases := make([]Phase, 0, len(mandatoryPhases))
	for _, name := range mandatoryPhases {
		phases = append(phases, Phase{Name: name, Status: StatusPassed})
	}
	return Result{SchemaVersion: 1, RunID: config.RunID, Status: StatusPassed, StartedAt: now, CompletedAt: now.Add(time.Minute), Environment: environmentFor(config), Phases: phases}
}
