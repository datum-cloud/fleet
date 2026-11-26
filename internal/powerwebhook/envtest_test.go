package powerwebhook

import (
	"context"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
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
)

var root = filepath.Join("..", "..")

// The identities that can reach Machine status in some mode, other than the
// power projector's. Each is impersonated with system:masters so that only
// admission decides.
var others = []string{
	"consumer-example",
	"claimant-example",
	"system:serviceaccount:fleet-system:fleet-custody-coordinator",
	"system:serviceaccount:fleet-system:fleet-release-controller",
	"system:serviceaccount:fleet-system:fleet-simulator",
	"provider-example",
	"discoverer-example",
	"staff-example",
}

func start(t *testing.T) (*rest.Config, client.Client, *runtime.Scheme) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	ctrllog.SetLogger(logr.Discard())
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
	return config, admin, scheme
}

func actor(t *testing.T, config *rest.Config, scheme *runtime.Scheme, name string) client.Client {
	t.Helper()
	cfg := rest.CopyConfig(config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: name, UID: name + "-uid", Groups: []string{"system:masters"}}
	api, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return api
}

// powered creates a Machine whose status.power the writer has set.
func powered(t *testing.T, writer client.Client, admin client.Client, name string) *metal.Machine {
	t.Helper()
	ctx := context.Background()
	machine := &metal.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}, Spec: metal.MachineSpec{
		MachineClassRef: metal.LocalReference{Name: "class-example"}, MachineProfileRef: metal.LocalReference{Name: "profile-example"}, LocationRef: metal.LocalReference{Name: "location-example"}, PowerState: "Off",
	}}
	if err := admin.Create(ctx, machine); err != nil {
		t.Fatal(err)
	}
	machine.Status.Phase = "Ready"
	machine.Status.Power = &metal.PowerStatus{State: "Off"}
	if err := writer.Status().Update(ctx, machine); err != nil {
		t.Fatalf("the power projector's write: %v", err)
	}
	return machine
}

// exercise proves the rule against a live API server: the writer changes
// power, and every other identity's power change is refused while its other
// status writes are admitted.
func exercise(t *testing.T, config *rest.Config, admin client.Client, scheme *runtime.Scheme, writerName string) {
	ctx := context.Background()
	writer := actor(t, config, scheme, writerName)
	machine := powered(t, writer, admin, "power-example")
	fresh := func() *metal.Machine {
		current := &metal.Machine{}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(machine), current); err != nil {
			t.Fatal(err)
		}
		return current
	}
	t.Run("the power projector changes and clears power", func(t *testing.T) {
		current := fresh()
		current.Status.Power = &metal.PowerStatus{State: "Unknown"}
		if err := writer.Status().Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		current = fresh()
		current.Status.Power = nil
		if err := writer.Status().Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		current = fresh()
		current.Status.Power = &metal.PowerStatus{State: "Off"}
		if err := writer.Status().Update(ctx, current); err != nil {
			t.Fatal(err)
		}
	})
	for _, name := range append(append([]string(nil), others...), "admin") {
		t.Run(name, func(t *testing.T) {
			api := admin
			if name != "admin" {
				api = actor(t, config, scheme, name)
			}
			for _, power := range []*metal.PowerStatus{{State: "On"}, {State: "Unknown"}, nil} {
				current := fresh()
				current.Status.Power = power
				if err := api.Status().Update(ctx, current); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
					t.Fatalf("power %+v: %v", power, err)
				}
				patch := client.MergeFrom(fresh())
				current = fresh()
				current.Status.Power = power
				if err := api.Status().Patch(ctx, current, patch); !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
					t.Fatalf("patched power %+v: %v", power, err)
				}
			}
			// Other status fields stay theirs to write; RBAC decides those.
			current := fresh()
			current.Status.Phase = map[string]string{"Ready": "Pending"}[current.Status.Phase]
			if current.Status.Phase == "" {
				current.Status.Phase = "Ready"
			}
			if err := api.Status().Update(ctx, current); err != nil {
				t.Fatalf("a status write that keeps power: %v", err)
			}
			if fresh().Status.Power == nil || fresh().Status.Power.State != "Off" {
				t.Fatalf("power changed to %+v", fresh().Status.Power)
			}
		})
	}
	t.Run("a Machine update cannot change power", func(t *testing.T) {
		current := fresh()
		current.Status.Power = &metal.PowerStatus{State: "On"}
		if err := actor(t, config, scheme, "consumer-example").Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		if fresh().Status.Power.State != "Off" {
			t.Fatal("a main-resource update changed status.power")
		}
	})
}

