package registration

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"
)

type liveAPI struct {
	t      *testing.T
	config *rest.Config
	scheme *runtime.Scheme
	admin  client.Client
}

func startAPI(t *testing.T) *liveAPI {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "base", "crd"), filepath.Join("..", "..", "config", "components", "inventory", "crds")}, ErrorIfCRDPathMissing: true}
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
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme, inventory.AddToScheme, metal.AddToScheme, admissionregistrationv1.AddToScheme, authorizationv1.AddToScheme} {
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

// actor impersonates a caller routed through the provider project, as Milo's
// routing filters establish it.
func (api *liveAPI) actor(name string, groups ...string) client.Client {
	api.t.Helper()
	config := rest.CopyConfig(api.config)
	config.Impersonate = rest.ImpersonationConfig{UserName: name, UID: name + "-uid", Groups: groups, Extra: map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"datum-cloud"}}}
	actor, err := client.New(config, client.Options{Scheme: api.scheme})
	if err != nil {
		api.t.Fatal(err)
	}
	return actor
}

func (api *liveAPI) device(name, namespace, serial, uuid string) *inventory.Device {
	api.t.Helper()
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: inventory.DeviceSpec{ClaimRef: inventory.LocalReference{Name: "installation"}, Identity: inventory.DeviceIdentity{Model: "test", Serial: serial, SystemUUID: uuid}}}
	if err := api.admin.Create(context.Background(), device); err != nil {
		api.t.Fatal(err)
	}
	return device
}

func (api *liveAPI) updateLifecycle(device *inventory.Device, lifecycle inventory.DeviceLifecycle) error {
	api.t.Helper()
	if err := api.admin.Get(context.Background(), client.ObjectKeyFromObject(device), device); err != nil {
		api.t.Fatal(err)
	}
	device.Spec.Lifecycle = lifecycle
	return api.admin.Update(context.Background(), device)
}

func (api *liveAPI) setLifecycle(device *inventory.Device, lifecycle inventory.DeviceLifecycle) {
	api.t.Helper()
	if err := api.updateLifecycle(device, lifecycle); err != nil {
		api.t.Fatal(err)
	}
}

// retire takes a Device through the only allowed path to Retired.
func (api *liveAPI) retire(device *inventory.Device) {
	api.t.Helper()
	api.setLifecycle(device, inventory.DeviceDecommissioning)
	api.setLifecycle(device, inventory.DeviceRetired)
}

// installWebhook serves the production handler behind the packaged webhook
// configuration and returns the server so a test can take it away.
func (api *liveAPI) installWebhook() *httptest.Server {
	api.t.Helper()
	handler := &Handler{Projects: []string{"datum-cloud"}, Resolve: func(context.Context, string) (Project, error) {
		return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "datum-cloud", UID: "project-1"}, Client: api.admin}, nil
	}, Authorize: Authorize}
	server := httptest.NewTLSServer(&admission.Webhook{Handler: handler})
	api.t.Cleanup(server.Close)
	data, err := os.ReadFile("../../config/components/registration-admission/webhook.yaml")
	if err != nil {
		api.t.Fatal(err)
	}
	webhooks := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := yaml.UnmarshalStrict(data, webhooks); err != nil {
		api.t.Fatal(err)
	}
	url := server.URL
	for index := range webhooks.Webhooks {
		webhooks.Webhooks[index].ClientConfig = admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}
	}
	if err := api.admin.Create(context.Background(), webhooks); err != nil {
		api.t.Fatal(err)
	}
	operator := api.actor("operator", "system:masters")
	api.eventually("registration webhook", func() bool {
		// Only marked capacity is routed, so the probe carries the marker.
		probe := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: CapacityName("webhook-probe"), Namespace: "default", Annotations: map[string]string{DeviceUIDAnnotation: "webhook-probe", DeviceVersionAnnotation: "1", OperationAnnotation: "webhook-probe"}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "absent"}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
		return apierrors.IsForbidden(operator.Create(context.Background(), probe, client.DryRunAll))
	})
	return server
}

// installDeletionProtection applies the optional component's manifests as an
// environment that opts in would.
func (api *liveAPI) installDeletionProtection(probe *inventory.Device) {
	api.t.Helper()
	data, err := os.ReadFile("../../config/components/device-deletion-protection/policy.yaml")
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

// registration builds the exact create the staff portal sends for the
// Device's current incarnation.
func (api *liveAPI) registration(device *inventory.Device) *metal.MachineCapacity {
	api.t.Helper()
	if err := api.admin.Get(context.Background(), client.ObjectKeyFromObject(device), device); err != nil {
		api.t.Fatal(err)
	}
	return &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: CapacityName(device.UID), Namespace: "default", Annotations: map[string]string{DeviceUIDAnnotation: string(device.UID), DeviceVersionAnnotation: device.ResourceVersion, OperationAnnotation: "operation-" + device.Name}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
}

func requireDenied(t *testing.T, err error, message string) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), message) {
		t.Fatalf("expected denial containing %q, got %v", message, err)
	}
}

