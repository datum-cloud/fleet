package registration

import (
	"context"
	"encoding/json"
	"fmt"
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
	resourcemanager "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	extensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"
)

func TestRealNoopRegistrationAndScopedTransportFailure(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	ctx := context.Background()
	projectCRD := &extensions.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "projects.resourcemanager.miloapis.com"}, Spec: extensions.CustomResourceDefinitionSpec{Group: "resourcemanager.miloapis.com", Names: extensions.CustomResourceDefinitionNames{Plural: "projects", Singular: "project", Kind: "Project", ListKind: "ProjectList"}, Scope: extensions.ClusterScoped, Versions: []extensions.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true, Schema: &extensions.CustomResourceValidation{OpenAPIV3Schema: &extensions.JSONSchemaProps{Type: "object"}}}}}}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "base", "crd"), filepath.Join("..", "..", "config", "components", "inventory", "crds")}, CRDs: []*extensions.CustomResourceDefinition{projectCRD}, ErrorIfCRDPathMissing: true}
	environment.ControlPlane.GetAPIServer().Configure().Append("disable-admission-plugins", "NamespaceLifecycle")
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
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, inventory.AddToScheme, metal.AddToScheme, resourcemanager.AddToScheme, admissionregistrationv1.AddToScheme, authorizationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := extensions.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	deviceCRD := &extensions.CustomResourceDefinition{}
	if err := api.Get(ctx, client.ObjectKey{Name: "devices.fleet.datumapis.com"}, deviceCRD); err != nil {
		t.Fatal(err)
	}
	deviceCRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"] = extensions.JSONSchemaProps{Type: "object", Properties: map[string]extensions.JSONSchemaProps{"observation": {Type: "integer"}}}
	if err := api.Update(ctx, deviceCRD); err != nil {
		t.Fatal(err)
	}
	devices := []*inventory.Device{
		{ObjectMeta: metav1.ObjectMeta{Name: "noop-hardware", Namespace: "default"}, Spec: inventory.DeviceSpec{ClaimRef: inventory.LocalReference{Name: "installation"}, Identity: inventory.DeviceIdentity{Model: "test", Serial: "noop-serial", SystemUUID: "11111111-1111-1111-1111-111111111111"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "real-edit-hardware", Namespace: "default"}, Spec: inventory.DeviceSpec{ClaimRef: inventory.LocalReference{Name: "installation"}, Identity: inventory.DeviceIdentity{Model: "test", Serial: "edit-serial", SystemUUID: "22222222-2222-2222-2222-222222222222"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "collector-hardware", Namespace: "default"}, Spec: inventory.DeviceSpec{ClaimRef: inventory.LocalReference{Name: "installation"}, Identity: inventory.DeviceIdentity{Model: "test", Serial: "collector-serial", SystemUUID: "33333333-3333-3333-3333-333333333333"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "concurrent-hardware", Namespace: "default"}, Spec: inventory.DeviceSpec{ClaimRef: inventory.LocalReference{Name: "installation"}, Identity: inventory.DeviceIdentity{Model: "test", Serial: "concurrent-serial", SystemUUID: "44444444-4444-4444-4444-444444444444"}}},
	}
	for _, device := range devices {
		if err := api.Create(ctx, device, client.FieldOwner("review")); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"datum-cloud", "unrelated-consumer"} {
		if err := api.Create(ctx, &resourcemanager.Project{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-namespace"}}); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(Ledger{Authority: "milo", Projects: []string{"datum-cloud"}})
	if err := api.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: "default"}, Data: map[string][]byte{"ledger.json": encoded}}); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Store: Store{Client: api, Namespace: "default", Authority: "milo", Projects: []string{"datum-cloud"}, AllowInactiveForTests: true}, VerifyFence: func(ctx context.Context) error { return VerifyFence(ctx, api, []string{"datum-cloud"}) }, Resolve: func(context.Context, string) (Project, error) {
		return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "datum-cloud", UID: "project-1"}, Client: api}, nil
	}, Authorize: Authorize}
	type gate struct {
		entered, release chan struct{}
		once             sync.Once
	}
	gates := map[string]*gate{
		"first":  {entered: make(chan struct{}), release: make(chan struct{})},
		"second": {entered: make(chan struct{}), release: make(chan struct{})},
	}
	server := httptest.NewTLSServer(&admission.Webhook{Handler: admission.HandlerFunc(func(ctx context.Context, request admission.Request) admission.Response {
		response := handler.Handle(ctx, request)
		if response.Allowed && request.Operation == admissionv1.Update && request.Resource.Resource == "devices" && request.Name == "concurrent-hardware" {
			var device inventory.Device
			if json.Unmarshal(request.Object.Raw, &device) == nil {
				if pending := gates[device.Labels["admission-delay"]]; pending != nil {
					pending.once.Do(func() {
						close(pending.entered)
						select {
						case <-pending.release:
						case <-ctx.Done():
						}
					})
				}
			}
		}
		return response
	})})
	t.Cleanup(server.Close)
	data, err := os.ReadFile("../../config/components/registration-admission/webhook.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var webhooks admissionregistrationv1.ValidatingWebhookConfiguration
	if err := yaml.UnmarshalStrict(data, &webhooks); err != nil {
		t.Fatal(err)
	}
	url := server.URL
	for index := range webhooks.Webhooks {
		webhooks.Webhooks[index].ClientConfig = admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: pemCertificate(server.Certificate().Raw)}
	}
	if err := api.Create(ctx, &webhooks); err != nil {
		t.Fatal(err)
	}
	actorConfig := rest.CopyConfig(config)
	actorConfig.Impersonate = rest.ImpersonationConfig{UserName: "review-operator", UID: "actor-1", Groups: []string{"system:masters"}, Extra: map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"datum-cloud"}}}
	actor, err := client.New(actorConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	outsideConfig := rest.CopyConfig(actorConfig)
	outsideConfig.Impersonate.Extra = map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"unrelated-consumer"}}
	outside, err := client.New(outsideConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := actor.Update(ctx, devices[0].DeepCopyObject().(*inventory.Device), client.DryRunAll)
		if apierrors.IsForbidden(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inventory webhook did not engage: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	register := func(t *testing.T, device *inventory.Device, name string) error {
		t.Helper()
		request := registrationRequest(t, device, name)
		var capacity metal.MachineCapacity
		if err := json.Unmarshal(request.Object.Raw, &capacity); err != nil {
			t.Fatal(err)
		}
		capacity.UID = ""
		return actor.Create(ctx, &capacity)
	}
	t.Run("unchanged-success-retains-current-RV-and-registers", func(t *testing.T) {
		device := devices[0]
		unchanged := false
		for attempt := 0; attempt < 3; attempt++ {
			if err := actor.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
				t.Fatal(err)
			}
			before := device.ResourceVersion
			if err := actor.Update(ctx, device, client.FieldOwner("review")); err != nil {
				t.Fatal(err)
			}
			if before == device.ResourceVersion {
				unchanged = true
				break
			}
		}
		if !unchanged {
			t.Fatal("API server did not reproduce the unchanged resourceVersion")
		}
		if err := actor.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		if err := register(t, device, "after-noop"); err != nil {
			t.Fatalf("unchanged successful update poisoned the current selection: %v", err)
		}
	})
	t.Run("genuine-edit-requires-explicit-new-selection", func(t *testing.T) {
		device := devices[1]
		baseline := device.DeepCopyObject().(*inventory.Device)
		device.Labels = map[string]string{"review-control": "real-change"}
		if err := actor.Update(ctx, device, client.FieldOwner("review")); err != nil {
			t.Fatal(err)
		}
		if baseline.ResourceVersion == device.ResourceVersion {
			t.Fatal("real edit did not change the version")
		}
		if err := register(t, baseline, "stale-selection"); !apierrors.IsForbidden(err) {
			t.Fatalf("stale selection not fenced: %v", err)
		}
		if err := actor.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		if err := register(t, device, "after-real-edit"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("repeated-status-and-metadata-writes-remain-bounded", func(t *testing.T) {
		device := devices[2]
		var initialSize int
		for observation := 1; observation <= 120; observation++ {
			if err := actor.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
				t.Fatal(err)
			}
			device.Labels = map[string]string{"observation": fmt.Sprint(observation)}
			if err := actor.Update(ctx, device); err != nil {
				t.Fatal(err)
			}
			current := &unstructured.Unstructured{}
			current.SetAPIVersion("fleet.datumapis.com/v1alpha1")
			current.SetKind("Device")
			if err := actor.Get(ctx, client.ObjectKeyFromObject(device), current); err != nil {
				t.Fatal(err)
			}
			before := current.GetResourceVersion()
			current.Object["status"] = map[string]any{"observation": int64(observation)}
			if err := actor.Status().Update(ctx, current, client.FieldOwner("inventory-collector")); err != nil {
				t.Fatal(err)
			}
			if before == current.GetResourceVersion() {
				t.Fatal("status observation did not persist")
			}
			secret, ledger, err := handler.Store.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, fence := range ledger.DeviceFences {
				if fence.UID == device.UID {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%d fences after %d observations", count, observation)
			}
			if observation == 1 {
				initialSize = len(secret.Data["ledger.json"])
			}
			if len(secret.Data["ledger.json"]) > initialSize+20 {
				t.Fatal("ledger grows with routine observations")
			}
		}
		if err := actor.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		if err := register(t, device, "after-collector"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("delayed-old-write-cannot-commit-after-coalescing", func(t *testing.T) {
		firstRelease := sync.OnceFunc(func() { close(gates["first"].release) })
		secondRelease := sync.OnceFunc(func() { close(gates["second"].release) })
		defer firstRelease()
		defer secondRelease()
		wait := func(pending *gate) {
			t.Helper()
			select {
			case <-pending.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("pending update did not enter admission")
			}
		}
		device := devices[3]
		first := device.DeepCopyObject().(*inventory.Device)
		first.Labels = map[string]string{"admission-delay": "first"}
		firstDone := make(chan error, 1)
		go func() { firstDone <- actor.Update(ctx, first) }()
		wait(gates["first"])
		device.Labels = map[string]string{"competing": "persisted"}
		if err := actor.Update(ctx, device); err != nil {
			t.Fatal(err)
		}
		currentVersion := device.ResourceVersion
		second := device.DeepCopyObject().(*inventory.Device)
		second.Labels = map[string]string{"admission-delay": "second"}
		secondDone := make(chan error, 1)
		go func() { secondDone <- actor.Update(ctx, second) }()
		wait(gates["second"])
		firstRelease()
		if err := <-firstDone; !apierrors.IsConflict(err) {
			t.Fatalf("old in-flight baseline must conflict at storage: %v", err)
		}
		if err := actor.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		if device.ResourceVersion != currentVersion {
			t.Fatal("old in-flight write changed current version")
		}
		if err := register(t, device, "during-second-pending"); !apierrors.IsForbidden(err) {
			t.Fatalf("coalescing lost current pending fence: %v", err)
		}
		_, ledger, err := handler.Store.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, fence := range ledger.DeviceFences {
			if fence.UID == device.UID {
				count++
				if fence.Version != currentVersion {
					t.Fatal("retained wrong pending version")
				}
			}
		}
		if count != 1 {
			t.Fatalf("concurrent versions created %d fences", count)
		}
		secondRelease()
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		if err := actor.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
			t.Fatal(err)
		}
		if err := register(t, device, "after-concurrent-writes"); err != nil {
			t.Fatal(err)
		}
	})
	if err := handler.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	_, ledger, err := handler.Store.Read(ctx)
	if err != nil || len(ledger.Entries) != 4 {
		t.Fatalf("expected all registrations: %+v %v", ledger, err)
	}
	for _, entry := range ledger.Entries {
		if entry.Outcome != "Recorded" || entry.CapacityUID == "" {
			t.Fatalf("registration not recorded: %+v", entry)
		}
	}
	if err := api.DeleteAllOf(ctx, &resourcemanager.Project{}); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "collection deletion") {
		t.Fatalf("healthy authority did not reject unnamed lifetime deletion: %v", err)
	}
	server.Close()
	for _, test := range []struct {
		name    string
		write   func() error
		blocked bool
	}{
		{"unrelated Device create", func() error {
			device := devices[0].DeepCopyObject().(*inventory.Device)
			device.Name, device.UID, device.ResourceVersion, device.ManagedFields = "outside-after-outage", "", "", nil
			device.Labels = map[string]string{"provider": "datum-cloud"}
			return outside.Create(ctx, device)
		}, false},
		{"provider Device create without opt-in labels", func() error {
			device := devices[0].DeepCopyObject().(*inventory.Device)
			device.Name, device.UID, device.ResourceVersion, device.ManagedFields = "inside-after-outage", "", "", nil
			device.Namespace = "other-namespace"
			device.Labels = map[string]string{"provider": "unrelated-consumer"}
			return actor.Create(ctx, device)
		}, true},
		{"provider Device update", func() error { return actor.Update(ctx, devices[0], client.DryRunAll) }, true},
		{"provider Device status update", func() error { return actor.Status().Update(ctx, devices[0], client.DryRunAll) }, true},
		{"provider Device delete", func() error { return actor.Delete(ctx, devices[0]) }, true},
		{"unrelated default namespace delete", func() error {
			return outside.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, client.DryRunAll)
		}, false},
		{"provider other namespace delete", func() error {
			return actor.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-namespace"}})
		}, false},
		{"provider default namespace delete", func() error {
			return actor.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, client.DryRunAll)
		}, true},
		{"unrelated root Project delete", func() error {
			return api.Delete(ctx, &resourcemanager.Project{ObjectMeta: metav1.ObjectMeta{Name: "unrelated-consumer"}})
		}, false},
		{"protected root Project delete", func() error {
			return api.Delete(ctx, &resourcemanager.Project{ObjectMeta: metav1.ObjectMeta{Name: "datum-cloud"}})
		}, true},
		{"protected root Project collection delete", func() error { return api.DeleteAllOf(ctx, &resourcemanager.Project{}) }, true},
		{"global capacity create", func() error {
			return outside.Create(ctx, &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "outside-capacity", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: devices[0].Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}})
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.write()
			if test.blocked {
				if err == nil || !strings.Contains(err.Error(), "failed calling webhook") {
					t.Fatalf("expected fail-closed transport error, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unrelated operation depends on unavailable webhook: %v", err)
			}
		})
	}
	for _, test := range []struct {
		name  string
		extra map[string][]string
	}{
		{"missing context", nil},
		{"nested organization context", map[string][]string{iam.ParentKindExtraKey: {"Organization"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"unrelated-consumer"}}},
		{"nested user context", map[string][]string{iam.ParentKindExtraKey: {"User"}, iam.ParentAPIGroupExtraKey: {"iam.miloapis.com"}, iam.ParentNameExtraKey: {"unrelated-consumer"}}},
		{"ambiguous context", map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"unrelated-consumer", "datum-cloud"}}},
		{"empty context", map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {""}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			unprovenConfig := rest.CopyConfig(actorConfig)
			unprovenConfig.Impersonate.Extra = test.extra
			unproven, err := client.New(unprovenConfig, client.Options{Scheme: scheme})
			if err != nil {
				t.Fatal(err)
			}
			device := devices[0].DeepCopyObject().(*inventory.Device)
			device.Name, device.UID, device.ResourceVersion, device.ManagedFields = "unproven-context", "", "", nil
			for _, err := range []error{unproven.Create(ctx, device), unproven.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, client.DryRunAll)} {
				if err == nil || !strings.Contains(err.Error(), "failed calling webhook") {
					t.Fatalf("ambiguous routing context bypassed fail-closed transport: %v", err)
				}
			}
		})
	}
}
