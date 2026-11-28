package cleaning

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"
)

// TestPolicyAgainstAPIServer installs the packaged CRD and webhook and
// checks that an invalid policy is refused and a valid one is immutable.
func TestPolicyAgainstAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	ctrllog.SetLogger(logr.Discard())
	root := filepath.Join("..", "..")
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(root, "config", "components", "device-operations", "crds")}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = environment.Stop() })
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{ops.AddToScheme, admissionregistrationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(&admission.Webhook{Handler: Webhook{}})
	t.Cleanup(server.Close)
	data, err := os.ReadFile(filepath.Join(root, "config", "components", "device-operations", "webhook.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	webhooks := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := yaml.UnmarshalStrict(data, webhooks); err != nil {
		t.Fatal(err)
	}
	webhooks.Webhooks = slices.DeleteFunc(webhooks.Webhooks, func(webhook admissionregistrationv1.ValidatingWebhook) bool {
		return !strings.HasSuffix(*webhook.ClientConfig.URL, WebhookPath)
	})
	if len(webhooks.Webhooks) != 1 {
		t.Fatalf("the packaged configuration must have one webhook that calls %s", WebhookPath)
	}
	url := server.URL + WebhookPath
	webhooks.Webhooks[0].ClientConfig = admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}
	if err := admin.Create(context.Background(), webhooks); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	policy := func(name string, spec ops.CleaningPolicySpec) *ops.CleaningPolicy {
		return &ops.CleaningPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: spec}
	}

	fallback := purgePolicy()
	fallback.Rules[0].Methods = append(fallback.Rules[0].Methods, ops.MethodOverwrite)
	if err := admin.Create(ctx, policy("fallback", fallback)); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "below the policy's Purge") {
		t.Fatalf("a fallback below the assurance = %v, want the webhook to refuse it", err)
	}
	// The schema refuses a time bound that is not a duration, so no stored
	// policy fails to decode.
	raw := func(name, bound string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": ops.GroupVersion.String(), "kind": "CleaningPolicy",
			"metadata": map[string]any{"name": name, "namespace": "default"},
			"spec": map[string]any{"assurance": "Clear", "rules": []any{map[string]any{"name": "r", "match": map[string]any{"protocol": "NVMe", "mediaType": "SSD"},
				"methods": []any{"Overwrite"}, "verify": map[string]any{"mode": "Full"}, "timePerTiB": bound}}}}}
	}
	for i, bound := range []string{"1h", "1.5h", "1h30m", "0.5µs", "90s"} {
		if _, err := time.ParseDuration(bound); err != nil {
			t.Fatal(err)
		}
		if err := admin.Create(ctx, raw(fmt.Sprintf("bound-%d", i), bound)); err != nil {
			t.Fatalf("time bound %q = %v, want accepted", bound, err)
		}
	}
	for i, bound := range []string{"abc", `1\x5h`, "1", "-1h", "1h "} {
		if err := admin.Create(ctx, raw(fmt.Sprintf("malformed-%d", i), bound)); !apierrors.IsInvalid(err) {
			t.Fatalf("time bound %q = %v, want Invalid", bound, err)
		}
	}
	valid := policy("valid", purgePolicy())
	if err := admin.Create(ctx, valid); err != nil {
		t.Fatal(err)
	}
	valid.Spec.Assurance = ops.AssuranceClear
	if err := admin.Update(ctx, valid); !apierrors.IsInvalid(err) {
		t.Fatalf("changing a policy spec = %v, want Invalid", err)
	}
}
