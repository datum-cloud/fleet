package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/simulator"
	"go.datum.net/fleet/internal/staging/sessionfixture"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	synthetic := flag.Bool("synthetic", false, "required acknowledgment: test-only admission, observations and cleanup; never use with real assets")
	kubeconfig := flag.String("kubeconfig", "", "management kubeconfig")
	phase := flag.String("phase", "allocate", "allocate or release")
	namespace := flag.String("namespace", "default", "Machine namespace")
	machine := flag.String("machine", "db-01", "Machine name")
	uid := flag.String("machine-uid", "", "exact retained Machine UID, required for release")
	capacityNamespace := flag.String("capacity-namespace", "fleet-provider", "MachineCapacity namespace")
	capacity := flag.String("capacity", "", "MachineCapacity name, required for allocation")
	principal := flag.String("principal", "", "exact authenticated username authorized by this synthetic fixture")
	member := flag.String("member-cluster", "", "synthetic member route")
	privateNamespace := flag.String("private-namespace", "fleet-system", "Coordinator and immutable synthetic grant Secret namespace")
	state := flag.String("state", "", "persistent synthetic executor file; retain across all release retries and reuse")
	flag.Parse()
	if !*synthetic || *kubeconfig == "" || *state == "" || (*phase != "allocate" && *phase != "release") {
		return errors.New("--synthetic, --kubeconfig, --state and --phase=allocate|release are required")
	}
	if *phase == "allocate" && (*capacity == "" || *principal == "" || *member == "") {
		return errors.New("allocation requires --capacity, --principal and --member-cluster")
	}
	if *phase == "release" && *uid == "" {
		return errors.New("release requires --machine-uid (never inferred from a possibly recreated name)")
	}
	if err := os.MkdirAll(filepath.Dir(*state), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(*state+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("synthetic executor is already in use: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	store, err := simulator.Open(*state)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		return err
	}
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		return err
	}
	kube, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	coordinator, err := controller.NewCoordinator(kube, *privateNamespace, sessionfixture.StoreExecutor{Store: store}, controller.LocalDigestImageResolver{}, nil, time.Minute)
	if err != nil {
		return err
	}
	fixture := sessionfixture.Fixture{Client: kube, Coordinator: coordinator, PrivateNamespace: *privateNamespace}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := client.ObjectKey{Namespace: *namespace, Name: *machine}
	var result any
	if *phase == "allocate" {
		asset, allocateErr := fixture.Allocate(ctx, key, client.ObjectKey{Namespace: *capacityNamespace, Name: *capacity}, *principal, *member)
		err = allocateErr
		if asset != nil {
			result = struct {
				MachineUID types.UID `json:"machineUID"`
				Epoch      int64     `json:"epoch"`
			}{asset.ClaimUID, asset.Epoch}
		}
	} else {
		result, err = fixture.Release(ctx, key, types.UID(*uid))
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}
