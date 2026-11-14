package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"go.datum.net/fleet/internal/simulator"
)

const shutdownTimeout = 10 * time.Second
const maxTokenFileBytes = 4096

func main() {
	var runtimeAddress, adminAddress, statePath, runtimeTokenPath, adminTokenPath string
	flag.StringVar(&runtimeAddress, "listen-address", "127.0.0.1:8080", "runtime HTTP listen address")
	flag.StringVar(&adminAddress, "admin-listen-address", "", "optional admin HTTP listen address; disabled by default")
	flag.StringVar(&statePath, "state-file", "/var/lib/fleet-simulator/state.json", "durable state path")
	flag.StringVar(&runtimeTokenPath, "runtime-token-file", "", "required file containing the runtime bearer token")
	flag.StringVar(&adminTokenPath, "admin-token-file", "", "file containing the admin bearer token; required when the admin listener is enabled")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	runtimeToken, err := readToken(runtimeTokenPath)
	if err != nil {
		logger.Error("load runtime token", "error", err)
		os.Exit(1)
	}
	store, err := simulator.Open(statePath)
	if err != nil {
		logger.Error("open simulator state", "error", err)
		os.Exit(1)
	}

	runtimeTokenSource := func() (string, error) { return readToken(runtimeTokenPath) }
	servers := []*http.Server{newServer(runtimeAddress, simulator.RuntimeHandlerWithTokenSource(store, runtimeTokenSource, logger), logger)}
	if adminAddress != "" {
		adminToken, err := readToken(adminTokenPath)
		if err != nil {
			logger.Error("load admin token", "error", err)
			os.Exit(1)
		}
		if err := validateDistinctTokens(runtimeToken, adminToken); err != nil {
			logger.Error("validate simulator credentials", "error", err)
			os.Exit(1)
		}
		adminTokenSource := func() (string, error) { return readToken(adminTokenPath) }
		servers = append(servers, newServer(adminAddress, simulator.AdminHandlerWithTokenSource(store, adminTokenSource, logger), logger))
	}

	errCh := make(chan error, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			logger.Info("fleet simulator listening", "address", server.Addr)
			errCh <- server.ListenAndServe()
		}()
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	failed := false
	select {
	case sig := <-signals:
		logger.Info("shutting down fleet simulator", "signal", sig.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("fleet simulator server failed", "error", err)
			failed = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("shut down fleet simulator server", "address", server.Addr, "error", err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func readToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("token file is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("token path must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxTokenFileBytes {
		return "", errors.New("token file is too large")
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", errors.New("token file is empty")
	}
	if strings.ContainsFunc(token, unicode.IsControl) {
		return "", errors.New("token must not contain control characters")
	}
	return token, nil
}

func validateDistinctTokens(runtimeToken, adminToken string) error {
	if len(runtimeToken) == len(adminToken) && subtle.ConstantTimeCompare([]byte(runtimeToken), []byte(adminToken)) == 1 {
		return errors.New("runtime and admin tokens must be distinct")
	}
	return nil
}

func newServer(address string, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}