func TestRegistrationAgainstLiveDevices(t *testing.T) {
	api := startAPI(t)
	ctx := context.Background()
	server := api.device("server", "default", "serial-1", "11111111-1111-1111-1111-111111111111")
	duplicateA := api.device("duplicate-a", "default", "Shared-Serial", "22222222-2222-2222-2222-222222222222")
	duplicateB := api.device("duplicate-b", "default", " shared-serial", "33333333-3333-3333-3333-333333333333")
	legacyBacked := api.device("legacy-backed", "default", "legacy-serial", "44444444-4444-4444-4444-444444444444")
	limitedTarget := api.device("limited-target", "default", "limited-serial", "55555555-5555-5555-5555-555555555555")
	// Capacity that existed before registration admission was installed.
	legacy := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "legacy-record", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: legacyBacked.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	if err := api.admin.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	// The limited caller may create capacity but may not read Devices.
	if err := api.admin.Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "capacity-creator", Namespace: "default"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"metal.datumapis.com"}, Resources: []string{"machinecapacities"}, Verbs: []string{"create"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := api.admin.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "capacity-creator", Namespace: "default"}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "capacity-creator"}, Subjects: []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: "limited"}}}); err != nil {
		t.Fatal(err)
	}
	webhook := api.installWebhook()
	operator := api.actor("operator", "system:masters")

	t.Run("duplicate alias across two live Devices is denied", func(t *testing.T) {
		for _, device := range []*inventory.Device{duplicateA, duplicateB} {
			requireDenied(t, operator.Create(ctx, api.registration(device)), "shares recorded hardware identity")
		}
	})
	t.Run("missing Device is denied", func(t *testing.T) {
		missing := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: CapacityName("9d0c3c2e-1f5b-4f37-9e43-2f5c6a1b7d80"), Namespace: "default", Annotations: map[string]string{DeviceUIDAnnotation: "9d0c3c2e-1f5b-4f37-9e43-2f5c6a1b7d80", DeviceVersionAnnotation: "1", OperationAnnotation: "missing"}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "never-recorded"}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
		requireDenied(t, operator.Create(ctx, missing), "does not exist")
	})
	t.Run("unauthorized caller is denied", func(t *testing.T) {
		limited := api.actor("limited")
		requireDenied(t, limited.Create(ctx, api.registration(limitedTarget)), "permission on devices is required")
		outsider := api.actor("outsider")
		if err := outsider.Create(ctx, api.registration(limitedTarget)); !apierrors.IsForbidden(err) {
			t.Fatalf("caller without capacity permission: %v", err)
		}
		if api.exists(api.registration(limitedTarget)) {
			t.Fatal("unauthorized registration persisted")
		}
	})
	t.Run("authored names cannot bypass one capacity per Device", func(t *testing.T) {
		authored := api.registration(server)
		authored.Name = "authored-name"
		requireDenied(t, operator.Create(ctx, authored), "derived from the selected Device UID")
	})
	t.Run("legacy capacity already backing the Device is refused", func(t *testing.T) {
		requireDenied(t, operator.Create(ctx, api.registration(legacyBacked)), "already backs capacity legacy-record")
	})
	t.Run("concurrent registrations of one Device persist once", func(t *testing.T) {
		var workers sync.WaitGroup
		results := make(chan error, 8)
		for range 8 {
			capacity := api.registration(server)
			workers.Add(1)
			go func() {
				defer workers.Done()
				results <- operator.Create(ctx, capacity)
			}()
		}
		workers.Wait()
		close(results)
		wins := 0
		for err := range results {
			switch {
			case err == nil:
				wins++
			case apierrors.IsAlreadyExists(err) || (apierrors.IsForbidden(err) && strings.Contains(err.Error(), "already backs capacity")):
			default:
				t.Fatalf("unexpected registration failure: %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("wanted exactly one persisted registration, got %d", wins)
		}
	})
	t.Run("a second capacity for the same Device is refused", func(t *testing.T) {
		// Admission sees the existing capacity first; if two creates race past
		// it, the derived name makes the API server reject the second.
		if err := operator.Create(ctx, api.registration(server)); !apierrors.IsAlreadyExists(err) && (!apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "already backs capacity")) {
			t.Fatalf("second capacity under the derived name: %v", err)
		}
		var capacities metal.MachineCapacityList
		if err := api.admin.List(ctx, &capacities); err != nil {
			t.Fatal(err)
		}
		bound := 0
		for _, capacity := range capacities.Items {
			if capacity.Spec.DeviceRef.Name == server.Name {
				bound++
			}
		}
		if bound != 1 {
			t.Fatalf("Device backs %d capacities", bound)
		}
	})
	t.Run("without the component Device DELETE is allowed and its capacity stays Disabled", func(t *testing.T) {
		registered := api.registration(server)
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(registered), registered); err != nil {
			t.Fatal(err)
		}
		if err := api.admin.Delete(ctx, server); err != nil {
			t.Fatalf("Device deletion without the component: %v", err)
		}
		if api.exists(server) {
			t.Fatal("Device survived deletion without the component")
		}
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(registered), registered); err != nil || registered.Spec.AllocationPolicy != "Disabled" {
			t.Fatalf("orphaned capacity is not Disabled: %v", err)
		}
		registered.Spec.AllocationPolicy = "Enabled"
		registered.Spec.CleaningPolicyRef.Name = "invented-policy"
		if err := operator.Update(ctx, registered); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "cannot leave Disabled: its Device does not exist") {
			t.Fatalf("orphaned capacity was enabled: %v", err)
		}
		if err := operator.Delete(ctx, registered); !apierrors.IsForbidden(err) {
			t.Fatalf("orphaned capacity was deleted: %v", err)
		}
		replacement := api.device("server", "default", "replacement-serial", "66666666-6666-6666-6666-666666666666")
		requireDenied(t, operator.Create(ctx, api.registration(replacement)), "already backs capacity")
	})

	t.Run("only an Active Device can be registered", func(t *testing.T) {
		decommissioning := api.device("decommissioning", "default", "decommissioning-serial", "99999999-9999-9999-9999-999999999991")
		api.setLifecycle(decommissioning, inventory.DeviceDecommissioning)
		requireDenied(t, operator.Create(ctx, api.registration(decommissioning)), "selected Device is Decommissioning")
		retired := api.device("retired", "default", "retired-serial", "99999999-9999-9999-9999-999999999992")
		api.retire(retired)
		requireDenied(t, operator.Create(ctx, api.registration(retired)), "selected Device is Retired")
		// A Retired Device still holds its identity while it exists.
		twin := api.device("retired-twin", "default", "RETIRED-SERIAL", "99999999-9999-9999-9999-999999999993")
		requireDenied(t, operator.Create(ctx, api.registration(twin)), "shares recorded hardware identity")
	})
	t.Run("capacity of a Decommissioning or Retired Device cannot leave Disabled", func(t *testing.T) {
		device := api.device("leaving-service", "default", "leaving-serial", "99999999-9999-9999-9999-999999999994")
		registered := api.registration(device)
		if err := operator.Create(ctx, registered); err != nil {
			t.Fatal(err)
		}
		enable := func() error {
			current := &metal.MachineCapacity{}
			if err := api.admin.Get(ctx, client.ObjectKeyFromObject(registered), current); err != nil {
				t.Fatal(err)
			}
			current.Spec.AllocationPolicy = "Enabled"
			current.Spec.CleaningPolicyRef.Name = "invented-policy"
			return operator.Update(ctx, current)
		}
		requireDenied(t, enable(), "mutation and deletion are disabled")
		api.setLifecycle(device, inventory.DeviceDecommissioning)
		requireDenied(t, enable(), "its Device is Decommissioning")
		api.setLifecycle(device, inventory.DeviceRetired)
		requireDenied(t, enable(), "its Device is Retired")
		current := &metal.MachineCapacity{}
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(registered), current); err != nil || current.Spec.AllocationPolicy != "Disabled" {
			t.Fatalf("capacity left Disabled: %v %q", err, current.Spec.AllocationPolicy)
		}
	})
	t.Run("webhook outage blocks only capacity writes", func(t *testing.T) {
		webhook.Close()
		other := api.device("outage-device", "default", "outage-serial", "88888888-8888-8888-8888-888888888888")
		other.Labels = map[string]string{"edited": "true"}
		if err := api.admin.Update(ctx, other); err != nil {
			t.Fatalf("Device update during outage: %v", err)
		}
		capacity := api.registration(other)
		if err := operator.Create(ctx, capacity); err == nil {
			t.Fatal("capacity registration admitted without the webhook")
		}
		if api.exists(capacity) {
			t.Fatal("capacity persisted during outage")
		}
	})
}
