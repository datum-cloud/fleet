package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/standalone"
)

func main() {
	options := standalone.DefaultOptions()
	var phase, simulatorURL, tokenFile string
	flag.StringVar(&phase, "phase", "", "required smoke phase: allocate, install, or release")
	flag.StringVar(&simulatorURL, "simulator-url", "http://fleet-simulator:8080", "synthetic simulator runtime URL")
	flag.StringVar(&tokenFile, "runtime-token-file", "", "file containing the synthetic simulator runtime token")
	flag.StringVar(&options.PrivateNamespace, "private-namespace", options.PrivateNamespace, "private record namespace")
	flag.StringVar(&options.ProviderNamespace, "provider-namespace", options.ProviderNamespace, "synthetic provider namespace")
	flag.StringVar(&options.FirstNamespace, "first-namespace", options.FirstNamespace, "first consumer namespace stand-in")
	flag.StringVar(&options.SecondNamespace, "second-namespace", options.SecondNamespace, "second consumer namespace stand-in")
	flag.StringVar(&options.ImageName, "image", options.ImageName, "digest-pinned synthetic image name")
	flag.Parse()
	if phase == "" || tokenFile == "" {
		fail(fmt.Errorf("phase and runtime-token-file are required"))
	}
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		fail(err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		fail(fmt.Errorf("runtime token must be one nonempty line"))
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fail(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		fail(err)
	}
	kube, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		fail(err)
	}
	provider, err := executor.NewHTTPClient(simulatorURL, token, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		fail(err)
	}
	if err := (standalone.Runner{Client: kube, Executor: provider, Options: options}).Run(context.Background(), phase); err != nil {
		fail(err)
	}
	fmt.Printf("standalone synthetic smoke phase %s completed\n", phase)
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
