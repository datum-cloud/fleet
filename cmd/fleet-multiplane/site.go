package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/redfish"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
	"go.datum.net/fleet/internal/site/redfishwork"
	"go.datum.net/fleet/internal/site/vmwork"
)

// The site's retained volume: the site store, the local VM disks, and the
// installer artifacts baked into the site image.
const (
	defaultStore = "/var/lib/fleet-local/site"
	vmDisks      = "/var/lib/fleet-local/vm"
	artifacts    = "/opt/fleet-local/artifacts"
	// libvirtSocketDir is where a local VM site mounts the hypervisor. A
	// Redfish site must not have it.
	libvirtSocketDir = "/run/fleet-libvirt"
)

// openStore opens the site store, creating its owner-only root once. A
// read-only role, such as the boot server, finds it existing.
func openStore() (*site.Store, error) {
	root := os.Getenv("FLEET_SITE_STATE")
	if root == "" {
		root = defaultStore
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("FLEET_SITE_STATE must be a clean absolute path")
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, err
		}
	}
	return site.OpenStore(root, nil)
}

func siteImage() (string, error) { return redfish.VerifyArtifacts(artifacts) }

func bootRoot() (string, error) {
	root := os.Getenv("FLEET_BOOT_EVIDENCE")
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("FLEET_BOOT_EVIDENCE must be a clean absolute path")
	}
	return root, nil
}

// vmBootConfig is what the local VM boot server needs: the artifacts it
// serves, the image they are, and where it records deliveries.
func vmBootConfig() (vmwork.Config, error) {
	if os.Getenv("FLEET_LOCAL_VM") != "disposable-container" {
		return vmwork.Config{}, errors.New("the local VM site requires its disposable container")
	}
	image, err := siteImage()
	if err != nil {
		return vmwork.Config{}, err
	}
	origin, err := redfish.ParseBootOrigin(os.Getenv("FLEET_BOOT_ORIGIN"))
	if err != nil {
		return vmwork.Config{}, err
	}
	boot, err := bootRoot()
	if err != nil {
		return vmwork.Config{}, err
	}
	return vmwork.Config{Artifacts: artifacts, Image: image, Disks: vmDisks, BootRoot: boot, BootOrigin: origin, PXE: true}, nil
}

