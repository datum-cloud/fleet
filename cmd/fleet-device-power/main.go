// fleet-device-power runs staff power operations on unallocated Devices.
//
//	fleet-device-power dispatcher --config=FILE
//	fleet-device-power worker
//
// The dispatcher runs in a site, next to BMC discovery, for one Milo project.
// It accepts or refuses each DevicePowerOperation, starts one worker Job per
// accepted operation and writes operation status. It holds no BMC credential.
// A worker runs one operation: it reads its mounted credential and its stage
// record, sends at most one ComputerSystem.Reset and records the outcome. It
// holds no Milo credential.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"go.datum.net/fleet/internal/devicepower"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fleet-device-power dispatcher --config=FILE | worker")
		os.Exit(64)
	}
	switch os.Args[1] {
	case "dispatcher":
		flags := flag.NewFlagSet("dispatcher", flag.ExitOnError)
		configPath := flags.String("config", "/etc/fleet/device-power/config.yaml", "dispatcher configuration file")
		zapOptions := zap.Options{Development: false}
		zapOptions.BindFlags(flags)
		_ = flags.Parse(os.Args[2:])
		ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
		if err := dispatch(ctrl.SetupSignalHandler(), *configPath); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "fleet-device-power dispatcher:", err)
			os.Exit(1)
		}
	case "worker":
		err := work(ctrl.SetupSignalHandler())
		switch {
		case errors.Is(err, devicepower.ErrNotClaimed):
			// A second worker for one operation stops here, having contacted
			// no BMC, and leaves the first worker's result in place.
			fmt.Fprintln(os.Stderr, "fleet-device-power worker:", err)
		case err != nil:
			fmt.Fprintln(os.Stderr, "fleet-device-power worker:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: fleet-device-power dispatcher --config=FILE | worker")
		os.Exit(64)
	}
}

func dispatch(ctx context.Context, configPath string) error {
	config, err := devicepower.LoadConfig(configPath)
	if err != nil {
		return err
	}
	milo, err := clientcmd.BuildConfigFromFlags("", config.MiloKubeconfig)
	if err != nil {
		return fmt.Errorf("Milo project kubeconfig: %w", err)
	}
	var local *rest.Config
	if config.LocalKubeconfig != "" {
		local, err = clientcmd.BuildConfigFromFlags("", config.LocalKubeconfig)
	} else {
		local, err = rest.InClusterConfig()
	}
	if err != nil {
		return fmt.Errorf("local cluster config: %w", err)
	}
	scheme, err := devicepower.NewScheme()
	if err != nil {
		return err
	}
	mgr, err := devicepower.NewManager(config, milo, local, scheme)
	if err != nil {
		return err
	}
	return mgr.Start(ctx)
}

func work(ctx context.Context) error {
	namespace, name, pod := os.Getenv("POD_NAMESPACE"), os.Getenv(devicepower.RecordEnv), os.Getenv(devicepower.PodUIDEnv)
	if namespace == "" || name == "" || pod == "" {
		return fmt.Errorf("%s, %s and POD_NAMESPACE are required", devicepower.RecordEnv, devicepower.PodUIDEnv)
	}
	local, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("local cluster config: %w", err)
	}
	scheme, err := devicepower.NewScheme()
	if err != nil {
		return err
	}
	records, err := client.New(local, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	w := &devicepower.Worker{Records: records, Record: types.NamespacedName{Namespace: namespace, Name: name}, PodUID: pod,
		CredentialDir: devicepower.CredentialMount, NewBMC: devicepower.PinnedBMC, Poll: 2 * time.Second,
		Log: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, time.Now().UTC().Format(time.RFC3339)+" "+format+"\n", args...)
		}}
	return w.Run(ctx)
}
