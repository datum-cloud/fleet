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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func main() {
	var configFile, providers, privateNamespace, cert, key, clientCA string
	flag.StringVar(&configFile, "config", "", "Fleet manager discovery configuration")
	flag.StringVar(&providers, "provider-projects", "", "complete comma-separated provider identity scope, fixed in the private ledger")
	flag.StringVar(&privateNamespace, "private-namespace", "fleet-system-private", "private registration ledger namespace")
	flag.StringVar(&cert, "tls-cert", "/etc/fleet/registration/tls.crt", "serving certificate")
	flag.StringVar(&key, "tls-key", "/etc/fleet/registration/tls.key", "serving key")
	flag.StringVar(&clientCA, "client-ca", "/etc/fleet/registration/client-ca.crt", "CA dedicated to trusted API-server webhook clients")
	flag.Parse()
	if err := run(ctrl.SetupSignalHandler(), configFile, providers, privateNamespace, cert, key, clientCA); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configFile, providers, namespace, cert, key, clientCA string) error {
	config, err := projectdiscovery.LoadConfig(configFile)
	if err != nil {
		return err
	}
	projects := strings.Split(providers, ",")
	sort.Strings(projects)
	for index, name := range projects {
		if name == "" || strings.TrimSpace(name) != name || (index > 0 && projects[index-1] == name) {
			return errors.New("a nonempty, unique provider scope is required")
		}
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, inventory.AddToScheme, metal.AddToScheme, resourcemanager.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	host, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	private, err := client.New(host, client.Options{Scheme: scheme})
	if err != nil {
		return err
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
	handler := registration.NewHandler(registration.Store{Client: private, Namespace: namespace, Authority: config.AuthorityID, Projects: projects}, root, template, scheme)
	if err := handler.VerifyFence(ctx); err != nil {
		return err
	}
	if _, _, err := handler.Store.Read(ctx); err != nil {
		return err
	}
	tlsConfig, err := webhookTLS(clientCA)
	if err != nil {
		return err
	}
	webhook := &admission.Webhook{Handler: handler}
	mux := http.NewServeMux()
	mux.Handle("/validate-registration", webhook)
	server := &http.Server{Addr: ":9443", Handler: http.MaxBytesHandler(mux, 2<<20), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, TLSConfig: tlsConfig}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdown)
				return
			case <-ticker.C:
				if err := handler.Recover(ctx); err != nil {
					fmt.Fprintln(os.Stderr, "registration recovery remains unknown:", err)
				}
			}
		}
	}()
	err = server.ListenAndServeTLS(cert, key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func webhookTLS(clientCA string) (*tls.Config, error) {
	caBytes, err := os.ReadFile(clientCA)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, errors.New("API-server client CA is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}, nil
}
