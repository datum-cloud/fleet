package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
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

func pemCertificate(raw []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}

func TestRealAPIServerAdmissionAndCAS(t *testing.T) {
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
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, inventory.AddToScheme, metal.AddToScheme, admissionregistrationv1.AddToScheme, authorizationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fleet-system-private"}}); err != nil {
		t.Fatal(err)
	}
	device := &inventory.Device{TypeMeta: metav1.TypeMeta{APIVersion: inventory.GroupVersion.String(), Kind: "Device"}, ObjectMeta: metav1.ObjectMeta{Name: "hardware", Namespace: "default"}, Spec: inventory.DeviceSpec{ClaimRef: inventory.LocalReference{Name: "installation"}, Identity: inventory.DeviceIdentity{Model: "test", Serial: "serial-1", SystemUUID: "11111111-1111-1111-1111-111111111111"}}}
	if err := api.Create(ctx, device); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(Ledger{Authority: "milo", Projects: []string{"datum-cloud"}})
	if err := api.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: "fleet-system-private"}, Data: map[string][]byte{"ledger.json": encoded}}); err != nil {
		t.Fatal(err)
	}
	protection, err := os.ReadFile("../../config/base/registration/protection.yaml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(protection), 4096)
	for {
		var object unstructured.Unstructured
		err := decoder.Decode(&object.Object)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := api.Create(ctx, &object); err != nil {
			t.Fatal(err)
		}
	}
	protectionDeadline := time.Now().Add(10 * time.Second)
	for {
		probe := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: "fleet-system-private"}}
		err := api.Delete(ctx, probe, client.DryRunAll)
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			break
		}
		if time.Now().After(protectionDeadline) {
			t.Fatalf("ledger retention policy did not engage: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	for {
		probe := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fleet-system-private"}}
		err := api.Delete(ctx, probe, client.DryRunAll)
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			break
		}
		if time.Now().After(protectionDeadline) {
			t.Fatalf("namespace retention policy did not engage: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	privateConfig := rest.CopyConfig(config)
	privateConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:fleet-system:fleet-registration", Groups: []string{"system:masters"}}
	private, err := client.New(privateConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	var denied atomic.Bool
	handler := &Handler{Store: Store{Client: private, Namespace: "fleet-system-private", Authority: "milo", Projects: []string{"datum-cloud"}}, VerifyFence: func(ctx context.Context) error { return VerifyFence(ctx, api, []string{"datum-cloud"}) }, Resolve: func(context.Context, string) (Project, error) {
		return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "datum-cloud", UID: "project-1"}, Client: api}, nil
	}, Authorize: func(ctx context.Context, project Project, actor authenticationv1.UserInfo, group, resource, name string) error {
		if denied.Load() {
			return ErrConflict
		}
		return Authorize(ctx, project, actor, group, resource, name)
	}}
	server := httptest.NewTLSServer(&admission.Webhook{Handler: handler})
	t.Cleanup(server.Close)
	url := server.URL
	data, err := os.ReadFile("../../config/components/registration-admission/webhook.yaml")
	if err != nil {
		t.Fatal(err)
	}
	webhooks := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := yaml.UnmarshalStrict(data, webhooks); err != nil {
		t.Fatal(err)
	}
	for index := range webhooks.Webhooks {
		webhooks.Webhooks[index].ClientConfig = admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: pemCertificate(server.Certificate().Raw)}
	}
	if err := api.Create(ctx, webhooks); err != nil {
		t.Fatal(err)
	}
	actorConfig := rest.CopyConfig(config)
	actorConfig.Impersonate = rest.ImpersonationConfig{UserName: "operator", UID: "actor-1", Groups: []string{"system:masters"}, Extra: map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"datum-cloud"}}}
	actor, err := client.New(actorConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "dry-run-probe", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
		err = actor.Create(ctx, probe, client.DryRunAll)
		if apierrors.IsForbidden(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook did not engage: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	var wins atomic.Int32
	var workers sync.WaitGroup
	for _, name := range []string{"first", "second", "third"} {
		request := registrationRequest(t, device, name)
		var capacity metal.MachineCapacity
		_ = json.Unmarshal(request.Object.Raw, &capacity)
		capacity.UID = "forged-client-uid"
		workers.Add(1)
		go func() {
			defer workers.Done()
			if actor.Create(ctx, &capacity) == nil {
				wins.Add(1)
			}
		}()
	}
	workers.Wait()
	if wins.Load() != 1 {
		t.Fatalf("wanted one admission, got %d", wins.Load())
	}
	if err := handler.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	_, ledger, err := handler.Store.Read(ctx)
	if err != nil || len(ledger.Entries) != 1 || ledger.Entries[0].Outcome != "Recorded" || ledger.Entries[0].CapacityUID == "forged-client-uid" {
		t.Fatalf("incarnation not bound by real API: %+v %v", ledger, err)
	}
	var capacity metal.MachineCapacity
	if err := actor.Get(ctx, client.ObjectKey{Namespace: "default", Name: ledger.Entries[0].Name}, &capacity); err != nil {
		t.Fatal(err)
	}
	capacity.Spec.AllocationPolicy = "Enabled"
	capacity.Spec.CleaningPolicyRef.Name = "invented-policy"
	if err := actor.Update(ctx, &capacity); !apierrors.IsForbidden(err) {
		t.Fatalf("direct enabling bypass: %v", err)
	}
	if err := actor.Get(ctx, client.ObjectKeyFromObject(&capacity), &capacity); err != nil {
		t.Fatal(err)
	}
	capacity.Annotations[DeviceUIDAnnotation] = "copied-other-incarnation"
	if err := actor.Update(ctx, &capacity); !apierrors.IsForbidden(err) {
		t.Fatalf("binding annotation bypass: %v", err)
	}
	if err := actor.Get(ctx, client.ObjectKeyFromObject(&capacity), &capacity); err != nil {
		t.Fatal(err)
	}
	capacity.Status.Conditions = []metav1.Condition{{Type: "Available", Status: metav1.ConditionTrue, Reason: "Fabricated", Message: "Must not be accepted", LastTransitionTime: metav1.Now()}}
	if err := actor.Status().Update(ctx, &capacity); !apierrors.IsForbidden(err) {
		t.Fatalf("positive readiness bypass: %v", err)
	}
	if err := actor.Delete(ctx, &capacity); !apierrors.IsForbidden(err) {
		t.Fatalf("direct deletion bypass: %v", err)
	}
	if err := actor.Delete(ctx, device); !apierrors.IsForbidden(err) {
		t.Fatalf("Device deletion bypass: %v", err)
	}
	var namespace corev1.Namespace
	if err := actor.Get(ctx, client.ObjectKey{Name: "default"}, &namespace); err != nil {
		t.Fatal(err)
	}
	if err := actor.Delete(ctx, &namespace); !apierrors.IsForbidden(err) {
		t.Fatalf("inventory namespace deletion bypass: %v", err)
	}
	var privateNamespace corev1.Namespace
	if err := api.Get(ctx, client.ObjectKey{Name: "fleet-system-private"}, &privateNamespace); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(ctx, &privateNamespace); !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
		t.Fatalf("private namespace deletion bypass: %v", err)
	}
	secret, _, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(ctx, secret); !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
		t.Fatalf("ledger deletion bypass: %v", err)
	}
	if err := api.Update(ctx, secret); !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
		t.Fatalf("ledger overwrite bypass: %v", err)
	}
	duplicate := device.DeepCopyObject().(*inventory.Device)
	duplicate.Name, duplicate.UID, duplicate.ResourceVersion = "other", "", ""
	if err := actor.Create(ctx, duplicate); !apierrors.IsForbidden(err) {
		t.Fatalf("alias bypass: %v", err)
	}
	denied.Store(true)
	request := registrationRequest(t, device, "denied")
	var rejected metal.MachineCapacity
	_ = json.Unmarshal(request.Object.Raw, &rejected)
	rejected.UID = ""
	if err := actor.Create(ctx, &rejected); !apierrors.IsForbidden(err) {
		t.Fatalf("revoked authorization bypass: %v", err)
	}
}
