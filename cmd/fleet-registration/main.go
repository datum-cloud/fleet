package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/projectdiscovery"
	"go.datum.net/fleet/internal/registration"
	resourcemanager "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/capacity"
	"go.datum.net/fleet/internal/custody/creatorwebhook"
)

func main() {
	var configFile, providers, cert, key, clientCA, clients, projectors string
	flag.StringVar(&configFile, "config", "", "Fleet manager discovery configuration")
	flag.StringVar(&providers, "provider-projects", "", "complete comma-separated provider scope searched for duplicate hardware identity")
	flag.StringVar(&cert, "tls-cert", "/etc/fleet/registration/tls.crt", "serving certificate")
	flag.StringVar(&key, "tls-key", "/etc/fleet/registration/tls.key", "serving key")
	flag.StringVar(&clientCA, "client-ca", "/etc/fleet/registration/client-ca.crt", "CA dedicated to trusted API-server webhook clients")
	flag.StringVar(&clients, "client-identities", "", "required comma-separated subjects of the API-server webhook client certificates; any other certificate is refused")
	flag.StringVar(&projectors, "capacity-projectors", "", "comma-separated usernames of the capacity projector, the only writer of registered capacity status")
	flag.Parse()
	if err := run(ctrl.SetupSignalHandler(), configFile, providers, cert, key, clientCA, list(clients), list(projectors)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configFile, providers, cert, key, clientCA string, clients, projectors []string) error {
	config, err := projectdiscovery.LoadConfig(configFile)
	if err != nil {
		return err
	}
	projects, err := providerScope(providers)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, inventory.AddToScheme, metal.AddToScheme, resourcemanager.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	rootConfig, err := clientcmd.BuildConfigFromFlags("", config.Discovery.DiscoveryKubeconfigPath)
	if err != nil {
		return err
	}
	root, err := client.New(rootConfig, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	template := rootConfig
	if config.Discovery.ProjectKubeconfigPath != "" {
		template, err = clientcmd.BuildConfigFromFlags("", config.Discovery.ProjectKubeconfigPath)
		if err != nil {
			return err
		}
	}
	handler := registration.NewHandler(config.AuthorityID, projects, root, template, scheme)
	handler.Projectors = projectors
	// No cleaning policy resolver exists yet: the cleaning policy owner
	// provides it. Until then no registered capacity can be enabled.
	handler.Cleaning = capacity.Unresolved
	tlsConfig, err := webhookTLS(clientCA, clients)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: ":9443", Handler: http.MaxBytesHandler(handler.Transport(&creatorwebhook.Handler{Projects: projects, Resources: creatorwebhook.Resources}), 2<<20), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, TLSConfig: tlsConfig}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.ListenAndServeTLS(cert, key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func providerScope(providers string) ([]string, error) {
	projects := strings.Split(providers, ",")
	sort.Strings(projects)
	for index, name := range projects {
		if name == "" || strings.TrimSpace(name) != name || (index > 0 && projects[index-1] == name) {
			return nil, errors.New("a nonempty, unique provider scope is required")
		}
	}
	return projects, nil
}

func list(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// webhookTLS accepts only clients whose certificate chains to the dedicated
// client CA and whose subject common name is one of the configured API
// server identities. Any other certificate is refused during the handshake,
// before a request is read.
func webhookTLS(clientCA string, identities []string) (*tls.Config, error) {
	if len(identities) == 0 {
		return nil, errors.New("--client-identities must name the API-server webhook clients")
	}
	caBytes, err := os.ReadFile(clientCA)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, errors.New("API-server client CA is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || !slices.Contains(identities, state.PeerCertificates[0].Subject.CommonName) {
				return errors.New("the client certificate is not a configured API-server webhook client")
			}
			return nil
		}}, nil
}
