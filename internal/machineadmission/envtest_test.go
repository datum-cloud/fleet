package machineadmission

import (
	"bytes"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"

	"go.datum.net/fleet/internal/allocation"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
)

// TestCreatorAgainstAPIServer serves both handlers behind the packaged
// webhook configurations. The API server passes the original caller to the
// webhooks, as Milo's root API server does, so the stored creator is the
// caller's whatever the client sent.
func TestCreatorAgainstAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	ctrllog.SetLogger(logr.Discard())
	root := filepath.Join("..", "..")
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(root, "config", "base", "crd")}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = environment.Stop() })
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{metal.AddToScheme, admissionregistrationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(MutatePath, &admission.Webhook{Handler: Mutator{}})
	mux.Handle(ValidatePath, &admission.Webhook{Handler: Validator{}})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	data, err := os.ReadFile(filepath.Join(root, "config", "components", "machine-admission", "webhook.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	caBundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	readdress := func(config *admissionregistrationv1.WebhookClientConfig, path string) {
		if config.URL == nil || !strings.HasSuffix(*config.URL, path) {
			t.Fatalf("the packaged webhook must call %s", path)
		}
		url := server.URL + path
		*config = admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: caBundle}
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var configured []string
	for {
		var raw runtime.RawExtension
		if err := decoder.Decode(&raw); err != nil {
			break
		}
		kind := struct{ Kind string }{}
		if err := yaml.Unmarshal(raw.Raw, &kind); err != nil {
			t.Fatal(err)
		}
		switch kind.Kind {
		case "MutatingWebhookConfiguration":
			webhooks := &admissionregistrationv1.MutatingWebhookConfiguration{}
			if err := yaml.UnmarshalStrict(raw.Raw, webhooks); err != nil {
				t.Fatal(err)
			}
			for index := range webhooks.Webhooks {
				readdress(&webhooks.Webhooks[index].ClientConfig, MutatePath)
			}
			if err := admin.Create(context.Background(), webhooks); err != nil {
				t.Fatal(err)
			}
		case "ValidatingWebhookConfiguration":
			webhooks := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			if err := yaml.UnmarshalStrict(raw.Raw, webhooks); err != nil {
				t.Fatal(err)
			}
			for index := range webhooks.Webhooks {
				readdress(&webhooks.Webhooks[index].ClientConfig, ValidatePath)
			}
			if err := admin.Create(context.Background(), webhooks); err != nil {
				t.Fatal(err)
			}
		}
		configured = append(configured, kind.Kind)
	}
	if !slices.Equal(configured, []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"}) {
		t.Fatalf("packaged webhooks = %v", configured)
	}
	actor := func(name, project string) client.Client {
		cfg := rest.CopyConfig(config)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: name, UID: name + "-uid", Groups: []string{"system:masters"}, Extra: map[string][]string{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {project}}}
		api, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return api
	}
	newMachine := func(creator *metal.MachineCreator) *metal.Machine {
		return &metal.Machine{ObjectMeta: metav1.ObjectMeta{GenerateName: "machine-", Namespace: "default"}, Spec: metal.MachineSpec{
			MachineClassRef: metal.LocalReference{Name: "class"}, MachineProfileRef: metal.LocalReference{Name: "profile"}, LocationRef: metal.LocalReference{Name: "location"}, PowerState: "Off", Creator: creator,
		}}
	}
	customer := actor("customer-example", "consumer-example")
	self := metal.MachineCreator{Username: "customer-example", UID: "customer-example-uid"}
	ctx := context.Background()
	// The webhooks may take a moment to engage.
	deadline := time.Now().Add(15 * time.Second)
	for !apierrors.IsForbidden(admin.Create(ctx, newMachine(nil), client.DryRunAll)) {
		if time.Now().After(deadline) {
			t.Fatal("Machine creator webhooks did not engage")
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Run("the stored creator is the original caller", func(t *testing.T) {
		for name, sent := range map[string]*metal.MachineCreator{"none": nil, "someone else": {Username: "other", UID: "other-uid"}, "the caller": &self} {
			t.Run(name, func(t *testing.T) {
				created := newMachine(sent)
				if err := customer.Create(ctx, created); err != nil {
					t.Fatal(err)
				}
				stored := &metal.Machine{}
				if err := admin.Get(ctx, client.ObjectKeyFromObject(created), stored); err != nil {
					t.Fatal(err)
				}
				if stored.Spec.Creator == nil || *stored.Spec.Creator != self {
					t.Fatalf("creator = %+v", stored.Spec.Creator)
				}
				if !slices.Contains(stored.Finalizers, allocation.MachineFinalizer) {
					t.Fatalf("finalizers = %v", stored.Finalizers)
				}
			})
		}
	})
	t.Run("the creator cannot change", func(t *testing.T) {
		created := newMachine(nil)
		if err := customer.Create(ctx, created); err != nil {
			t.Fatal(err)
		}
		// Another caller in the project may update the Machine without
		// becoming its creator.
		stored := &metal.Machine{}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(created), stored); err != nil {
			t.Fatal(err)
		}
		stored.Spec.PowerState = "On"
		if err := actor("other-example", "consumer-example").Update(ctx, stored); err != nil {
			t.Fatalf("update keeping the creator: %v", err)
		}
		for name, change := range map[string]*metal.MachineCreator{"another creator": {Username: "other-example", UID: "other-example-uid"}, "no creator": nil} {
			if err := admin.Get(ctx, client.ObjectKeyFromObject(created), stored); err != nil {
				t.Fatal(err)
			}
			stored.Spec.Creator = change
			if err := customer.Update(ctx, stored); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
				t.Fatalf("%s: %v", name, err)
			}
		}
	})
	t.Run("a caller without a project is refused", func(t *testing.T) {
		// The API server's own identity carries no project.
		if err := admin.Create(ctx, newMachine(&self)); !apierrors.IsForbidden(err) {
			t.Fatalf("root identity: %v", err)
		}
	})
}