// vmConfig is the local VM worker's configuration from its environment:
// the boot configuration, the hypervisor, and the site's enrollment.
func vmConfig() (vmwork.Config, fixture.Enrollment, error) {
	config, err := vmBootConfig()
	if err != nil {
		return vmwork.Config{}, nil, err
	}
	accelerator := os.Getenv("FLEET_LOCAL_VM_ACCEL")
	switch accelerator {
	case "":
		accelerator = "kvm"
	case "kvm", "tcg":
	default:
		return vmwork.Config{}, nil, errors.New("FLEET_LOCAL_VM_ACCEL must be kvm or tcg")
	}
	uri := os.Getenv("FLEET_LIBVIRT_URI")
	if !strings.HasPrefix(uri, "qemu:///system?socket="+libvirtSocketDir+"/") {
		return vmwork.Config{}, nil, errors.New("FLEET_LIBVIRT_URI must name the site's libvirt socket")
	}
	// The worker starts no hypervisor itself: the runtime behind the socket
	// does, and it checks its own device. A worker runs only against a
	// runtime that published the same accelerator.
	if err := runtimeAccelerator(libvirtSocketDir, accelerator); err != nil {
		return vmwork.Config{}, nil, err
	}
	var enrollment fixture.Enrollment
	decoder := json.NewDecoder(strings.NewReader(os.Getenv("FLEET_SITE_ENROLLMENT")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&enrollment); err != nil || len(enrollment) == 0 {
		return vmwork.Config{}, nil, errors.New("FLEET_SITE_ENROLLMENT must enroll the site's Devices")
	}
	config.Accelerator, config.URI = accelerator, uri
	return config, enrollment, nil
}

// runtimeAccelerator checks that the VM runtime sharing dir published
// accelerator.
func runtimeAccelerator(dir, accelerator string) error {
	data, err := os.ReadFile(filepath.Join(dir, "accelerator"))
	if err != nil {
		return errors.New("the site's VM runtime has not published its accelerator")
	}
	if published := strings.TrimSpace(string(data)); published != accelerator {
		return fmt.Errorf("the site's VM runtime runs %s guests, not %s", published, accelerator)
	}
	return nil
}

// redfishBootServer is what the Redfish boot server needs: the artifacts it
// serves, the image they are, the listener firmware reaches, and where it
// records deliveries. It reads no enrollment or credential.
func redfishBootServer(store *site.Store) (redfishwork.BootServer, error) {
	boot, err := bootRoot()
	if err != nil {
		return redfishwork.BootServer{}, err
	}
	origin, err := redfish.ParseBootOrigin(os.Getenv("FLEET_BOOT_ORIGIN"))
	if err != nil {
		return redfishwork.BootServer{}, err
	}
	image, err := siteImage()
	if err != nil {
		return redfishwork.BootServer{}, err
	}
	return redfishwork.BootServer{Store: store, Origin: origin, Artifacts: artifacts, Image: image, BootRoot: boot}, nil
}

// redfishConfig is the Redfish worker's configuration from its environment.
// A Redfish site has no hypervisor fallback.
func redfishConfig() (redfishwork.Config, redfish.Enrollment, error) {
	if os.Getenv("FLEET_LIBVIRT_URI") != "" {
		return redfishwork.Config{}, redfish.Enrollment{}, errors.New("a Redfish site refuses a libvirt URI")
	}
	if _, err := os.Stat(libvirtSocketDir); err == nil {
		return redfishwork.Config{}, redfish.Enrollment{}, errors.New("a Redfish site refuses a mounted libvirt socket")
	}
	enrollment, err := redfish.ParseEnrollment([]byte(os.Getenv("FLEET_REDFISH_ENROLLMENT")))
	if err != nil {
		return redfishwork.Config{}, redfish.Enrollment{}, err
	}
	console := os.Getenv("FLEET_REDFISH_CONSOLE")
	if !filepath.IsAbs(console) {
		return redfishwork.Config{}, redfish.Enrollment{}, errors.New("FLEET_REDFISH_CONSOLE must be an absolute path")
	}
	boot, err := bootRoot()
	if err != nil {
		return redfishwork.Config{}, redfish.Enrollment{}, err
	}
	return redfishwork.Config{ConsoleRoot: console, BootRoot: boot, Synced: enrollment.DHCP == redfish.DHCPSynced}, enrollment, nil
}

// siteRunner is the site runner for this cell's mode. A Redfish worker reads
// each Device's own BMC credential Secret, never a shared credential.
func siteRunner(_ context.Context, config *rest.Config, cell string, store *site.Store) (*site.Runner, error) {
	runner := &site.Runner{Site: cell, Store: store, Results: site.NewResults(store)}
	if !redfishMode() {
		vm, enrollment, err := vmConfig()
		if err != nil {
			return nil, err
		}
		runner.Access = fixture.Access{Enrollment: enrollment}
		runner.Kinds = map[string]site.Kind{"DeviceInstallation": vmwork.Installation{Config: vm}, "DeviceWipe": vmwork.Wipe{Config: vm}}
		return runner, nil
	}
	work, enrollment, err := redfishConfig()
	if err != nil {
		return nil, err
	}
	namespace, project := os.Getenv("FLEET_SITE_CREDENTIALS_NAMESPACE"), os.Getenv("FLEET_SITE_PROJECT")
	if namespace == "" || project == "" {
		return nil, errors.New("FLEET_SITE_CREDENTIALS_NAMESPACE and FLEET_SITE_PROJECT name the per-Device credential Secrets")
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	reader, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	targets := site.StaticTargets{}
	for _, target := range enrollment.Targets {
		targets[target.Device] = target
	}
	runner.Access = site.RedfishAccess{Credentials: site.SecretCredentials{Reader: reader, Namespace: namespace, Project: project}, Targets: targets}
	runner.Kinds = map[string]site.Kind{"DeviceInstallation": redfishwork.Installation{Config: work}, "DeviceWipe": redfishwork.Wipe{Config: work}}
	return runner, nil
}

// boot serves network boots, the DHCP reservation feed, or probes that
// every closed admission is denied.
func boot(command, origin, extraMAC, syncToken string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	if command == "boot-denials" {
		// The probe needs only the store's admissions and the listener it
		// probes, given explicitly; it reads no boot server configuration.
		origin, err := redfish.ParseBootOrigin(origin)
		if err != nil {
			return err
		}
		var summary string
		if redfishMode() {
			var extra []string
			if extraMAC != "" {
				extra = append(extra, extraMAC)
			}
			summary, err = redfishwork.BootServer{Store: store}.CheckClosedBoot(origin, extra)
		} else {
			summary, err = vmwork.Config{}.CheckClosedBoot(store, origin)
		}
		if err != nil {
			return err
		}
		fmt.Println(summary)
		return nil
	}
	if redfishMode() {
		server, err := redfishBootServer(store)
		if err != nil {
			return err
		}
		if command == "dhcp-feed" {
			// Reservations name each Device's provisioning attachment, which
			// only the enrollment records.
			enrollment, err := redfish.ParseEnrollment([]byte(os.Getenv("FLEET_REDFISH_ENROLLMENT")))
			if err != nil {
				return err
			}
			server.Enrollment = enrollment
			token, err := os.ReadFile(syncToken)
			if err != nil {
				return fmt.Errorf("DHCP synchronizer token: %w", err)
			}
			return server.ServeDHCP(strings.TrimSpace(string(token)))
		}
		return serveBoot(server.Handler(), vmwork.DefaultBootWindow*2)
	}
	if command == "dhcp-feed" {
		return errors.New("the DHCP reservation feed serves only a Redfish site")
	}
	vm, err := vmBootConfig()
	if err != nil {
		return err
	}
	// Emulated firmware takes minutes to fetch the installer.
	return serveBoot(vm.BootHandler(store), vmwork.DefaultBootWindow)
}
