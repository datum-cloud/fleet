package miloacceptance

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	maxSummaryEvidenceBytes     = 64 << 10
	maxEnvironmentEvidenceBytes = 16 << 10
	maxJUnitEvidenceBytes       = 64 << 10
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
	artifacts, err := encodeEvidence(result)
	if err != nil {
		return err
	}
	for name, body := range artifacts {
		if err := writeAtomic(filepath.Join(stage, name), body); err != nil {
			return err
		}
	}
	if err := VerifyEvidenceContent(stage); err != nil {
		return err
	}
	return os.Rename(stage, dir)
}

func encodeEvidence(result Result) (map[string][]byte, error) {
	summary, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	environment, err := json.MarshalIndent(result.Environment, "", "  ")
	if err != nil {
		return nil, err
	}
	junit, err := xml.MarshalIndent(toJUnit(result), "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"summary.json":     append(summary, '\n'),
		"environment.json": append(environment, '\n'),
		"junit.xml":        append([]byte(xml.Header), append(junit, '\n')...),
	}, nil
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
			item.Failure = &junitMessage{Message: string(phase.Reason)}
		case StatusBlocked, StatusNotRun:
			suite.Skipped++
			item.Skipped = &junitMessage{Message: string(phase.Reason)}
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

// VerifyEvidenceContent enforces the structural evidence allowlist. summary.json
// must decode into the closed Result schema and pass its reviewed code
// vocabulary; environment.json and junit.xml must be the exact canonical
// projections of that result. Free-form cluster errors, object dumps, unknown
// fields, and alternate artifact encodings therefore cannot be published.
func VerifyEvidenceContent(dir string) error {
	return verifyEvidenceContent(dir, nil)
}

// verifyEvidenceContent opens the complete artifact set before invoking hook
// and reads only those held descriptors. The hook exists solely for
// adversarial tests which grow or replace paths between open and read.
func verifyEvidenceContent(dir string, hook func()) error {
	binding, dirFD, err := openEvidenceDirectory(dir)
	if err != nil {
		return errors.New("evidence path must be a real directory")
	}
	defer binding.close()
	directory := os.NewFile(uintptr(dirFD), filepath.Base(dir))
	defer directory.Close()
	dirInfo, err := directory.Stat()
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		return errors.New("evidence directory has unsafe permissions")
	}
	var initialDirStat unix.Stat_t
	if err := unix.Fstat(dirFD, &initialDirStat); err != nil {
		return errors.New("evidence directory cannot be inspected safely")
	}
	names, err := directory.Readdirnames(-1)
	if err != nil || len(names) != len(evidenceAllowlist) {
		return errors.New("evidence directory does not contain the exact allowlist")
	}
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, allowed := evidenceAllowlist[name]; !allowed {
			return fmt.Errorf("unexpected evidence entry %s", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate evidence entry %s", name)
		}
		seen[name] = struct{}{}
	}
	limits := map[string]int64{
		"summary.json": maxSummaryEvidenceBytes, "environment.json": maxEnvironmentEvidenceBytes,
		"junit.xml": maxJUnitEvidenceBytes,
	}
	files := make(map[string]*os.File, len(limits))
	stats := make(map[string]unix.Stat_t, len(limits))
	for _, name := range []string{"summary.json", "environment.json", "junit.xml"} {
		fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			closeEvidenceFiles(files)
			return fmt.Errorf("evidence entry %s cannot be opened safely", name)
		}
		file := os.NewFile(uintptr(fd), name)
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > limits[name] {
			file.Close()
			closeEvidenceFiles(files)
			return fmt.Errorf("evidence entry %s exceeds its structural boundary", name)
		}
		files[name] = file
		var openedStat unix.Stat_t
		if err := unix.Fstat(fd, &openedStat); err != nil {
			closeEvidenceFiles(files)
			return fmt.Errorf("evidence entry %s cannot be inspected safely", name)
		}
		stats[name] = openedStat
	}
	defer closeEvidenceFiles(files)
	if hook != nil {
		hook()
	}
	bodies := make(map[string][]byte, len(files))
	for _, name := range []string{"summary.json", "environment.json", "junit.xml"} {
		body, err := io.ReadAll(io.LimitReader(files[name], limits[name]+1))
		if err != nil || int64(len(body)) > limits[name] {
			return fmt.Errorf("evidence entry %s exceeds its structural boundary", name)
		}
		bodies[name] = body
	}
	summary := bodies["summary.json"]
	var result Result
	if err := json.Unmarshal(summary, &result); err != nil {
		return fmt.Errorf("summary.json is not valid result evidence: %w", err)
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("summary.json violates the evidence schema: %w", err)
	}
	want, err := encodeEvidence(result)
	if err != nil {
		return err
	}
	for name, expected := range want {
		if !bytes.Equal(bodies[name], expected) {
			return fmt.Errorf("%s is not the canonical structural projection", name)
		}
	}
	if err := recheckEvidenceSnapshot(binding, directory, initialDirStat, files, stats); err != nil {
		return err
	}
	return nil
}

