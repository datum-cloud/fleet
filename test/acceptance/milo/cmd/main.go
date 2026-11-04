package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	acceptance "go.datum.net/fleet/test/acceptance/milo"
)

const maxConfigBytes = 1 << 20

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: fleet-milo-acceptance <validate|live>")
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
		result := acceptance.Result{SchemaVersion: 1, Status: acceptance.StatusNotRun, StartedAt: now, CompletedAt: now, Phases: []acceptance.Phase{{Name: "configuration", Status: acceptance.StatusNotRun, Reason: "configuration could not be loaded or decoded"}}}
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
	result, runErr := acceptance.Run(context.Background(), config, time.Now)
	if validationErr := acceptance.ValidateOutcome(result, runErr); validationErr != nil {
		fmt.Fprintln(os.Stderr, "internal acceptance result invariant failed")
		return 1
	}
	if err := acceptance.WriteEvidence(*output, result, runErr); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write sanitized evidence: %v\n", err)
		return 1
	}
	if contains, err := acceptance.EvidenceContainsCredentialMaterial(*output); err != nil || contains {
		fmt.Fprintf(os.Stderr, "evidence sanitization failed: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "Milo acceptance failed: %v\n", runErr)
		return 1
	}
	return 0
}

func loadConfig(path string) (acceptance.Config, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return acceptance.Config{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return acceptance.Config{}, errors.New("config path must be a real regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return acceptance.Config{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes+1))
	decoder.DisallowUnknownFields()
	var config acceptance.Config
	if err := decoder.Decode(&config); err != nil {
		return acceptance.Config{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return acceptance.Config{}, errors.New("config must contain exactly one JSON object")
	}
	if info.Size() > maxConfigBytes {
		return acceptance.Config{}, errors.New("config exceeds size limit")
	}
	return config.Canonicalized(), nil
}
