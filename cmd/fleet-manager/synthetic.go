package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/delivery"
	"go.datum.net/fleet/internal/site/fixture"
	"go.datum.net/fleet/internal/site/siteapi"
)

// syntheticSite is the synthetic provider: the simulator site.
const syntheticSite = "synthetic-provider"

// syntheticCleaningPolicy is the fixture cleaning policy record every
// synthetic capacity names. Its digest is what each allocation pins.
const syntheticCleaningPolicy = `{"method":"overwrite","disks":"all"}`

// syntheticCustody builds the allocator, the custody coordinator loop and
// fixture discovery for synthetic staging. The manager's own identity is the
// Metal provider; the coordinator and the release controller authenticate
// to Milo with their own client certificates, so each component's writes
// carry its identity.
func syntheticCustody(ctx context.Context, scheme *runtime.Scheme, providerConfig *rest.Config, flags *managerFlags) (*allocation.Allocator, *delivery.Loop, func(context.Context) error, error) {
	enrollment, err := readEnrollment(flags.syntheticEnrollmentFile)
	if err != nil {
		return nil, nil, nil, err
	}
	coordinatorConfig, err := withClientCert(providerConfig, flags.syntheticCoordinatorCertDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("custody coordinator credential: %w", err)
	}
	releaseConfig, err := withClientCert(providerConfig, flags.syntheticReleaseCertDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("release controller credential: %w", err)
	}
	clients := make([]ctrlclient.Client, 3)
	for index, config := range []*rest.Config{providerConfig, coordinatorConfig, releaseConfig} {
		// Decisions read through to the API server, never a cache.
		if clients[index], err = ctrlclient.New(config, ctrlclient.Options{Scheme: scheme}); err != nil {
			return nil, nil, nil, err
		}
	}
	provider, coordinatorClient, releaseClient := clients[0], clients[1], clients[2]
	providerCreator, err := allocation.Creator(ctx, providerConfig)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("identify the Metal provider: %w", err)
	}
	releaseCreator, err := allocation.Creator(ctx, releaseConfig)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("identify the release controller: %w", err)
	}
	registry, err := custody.DefaultRegistry()
	if err != nil {
		return nil, nil, nil, err
	}
	remote, err := siteapi.NewClient(flags.syntheticProviderURL, func() (string, error) { return readToken(flags.syntheticTokenFile) }, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return nil, nil, nil, err
	}
	namespace := flags.syntheticDeviceNamespace
	sites := &delivery.Sites{Sites: map[string]delivery.Site{syntheticSite: remote}, Devices: map[string][]string{}}
	for device := range enrollment {
		deviceNamespace, name, _ := strings.Cut(device, "/")
		if deviceNamespace != namespace {
			return nil, nil, nil, fmt.Errorf("enrolled Device %s is outside %s", device, namespace)
		}
		sites.Devices[name] = []string{syntheticSite}
	}
	principals := custody.Principals{
		custody.CreatorMetalProvider:     {{Username: providerCreator.Username, UID: providerCreator.UID}},
		custody.CreatorReleaseController: {{Username: releaseCreator.Username, UID: releaseCreator.UID}},
	}
	if err := principals.Validate(); err != nil {
		return nil, nil, nil, err
	}
	coordinator := &custody.Coordinator{Client: coordinatorClient, Registry: registry, Sites: sites, Config: custody.Config{Namespace: namespace, Principals: principals}}
	log := ctrl.Log.WithName("custody")
	loop := &delivery.Loop{Client: coordinatorClient, Coordinator: coordinator, Namespace: namespace,
		Courier: &delivery.Courier{Client: coordinatorClient, Registry: registry, Coordinator: coordinator, Sites: sites, Namespace: namespace},
		OnError: func(err error) { log.Info("coordinator pass incomplete", "error", err.Error()) }}
	allocator := &allocation.Allocator{
		Provider: provider, ProviderCreator: providerCreator,
		Releaser: releaseClient, ReleaseCreator: releaseCreator,
		Custody: coordinator, Namespace: namespace,
		Cleaning: func(string) (string, error) { return custody.SpecDigest(syntheticCleaningPolicy) },
		// The synthetic site enrolls one boot NIC and one disk per Device.
		BootInterface: "primary", TargetDisks: []string{"disk-0"},
	}
	// Synthetic staging has no BMC discovery. The manager writes fixture
	// discovery for the synthetic site's Devices instead.
	discover := func(ctx context.Context) error {
		fixture.Run(ctx, provider, enrollment, time.Minute, func(err error) { log.Info("fixture discovery incomplete", "error", err.Error()) })
		return nil
	}
	return allocator, loop, discover, nil
}

// withClientCert is the Milo connection of config, the same endpoint and
// server CA, authenticated only by the client certificate in dir. The
// directory holds a cert-manager certificate Secret's tls.crt and tls.key.
// client-go rereads both files, so a renewed certificate is used without a
// restart; mount the whole Secret, never a subPath.
func withClientCert(config *rest.Config, dir string) (*rest.Config, error) {
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return nil, fmt.Errorf("load the client certificate in %s: %w", dir, err)
	}
	copied := rest.AnonymousClientConfig(config)
	copied.CertFile, copied.KeyFile = certFile, keyFile
	return copied, nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("the token must be one nonempty line")
	}
	return token, nil
}

func readEnrollment(path string) (fixture.Enrollment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var enrollment fixture.Enrollment
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&enrollment); err != nil {
		return nil, err
	}
	if len(enrollment) == 0 {
		return nil, errors.New("the synthetic site enrolls no Device")
	}
	return enrollment, nil
}
