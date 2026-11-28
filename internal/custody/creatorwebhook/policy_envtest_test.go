package creatorwebhook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// TestStandaloneCreatorPolicyAgainstAPIServer applies the packaged standalone
// creator policy, which enforces the webhook's rule on a plain cluster.
func TestStandaloneCreatorPolicyAgainstAPIServer(t *testing.T) {
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
	data, err := os.ReadFile(filepath.Join(root, "config", "standalone", "site.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, document := range bytes.Split(data, []byte("\n---\n")) {
		var meta metav1.TypeMeta
		if err := yaml.Unmarshal(document, &meta); err != nil {
			t.Fatal(err)
		}
		var object client.Object
		switch meta.Kind {
		case "ValidatingAdmissionPolicy":
			object = &admissionregistrationv1.ValidatingAdmissionPolicy{}
		case "ValidatingAdmissionPolicyBinding":
			object = &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
		default:
			continue
		}
		if err := yaml.UnmarshalStrict(document, object); err != nil {
			t.Fatal(err)
		}
		if err := admin.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
		applied++
	}
	if applied != 2 {
		t.Fatalf("the standalone site must package the creator policy and its binding, found %d objects", applied)
	}
	actor := func(name, uid string) client.Client {
		cfg := rest.CopyConfig(config)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: name, UID: uid, Groups: []string{"system:masters"}}
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
	staff := actor("staff-example", "staff-example-uid")
	// Some credentials, such as client certificates, carry no UID.
	service := actor("service-example", "")
	ctx := context.Background()
	// The policy may take a moment to engage.
	deadline := time.Now().Add(15 * time.Second)
	for !apierrors.IsInvalid(staff.Create(ctx, recovery(ops.Creator{Username: "probe", UID: "probe"}), client.DryRunAll)) {
		if time.Now().After(deadline) {
			t.Fatal("creator policy did not engage")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cases := map[string]struct {
		caller  client.Client
		creator ops.Creator
		allowed bool
	}{
		"a caller with a UID naming itself":     {staff, ops.Creator{Username: "staff-example", UID: "staff-example-uid"}, true},
		"a caller with a UID forging it":        {staff, ops.Creator{Username: "staff-example", UID: "forged"}, false},
		"a caller with a UID omitting it":       {staff, ops.Creator{Username: "staff-example"}, false},
		"a caller without a UID naming itself":  {service, ops.Creator{Username: "service-example"}, true},
		"a caller without a UID adding one":     {service, ops.Creator{Username: "service-example", UID: "forged"}, false},
		"a caller without a UID naming another": {service, ops.Creator{Username: "staff-example"}, false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			err := test.caller.Create(ctx, recovery(test.creator), client.DryRunAll)
			if test.allowed && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !test.allowed && !apierrors.IsInvalid(err) {
				t.Fatalf("not refused by the policy: %v", err)
			}
		})
	}
}
