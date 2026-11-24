package devicepower

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

var componentRoot = filepath.Join("..", "..", "config", "components", "device-power")

func decodeFile[T any](t *testing.T, path string) []T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []T
	for {
		var item T
		if err := decoder.Decode(&item); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, item)
	}
}

// TestOperationAPIOnARealAPIServer applies the shipped CRD and requester
// policy to a disposable API server and checks, as real users:
//   - the requester must be the creating user;
//   - the spec is fixed after creation;
//   - the result is set once;
//   - a staff user cannot write status, and the dispatcher cannot create,
//     change or delete an operation.
func TestOperationAPIOnARealAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required for disposable API server validation")
	}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(componentRoot, "crds")}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	if err := admissionv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	policies := decodeFile[admissionv1.ValidatingAdmissionPolicy](t, filepath.Join(componentRoot, "admission", "requester.yaml"))
	bindings := decodeFile[admissionv1.ValidatingAdmissionPolicyBinding](t, filepath.Join(componentRoot, "admission", "requester.yaml"))
	policy, binding := policies[0], bindings[1]
	binding.TypeMeta = metav1.TypeMeta{}
	for _, obj := range []client.Object{&policy, &binding, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: deviceNS + "-api"}}} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	// The Kubernetes RBAC equivalents of the operator and dispatcher roles.
	rules := map[string][]rbacv1.PolicyRule{
		"staff@example.com": {{APIGroups: []string{inventory.GroupVersion.Group}, Resources: []string{"devicepoweroperations"}, Verbs: []string{"create", "get", "list", "watch"}}},
		"dispatcher": {{APIGroups: []string{inventory.GroupVersion.Group}, Resources: []string{"devicepoweroperations"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{inventory.GroupVersion.Group}, Resources: []string{"devicepoweroperations/status"}, Verbs: []string{"update", "patch"}}},
	}
	as := map[string]client.Client{}
	for user, rule := range rules {
		name := strings.Split(user, "@")[0]
		for _, obj := range []client.Object{&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: rule},
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
				Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: user}}}} {
			if err := admin.Create(ctx, obj); err != nil {
				t.Fatal(err)
			}
		}
		impersonated := rest.CopyConfig(config)
		impersonated.Impersonate = rest.ImpersonationConfig{UserName: user}
		if as[user], err = client.New(impersonated, client.Options{Scheme: scheme}); err != nil {
			t.Fatal(err)
		}
	}
	staff, dispatcher := as["staff@example.com"], as["dispatcher"]
	newOp := func(name, requester string) *inventory.DevicePowerOperation {
		op := operation(name, "device-a", inventory.PowerForceRestart)
		op.Namespace, op.UID, op.CreationTimestamp, op.Generation, op.Spec.Requester = deviceNS+"-api", "", metav1.Time{}, 0, requester
		return op
	}
	// The policy may take a moment to be enforced.
	var err2 error
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if err2 = staff.Create(ctx, newOp("forged", "someone-else@example.com")); apierrors.IsInvalid(err2) || apierrors.IsForbidden(err2) {
			break
		}
		if err2 == nil {
			_ = admin.Delete(ctx, newOp("forged", ""))
		}
	}
	if !apierrors.IsInvalid(err2) && !apierrors.IsForbidden(err2) {
		t.Fatalf("a forged requester was admitted: %v", err2)
	}
	op := newOp("restart", "staff@example.com")
	if err := staff.Create(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Create(ctx, newOp("by-dispatcher", "dispatcher")); !apierrors.IsForbidden(err) {
		t.Fatalf("the dispatcher created an operation: %v", err)
	}
	// Staff may not write status or change the request.
	op.Status.Result = inventory.PowerSucceeded
	if err := staff.Status().Update(ctx, op); !apierrors.IsForbidden(err) {
		t.Fatalf("staff wrote status: %v", err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	changed := op.DeepCopyObject().(*inventory.DevicePowerOperation)
	changed.Spec.Action = inventory.PowerForceOff
	if err := admin.Update(ctx, changed); !apierrors.IsInvalid(err) {
		t.Fatalf("the spec was changed: %v", err)
	}
	if err := dispatcher.Delete(ctx, op); !apierrors.IsForbidden(err) {
		t.Fatalf("the dispatcher deleted an operation: %v", err)
	}
	// The dispatcher writes the result once.
	meta.SetStatusCondition(&op.Status.Conditions, metav1.Condition{Type: inventory.ConditionPowerFinished, Status: metav1.ConditionTrue, Reason: ReasonWorkerLost, Message: "synthetic"})
	op.Status.Result = inventory.PowerUnknown
	if err := dispatcher.Status().Update(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.Status.Result = inventory.PowerSucceeded
	if err := dispatcher.Status().Update(ctx, op); !apierrors.IsInvalid(err) {
		t.Fatalf("the result was changed: %v", err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	op.Status.Result = ""
	if err := dispatcher.Status().Update(ctx, op); !apierrors.IsInvalid(err) {
		t.Fatalf("the result was removed: %v", err)
	}
}

// The Milo roles keep requesting, dispatching and discovery apart.
func TestShippedRolesSplitRequestFromDispatch(t *testing.T) {
	type role struct {
		Metadata struct{ Name string } `json:"metadata"`
		Spec     struct {
			Inherited []struct{ Name string } `json:"inheritedRoles"`
			Included  []string                `json:"includedPermissions"`
		} `json:"spec"`
	}
	roles := map[string][]string{}
	for _, file := range []string{"operator.yaml", "dispatcher.yaml"} {
		for _, r := range decodeFile[role](t, filepath.Join(componentRoot, "roles", file)) {
			if len(r.Spec.Inherited) != 0 {
				t.Fatalf("%s inherits roles", r.Metadata.Name)
			}
			roles[r.Metadata.Name] = r.Spec.Included
		}
	}
	operator, dispatcher := roles["fleet.datumapis.com-device-power-operator"], roles["fleet.datumapis.com-device-power-dispatcher"]
	for _, p := range operator {
		if strings.Contains(p, "status") || strings.HasSuffix(p, ".update") || strings.HasSuffix(p, ".patch") || strings.HasSuffix(p, ".delete") {
			t.Fatalf("operator role holds %s", p)
		}
	}
	if !slices.Contains(operator, "fleet.datumapis.com/devicepoweroperations.create") {
		t.Fatal("operator role cannot create operations")
	}
	for _, p := range dispatcher {
		if strings.HasSuffix(p, ".create") || strings.HasSuffix(p, ".delete") || p == "fleet.datumapis.com/devicepoweroperations.update" ||
			p == "fleet.datumapis.com/devicepoweroperations.patch" || strings.HasPrefix(p, "fleet.datumapis.com/devices") && !strings.HasSuffix(p, ".get") && !strings.HasSuffix(p, ".list") && !strings.HasSuffix(p, ".watch") {
			t.Fatalf("dispatcher role holds %s", p)
		}
	}
	for _, need := range []string{"fleet.datumapis.com/devicepoweroperations/status.update", "metal.datumapis.com/machinecapacities.list"} {
		if !slices.Contains(dispatcher, need) {
			t.Fatalf("dispatcher role lacks %s", need)
		}
	}
}
