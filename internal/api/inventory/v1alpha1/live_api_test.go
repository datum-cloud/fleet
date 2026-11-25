package v1alpha1

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// liveAPI is a disposable API server with the inventory CRDs installed, for
// tests that need admission policies or impersonated callers.
type liveAPI struct {
	t      *testing.T
	config *rest.Config
	scheme *runtime.Scheme
	admin  client.Client
}

func startAPI(t *testing.T) *liveAPI {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required for disposable API validation")
	}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(inventoryRoot, "crds")}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{AddToScheme, corev1.AddToScheme, admissionregistrationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return &liveAPI{t: t, config: config, scheme: scheme, admin: admin}
}

// actor impersonates a caller with the given username and groups.
func (api *liveAPI) actor(name string, groups ...string) client.Client {
	api.t.Helper()
	config := rest.CopyConfig(api.config)
	config.Impersonate = rest.ImpersonationConfig{UserName: name, UID: name + "-uid", Groups: groups}
	actor, err := client.New(config, client.Options{Scheme: api.scheme})
	if err != nil {
		api.t.Fatal(err)
	}
	return actor
}

func (api *liveAPI) device(name, namespace, serial, uuid string) *Device {
	api.t.Helper()
	device := &Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: DeviceSpec{ClaimRef: LocalReference{Name: "installation"}, Identity: DeviceIdentity{Model: "test", Serial: serial, SystemUUID: uuid}}}
	if err := api.admin.Create(context.Background(), device); err != nil {
		api.t.Fatal(err)
	}
	return device
}

func (api *liveAPI) updateLifecycle(device *Device, lifecycle DeviceLifecycle) error {
	api.t.Helper()
	if err := api.admin.Get(context.Background(), client.ObjectKeyFromObject(device), device); err != nil {
		api.t.Fatal(err)
	}
	device.Spec.Lifecycle = lifecycle
	return api.admin.Update(context.Background(), device)
}

func (api *liveAPI) setLifecycle(device *Device, lifecycle DeviceLifecycle) {
	api.t.Helper()
	if err := api.updateLifecycle(device, lifecycle); err != nil {
		api.t.Fatal(err)
	}
}

// retire takes a Device through the only allowed path to Retired.
func (api *liveAPI) retire(device *Device) {
	api.t.Helper()
	api.setLifecycle(device, DeviceDecommissioning)
	api.setLifecycle(device, DeviceRetired)
}

// installDeletionProtection applies the optional component's manifests as a
// deploying repository that opts in would.
func (api *liveAPI) installDeletionProtection(probe *Device) {
	api.t.Helper()
	data, err := os.ReadFile(filepath.Join(configRoot, "components", "device-deletion-protection", "policy.yaml"))
	if err != nil {
		api.t.Fatal(err)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var object unstructured.Unstructured
		err := decoder.Decode(&object.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			api.t.Fatal(err)
		}
		if err := api.admin.Create(context.Background(), &object); err != nil {
			api.t.Fatal(err)
		}
	}
	api.eventually("Device deletion policy", func() bool {
		return apierrors.IsForbidden(api.admin.Delete(context.Background(), probe, client.DryRunAll))
	})
}

func (api *liveAPI) eventually(what string, condition func() bool) {
	api.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			api.t.Fatalf("%s did not engage", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (api *liveAPI) exists(object client.Object) bool {
	api.t.Helper()
	err := api.admin.Get(context.Background(), client.ObjectKeyFromObject(object), object)
	if err != nil && !apierrors.IsNotFound(err) {
		api.t.Fatal(err)
	}
	return err == nil
}
