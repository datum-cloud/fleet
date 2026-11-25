package creatorwebhook

import (
	"context"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"
)

// TestCreatorAgainstAPIServer serves the handler behind the packaged webhook
// configuration. The API server passes the original caller to the webhook,
// as Milo's root API server does, so the stored creator is the caller's.
func TestCreatorAgainstAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	ctrllog.SetLogger(logr.Discard())
	root := filepath.Join("..", "..", "..")
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
	server := httptest.NewTLSServer(&admission.Webhook{Handler: &Handler{Projects: []string{"provider-example"}, Resources: resources}})
	t.Cleanup(server.Close)
	data, err := os.ReadFile(filepath.Join(root, "config", "components", "device-operations", "webhook.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	webhooks := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := yaml.UnmarshalStrict(data, webhooks); err != nil {
		t.Fatal(err)
	}
	// The configuration also carries the cleaning policy webhook; this test
	// installs only the creator webhook.
	webhooks.Webhooks = slices.DeleteFunc(webhooks.Webhooks, func(webhook admissionregistrationv1.ValidatingWebhook) bool {
		return !strings.HasSuffix(*webhook.ClientConfig.URL, Path)
	})
	if len(webhooks.Webhooks) != 1 {
		t.Fatalf("the packaged configuration must have one webhook that calls %s", Path)
	}
	url := server.URL + Path
	for index := range webhooks.Webhooks {
		webhooks.Webhooks[index].ClientConfig = admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}
	}
	if err := admin.Create(context.Background(), webhooks); err != nil {
		t.Fatal(err)
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
	recovery := func(creator ops.Creator) *ops.DeviceRecovery {
		return &ops.DeviceRecovery{ObjectMeta: metav1.ObjectMeta{GenerateName: "recovery-", Namespace: "default"}, Spec: ops.DeviceRecoverySpec{
			OperationEnvelope: ops.OperationEnvelope{Creator: creator, DeviceRef: ops.DeviceReference{Name: "server-example-001"}, Authority: ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: "settle"}}},
			Decision:          ops.DecisionSettle, Hold: &ops.HeldOperationReference{Kind: "DeviceWipe", Name: "alloc-example-wipe-1", Fence: 3},
			Evidence: ops.RecoveryEvidence{ObservedAt: metav1.NewTime(time.Now()), Summary: "observed"},
		}}
	}
	staff := actor("staff-example", "provider-example")
	ctx := context.Background()
	// The webhook may take a moment to engage.
	deadline := time.Now().Add(15 * time.Second)
	for !apierrors.IsForbidden(staff.Create(ctx, recovery(ops.Creator{Username: "probe", UID: "probe"}), client.DryRunAll)) {
		if time.Now().After(deadline) {
			t.Fatal("creator webhook did not engage")
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Run("the stored creator is the original caller", func(t *testing.T) {
		created := recovery(ops.Creator{Username: "staff-example", UID: "staff-example-uid"})
		if err := staff.Create(ctx, created); err != nil {
			t.Fatal(err)
		}
		stored := &ops.DeviceRecovery{}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(created), stored); err != nil {
			t.Fatal(err)
		}
		if stored.Spec.Creator != (ops.Creator{Username: "staff-example", UID: "staff-example-uid"}) {
			t.Fatalf("creator = %+v", stored.Spec.Creator)
		}
		// The coordinator writes status; the webhook does not govern it.
		stored.Status = ops.OperationStatus{Phase: ops.PhaseFailed, Reason: "Test"}
		if err := admin.Status().Update(ctx, stored); err != nil {
			t.Fatalf("status write: %v", err)
		}
		// Spec, creator included, is immutable.
		stored.Spec.Creator.Username = "someone-else"
		if err := staff.Update(ctx, stored); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
			t.Fatalf("creator changed: %v", err)
		}
	})
	t.Run("a client-supplied creator other than the caller is refused", func(t *testing.T) {
		for _, creator := range []ops.Creator{{Username: "other-staff", UID: "other-staff-uid"}, {Username: "staff-example", UID: "forged"}} {
			if err := staff.Create(ctx, recovery(creator)); !apierrors.IsForbidden(err) {
				t.Fatalf("creator %+v: %v", creator, err)
			}
		}
		// Another caller cannot claim this caller's identity either.
		if err := actor("other-staff", "provider-example").Create(ctx, recovery(ops.Creator{Username: "staff-example", UID: "staff-example-uid"})); !apierrors.IsForbidden(err) {
			t.Fatalf("impersonated creator: %v", err)
		}
	})
	t.Run("a caller outside the provider projects is refused", func(t *testing.T) {
		if err := actor("staff-example", "consumer-example").Create(ctx, recovery(ops.Creator{Username: "staff-example", UID: "staff-example-uid"})); !apierrors.IsForbidden(err) {
			t.Fatalf("consumer project: %v", err)
		}
		// The API server's own identity carries no project and is refused.
		if err := admin.Create(ctx, recovery(ops.Creator{Username: "admin", UID: "admin"})); !apierrors.IsForbidden(err) {
			t.Fatalf("root identity: %v", err)
		}
	})
}
