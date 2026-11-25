package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/simwork"
	"go.datum.net/fleet/internal/site/siteapi"
)

const shutdownTimeout = 10 * time.Second
const maxTokenFileBytes = 4096

// fleet-simulator is the simulator site: synthetic Devices with no BMC whose
// install and wipe effects go through the site framework's fenced journal.
// The coordinator reaches it over the site API.
func main() {
	var address, stateDir, tokenPath, siteName, enrollmentPath string
	var discover bool
	var discoveryInterval time.Duration
	flag.StringVar(&address, "listen-address", "127.0.0.1:8080", "site API listen address")
	flag.StringVar(&stateDir, "state-dir", "/var/lib/fleet/site", "the site store's owner-only directory")
	flag.StringVar(&tokenPath, "runtime-token-file", "", "required file containing the site API bearer token")
	flag.StringVar(&siteName, "site", "simulator", "this site's name")
	flag.StringVar(&enrollmentPath, "enrollment-file", "", "required JSON enrollment of the Devices this site serves")
	flag.BoolVar(&discover, "discover", false, "write fixture discovery for the enrolled Devices, using the in-cluster credential")
	flag.DurationVar(&discoveryInterval, "discovery-interval", time.Minute, "how often fixture discovery runs")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	fail := func(what string, err error) {
		logger.Error(what, "error", err)
		os.Exit(1)
	}
	if _, err := readToken(tokenPath); err != nil {
		fail("load site token", err)
	}
	enrollment, err := readEnrollment(enrollmentPath)
	if err != nil {
		fail("load enrollment", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		fail("create the site store", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		fail("restrict the site store", err)
	}
	store, err := site.OpenStore(stateDir, nil)
	if err != nil {
		fail("open the site store", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	results := site.NewResults(store)
	runner := &site.Runner{Site: siteName, Store: store, Results: results, Access: simwork.Access{Enrollment: enrollment},
		Kinds: map[string]site.Kind{"DeviceInstallation": simwork.Installation{}, "DeviceWipe": simwork.Wipe{}}}
	dispatcher := &siteapi.Dispatcher{Runner: runner, Context: ctx, Logger: logger}
	server := newServer(address, siteapi.Handler(dispatcher, store, results, func() (string, error) { return readToken(tokenPath) }), logger)
	if discover {
		api, err := discoveryClient()
		if err != nil {
			fail("build the discovery client", err)
		}
		go func() {
			for {
				if err := simwork.Discover(ctx, api, enrollment, time.Now()); err != nil && ctx.Err() == nil {
					logger.Info("fixture discovery incomplete", "error", err.Error())
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(discoveryInterval):
				}
			}
		}()
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("simulator site listening", "address", address, "site", siteName, "devices", len(enrollment))
		errCh <- server.ListenAndServe()
	}()
	failed := false
	select {
	case <-ctx.Done():
		logger.Info("shutting down the simulator site")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("simulator site server failed", "error", err)
			failed = true
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("shut down the simulator site", "error", err)
		failed = true
	}
	// Workers stopped by shutdown store no result; the next process
	// resumes them from the journal.
	dispatcher.Wait()
	if failed {
		os.Exit(1)
	}
}

func readEnrollment(path string) (simwork.Enrollment, error) {
	if path == "" {
		return nil, errors.New("an enrollment file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var enrollment simwork.Enrollment
	if err := decoder.Decode(&enrollment); err != nil {
		return nil, err
	}
	for device, target := range enrollment {
		if !strings.Contains(device, "/") || target.SystemUUID == "" || target.BMCEndpoint == "" || target.BMCPublicKeySHA256 == "" || len(target.Disks) == 0 {
			return nil, fmt.Errorf("enrollment of %s is incomplete", device)
		}
	}
	return enrollment, nil
}

func discoveryClient() (client.Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := inventory.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(config, client.Options{Scheme: scheme})
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