// waitFor retries a dry-run power change by another identity on the probe
// until the guard refuses it, because admission configuration may take a
// moment to engage.
func waitFor(t *testing.T, config *rest.Config, admin client.Client, scheme *runtime.Scheme, probe *metal.Machine) {
	t.Helper()
	ctx := context.Background()
	other := actor(t, config, scheme, "consumer-example")
	deadline := time.Now().Add(15 * time.Second)
	for {
		current := &metal.Machine{}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(probe), current); err != nil {
			t.Fatal(err)
		}
		current.Status.Power = &metal.PowerStatus{State: "On"}
		if err := other.Status().Update(ctx, current, client.DryRunAll); apierrors.IsForbidden(err) || apierrors.IsInvalid(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the power guard did not engage")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPowerWebhookAgainstAPIServer serves the handler behind the packaged
// webhook configuration. The API server passes the original caller, as
// Milo's root API server does.
func TestPowerWebhookAgainstAPIServer(t *testing.T) {
	config, admin, scheme := start(t)
	// The probe's power is set before the webhook exists.
	probe := powered(t, admin, admin, "probe-example")
	server := httptest.NewTLSServer(&admission.Webhook{Handler: &Handler{Writer: writer}})
	t.Cleanup(server.Close)
	data, err := os.ReadFile(filepath.Join(root, "config", "components", "machine-power-admission", "webhook.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	webhooks := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := yaml.UnmarshalStrict(data, webhooks); err != nil {
		t.Fatal(err)
	}
	if len(webhooks.Webhooks) != 1 || !strings.HasSuffix(*webhooks.Webhooks[0].ClientConfig.URL, Path) || *webhooks.Webhooks[0].FailurePolicy != admissionregistrationv1.Fail {
		t.Fatalf("the packaged webhook must call %s and fail closed", Path)
	}
	url := server.URL + Path
	webhooks.Webhooks[0].ClientConfig = admissionregistrationv1.WebhookClientConfig{URL: &url, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}
	if err := admin.Create(context.Background(), webhooks); err != nil {
		t.Fatal(err)
	}
	waitFor(t, config, admin, scheme, probe)
	exercise(t, config, admin, scheme, writer)

	t.Run("only power changes are routed", func(t *testing.T) {
		// With the service down, a power change fails closed and every other
		// status write still succeeds.
		server.Close()
		ctx := context.Background()
		current := &metal.Machine{}
		if err := admin.Get(ctx, client.ObjectKey{Namespace: "default", Name: "power-example"}, current); err != nil {
			t.Fatal(err)
		}
		current.Status.Phase = "Ready"
		if err := admin.Status().Update(ctx, current); err != nil {
			t.Fatalf("a status write that keeps power was routed: %v", err)
		}
		current.Status.Power = &metal.PowerStatus{State: "On"}
		if err := actor(t, config, scheme, writer).Status().Update(ctx, current); err == nil {
			t.Fatal("a power change was admitted without the webhook")
		}
	})
}

// TestStandalonePowerPolicyAgainstAPIServer applies the standalone
// ValidatingAdmissionPolicy. The smoke runner runs the power projector there,
// so its identity is the writer.
func TestStandalonePowerPolicyAgainstAPIServer(t *testing.T) {
	config, admin, scheme := start(t)
	probe := powered(t, admin, admin, "probe-example")
	data, err := os.ReadFile(filepath.Join(root, "config", "standalone", "machine-power.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	for _, object := range []client.Object{policy, binding} {
		if err := decoder.Decode(object); err != nil {
			t.Fatal(err)
		}
		if err := admin.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, config, admin, scheme, probe)
	exercise(t, config, admin, scheme, "system:serviceaccount:fleet-system:fleet-standalone-smoke")
}
