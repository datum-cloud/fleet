package miloacceptance

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name    string        `xml:"name,attr"`
	Failure *junitMessage `xml:"failure,omitempty"`
	Error   *junitMessage `xml:"error,omitempty"`
	Skipped *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
}

func WriteEvidence(dir string, result Result, outcomeErr error) error {
	if err := ValidateOutcome(result, outcomeErr); err != nil {
		return fmt.Errorf("refusing invalid evidence result: %w", err)
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("evidence directory must be an explicit absolute path")
	}
	if filepath.Clean(dir) != dir {
		return errors.New("evidence directory must be canonical")
	}
	parent := filepath.Dir(dir)
	if err := rejectSymlinkComponents(parent); err != nil {
		return fmt.Errorf("evidence parent is unsafe: %w", err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return errors.New("evidence parent must be an existing directory")
	}
	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("evidence path must be a real directory")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return errors.New("evidence directory must be fresh and empty")
		}
		if err := os.Remove(dir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return err
	}
	summary, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	environment, err := json.MarshalIndent(result.Environment, "", "  ")
	if err != nil {
		return err
	}
	junit, err := xml.MarshalIndent(toJUnit(result), "", "  ")
	if err != nil {
		return err
	}
	for name, body := range map[string][]byte{
		"summary.json":     append(summary, '\n'),
		"environment.json": append(environment, '\n'),
		"junit.xml":        append([]byte(xml.Header), append(junit, '\n')...),
	} {
		if err := writeAtomic(filepath.Join(stage, name), body); err != nil {
			return err
		}
	}
	if err := VerifyEvidenceDirectory(stage); err != nil {
		return err
	}
	if contains, err := EvidenceContainsCredentialMaterial(stage); err != nil || contains {
		if err != nil {
			return err
		}
		return errors.New("staged evidence contains credential material")
	}
	return os.Rename(stage, dir)
}

func writeAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".evidence-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func toJUnit(result Result) junitSuite {
	suite := junitSuite{Name: "fleet-milo-acceptance", Tests: len(result.Phases)}
	for _, phase := range result.Phases {
		item := junitCase{Name: phase.Name}
		switch phase.Status {
		case StatusFailed:
			suite.Failures++
			item.Failure = &junitMessage{Message: phase.Reason}
		case StatusBlocked, StatusNotRun:
			suite.Skipped++
			item.Skipped = &junitMessage{Message: phase.Reason}
		}
		suite.Cases = append(suite.Cases, item)
	}
	if result.Status != StatusPassed {
		suite.Tests++
		suite.Errors++
		suite.Cases = append(suite.Cases, junitCase{Name: "acceptance-outcome", Error: &junitMessage{Message: "overall acceptance outcome is " + string(result.Status)}})
	}
	return suite
}

func EvidenceContainsCredentialMaterial(dir string) (bool, error) {
	if err := VerifyEvidenceDirectory(dir); err != nil {
		return false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return false, err
		}
		lower := strings.ToLower(string(body))
		for _, forbidden := range []string{"client-certificate-data", "client-key-data", "bearer token", "kind: secret"} {
			if strings.Contains(lower, forbidden) {
				return true, fmt.Errorf("%s contains forbidden credential marker %q", entry.Name(), forbidden)
			}
		}
	}
	return false, nil
}

var evidenceAllowlist = map[string]struct{}{
	"summary.json":     {},
	"environment.json": {},
	"junit.xml":        {},
}

func VerifyEvidenceDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("evidence path must be a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != len(evidenceAllowlist) {
		return errors.New("evidence directory does not contain the exact allowlist")
	}
	for _, entry := range entries {
		if _, allowed := evidenceAllowlist[entry.Name()]; !allowed {
			return fmt.Errorf("unexpected evidence entry %s", entry.Name())
		}
		entryInfo, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("evidence entry %s must be a real regular file", entry.Name())
		}
		if entryInfo.Mode().Perm() != 0o600 {
			return fmt.Errorf("evidence entry %s has unsafe permissions", entry.Name())
		}
	}
	return nil
}