func recheckEvidenceSnapshot(binding evidencePathBinding, directory *os.File, initialDir unix.Stat_t, files map[string]*os.File, initial map[string]unix.Stat_t) error {
	var dirNow unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &dirNow); err != nil || initialDir.Dev != dirNow.Dev || initialDir.Ino != dirNow.Ino || initialDir.Mode != dirNow.Mode {
		return errors.New("evidence directory changed during verification")
	}
	var pathDir unix.Stat_t
	if err := binding.rewalk(&pathDir); err != nil || pathDir.Dev != dirNow.Dev || pathDir.Ino != dirNow.Ino || pathDir.Mode != dirNow.Mode {
		return errors.New("evidence directory path changed during verification")
	}
	checkFD, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return errors.New("evidence directory cannot be rechecked")
	}
	checkDirectory := os.NewFile(uintptr(checkFD), "evidence-recheck")
	defer checkDirectory.Close()
	names, err := checkDirectory.Readdirnames(-1)
	if err != nil || len(names) != len(evidenceAllowlist) {
		return errors.New("evidence directory changed during verification")
	}
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, ok := evidenceAllowlist[name]; !ok {
			return errors.New("evidence directory changed during verification")
		}
		seen[name] = struct{}{}
	}
	if len(seen) != len(evidenceAllowlist) {
		return errors.New("evidence directory changed during verification")
	}
	for name, file := range files {
		var held, path unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &held); err != nil {
			return fmt.Errorf("evidence entry %s changed during verification", name)
		}
		if err := unix.Fstatat(int(directory.Fd()), name, &path, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("evidence entry %s changed during verification", name)
		}
		was := initial[name]
		if held.Dev != was.Dev || held.Ino != was.Ino || held.Mode != was.Mode || held.Size != was.Size || path.Dev != held.Dev || path.Ino != held.Ino || path.Mode != held.Mode || path.Size != held.Size {
			return fmt.Errorf("evidence entry %s changed during verification", name)
		}
	}
	return nil
}

type evidencePathBinding struct {
	rootFD, parentFD int
	basename         string
	parents          []string
	identities       []unix.Stat_t
}

func (b evidencePathBinding) close() { _ = unix.Close(b.parentFD); _ = unix.Close(b.rootFD) }

func (b evidencePathBinding) rewalk(final *unix.Stat_t) error {
	current, err := unix.Dup(b.rootFD)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(current) }()
	var stat unix.Stat_t
	if err := unix.Fstat(current, &stat); err != nil || !sameDirectoryIdentity(stat, b.identities[0]) {
		return errors.New("evidence root changed")
	}
	for index, component := range b.parents {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if openErr != nil {
			return openErr
		}
		_ = unix.Close(current)
		current = next
		if err := unix.Fstat(current, &stat); err != nil || !sameDirectoryIdentity(stat, b.identities[index+1]) {
			return errors.New("evidence ancestor changed")
		}
	}
	return unix.Fstatat(current, b.basename, final, unix.AT_SYMLINK_NOFOLLOW)
}

func sameDirectoryIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode
}

func openEvidenceDirectory(path string) (binding evidencePathBinding, dirFD int, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return binding, -1, errors.New("noncanonical evidence path")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return binding, -1, errors.New("missing evidence basename")
	}
	binding.rootFD, err = unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return binding, -1, err
	}
	binding.parentFD, err = unix.Dup(binding.rootFD)
	if err != nil {
		unix.Close(binding.rootFD)
		return binding, -1, err
	}
	binding.parents = append([]string(nil), parts[:len(parts)-1]...)
	binding.basename = parts[len(parts)-1]
	var stat unix.Stat_t
	if err := unix.Fstat(binding.rootFD, &stat); err != nil {
		binding.close()
		return binding, -1, err
	}
	binding.identities = append(binding.identities, stat)
	for _, component := range binding.parents {
		next, openErr := unix.Openat(binding.parentFD, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if openErr != nil {
			binding.close()
			return binding, -1, openErr
		}
		unix.Close(binding.parentFD)
		binding.parentFD = next
		if err := unix.Fstat(next, &stat); err != nil {
			binding.close()
			return binding, -1, err
		}
		binding.identities = append(binding.identities, stat)
	}
	dirFD, err = unix.Openat(binding.parentFD, binding.basename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		binding.close()
		return binding, -1, err
	}
	return binding, dirFD, nil
}

func closeEvidenceFiles(files map[string]*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
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
	if info.Mode().Perm() != 0o700 {
		return errors.New("evidence directory has unsafe permissions")
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
