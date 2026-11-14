package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	acceptance "go.datum.net/fleet/test/acceptance/milo"
	"golang.org/x/sys/unix"
)

const maxConfigBytes = 1 << 20

var errConfigExceedsSizeLimit = errors.New("config exceeds size limit")
var runAcceptance = acceptance.Run

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: fleet-milo-acceptance <validate|verify-evidence|live>")
		return 2
	}
	switch args[0] {
	case "validate":
		flags := flag.NewFlagSet("validate", flag.ContinueOnError)
		root := flags.String("root", ".", "repository root")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return 2
		}
		if err := acceptance.ValidateRepository(*root); err != nil {
			fmt.Fprintf(os.Stderr, "Milo acceptance validation failed: %v\n", err)
			return 1
		}
		fmt.Println("Milo acceptance packaging is structurally valid; this is not live acceptance evidence.")
		return 0
	case "live":
		return runLive(args[1:])
	case "verify-evidence":
		flags := flag.NewFlagSet("verify-evidence", flag.ContinueOnError)
		output := flags.String("output", "", "absolute evidence directory")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*output) {
			return 2
		}
		if err := acceptance.VerifyEvidenceContent(*output); err != nil {
			fmt.Fprintln(os.Stderr, "evidence is not publishable")
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runLive(args []string) int {
	flags := flag.NewFlagSet("live", flag.ContinueOnError)
	configPath := flags.String("config", "", "absolute path to live acceptance config")
	output := flags.String("output", "", "absolute path for sanitized evidence")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*configPath) || !filepath.IsAbs(*output) {
		fmt.Fprintln(os.Stderr, "live requires explicit absolute --config and --output paths")
		return 2
	}
	config, err := loadConfig(*configPath)
	if err != nil {
		now := time.Now().UTC()
		result := acceptance.Result{SchemaVersion: 1, Status: acceptance.StatusNotRun, StartedAt: now, CompletedAt: now, Phases: []acceptance.Phase{{Name: "configuration", Status: acceptance.StatusNotRun, Reason: acceptance.ReasonConfigurationInvalid}}}
		if validationErr := acceptance.ValidateOutcome(result, acceptance.ErrNotRun); validationErr != nil {
			fmt.Fprintln(os.Stderr, "internal NotRun result invariant failed")
			return 1
		}
		if evidenceErr := acceptance.WriteEvidence(*output, result, acceptance.ErrNotRun); evidenceErr != nil {
			fmt.Fprintf(os.Stderr, "cannot write NotRun evidence: %v\n", evidenceErr)
		}
		fmt.Fprintln(os.Stderr, "Milo acceptance was not run: configuration could not be loaded or decoded")
		return 2
	}
	result, runErr := runAcceptance(context.Background(), config, time.Now)
	if validationErr := acceptance.ValidateOutcome(result, runErr); validationErr != nil {
		fmt.Fprintln(os.Stderr, "internal acceptance result invariant failed")
		return 1
	}
	if err := acceptance.WriteEvidence(*output, result, runErr); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write sanitized evidence: %v\n", err)
		return 1
	}
	if err := acceptance.VerifyEvidenceContent(*output); err != nil {
		fmt.Fprintf(os.Stderr, "evidence structural verification failed: %v\n", err)
		return 1
	}
	if errors.Is(runErr, acceptance.ErrNotRun) {
		fmt.Fprintln(os.Stderr, "Milo acceptance was not run: configuration is invalid")
		return 2
	}
	if errors.Is(runErr, acceptance.ErrBlocked) {
		fmt.Fprintln(os.Stderr, "Milo acceptance is blocked; see sanitized evidence for missing production capabilities.")
		return 3
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "Milo acceptance failed: LiveAssertionFailed; see reviewed sanitized evidence.")
		return 1
	}
	return 0
}

func loadConfig(path string) (acceptance.Config, error) {
	return loadConfigWithHook(path, nil)
}

func loadConfigWithHook(path string, componentOpened func(string)) (acceptance.Config, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return acceptance.Config{}, errors.New("config path must be absolute and canonical")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return acceptance.Config{}, errors.New("config path must name a file")
	}
	parentFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return acceptance.Config{}, errors.New("config root cannot be opened safely")
	}
	defer func() { _ = unix.Close(parentFD) }()
	for _, component := range parts[:len(parts)-1] {
		nextFD, openErr := unix.Openat(parentFD, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if openErr != nil {
			return acceptance.Config{}, errors.New("config parent cannot be opened safely")
		}
		_ = unix.Close(parentFD)
		parentFD = nextFD
		if componentOpened != nil {
			componentOpened(component)
		}
	}
	fd, err := unix.Openat(parentFD, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return acceptance.Config{}, errors.New("config file cannot be opened safely")
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return acceptance.Config{}, errors.New("config descriptor is not an owner-only regular file")
	}
	if info.Size() > maxConfigBytes {
		return acceptance.Config{}, errConfigExceedsSizeLimit
	}
	body, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return acceptance.Config{}, err
	}
	if len(body) > maxConfigBytes {
		return acceptance.Config{}, errConfigExceedsSizeLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config acceptance.Config
	if err := decoder.Decode(&config); err != nil {
		return acceptance.Config{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return acceptance.Config{}, errors.New("config must contain exactly one JSON object")
	}
	return config.Canonicalized(), nil
}
