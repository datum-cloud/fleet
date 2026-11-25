// Package sitepolicy tests the site's admission policies and kro's RBAC
// against a real API server. kro creates every per-operation Role and
// RoleBinding at a site, so these policies, not kro, decide what a worker
// may read. Each case also runs with the one policy rule it tests removed,
// and must then be admitted: a probe that passes without its rule proves
// nothing.
package sitepolicy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	kro          = "system:serviceaccount:kro-system:kro"
	coordination = "fleet-coordination"
	credentials  = "fleet-site-credentials"
	handoff      = "op-" + "0123456789abcdef0123456789abcdef01234567"
)

var root = filepath.Join("..", "..")

// render builds a kustomization with the kustomize binary CI installs.
func render(t *testing.T, dir string) []*unstructured.Unstructured {
	t.Helper()
	path, err := exec.LookPath("kustomize")
	if err != nil {
		t.Fatal("kustomize is required: the policies are tested as rendered")
	}
	output, err := exec.Command(path, "build", filepath.Join(root, dir)).Output()
	if err != nil {
		t.Fatalf("kustomize build %s: %v", dir, err)
	}
	return decode(t, output)
}

func decode(t *testing.T, data []byte) []*unstructured.Unstructured {
	t.Helper()
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(&object.Object); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		if len(object.Object) != 0 {
			out = append(out, object)
		}
	}
}

func object(t *testing.T, text string) *unstructured.Unstructured {
	t.Helper()
	objects := decode(t, []byte(text))
	if len(objects) != 1 {
		t.Fatalf("want one object in %s", text)
	}
	return objects[0]
}

type env struct {
	t     *testing.T
	ctx   context.Context
	admin client.Client
	kro   client.Client
}

// start runs an API server with the coordination CRDs and one rendered site
// base, and binds kro's cluster role as the kro chart's aggregation does.
func start(t *testing.T, base string) *env {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	ctrllog.SetLogger(logr.Discard())
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(root, "config", "multiplane", "coordination", "crd.yaml")}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = environment.Stop() })
	scheme := runtime.NewScheme()
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	impersonated := rest.CopyConfig(config)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: kro, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:kro-system", "system:authenticated"}}
	kroClient, err := client.New(impersonated, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: context.Background(), admin: admin, kro: kroClient}
	for _, namespace := range []string{"kro-system", "flux-system", coordination, credentials} {
		e.apply(object(t, fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q}}`, namespace)))
	}
	for _, rendered := range render(t, base) {
		if rendered.GetKind() == "StorageClass" || rendered.GetKind() == "PersistentVolumeClaim" {
			continue
		}
		e.apply(rendered)
	}
	e.apply(object(t, `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRoleBinding","metadata":{"name":"kro-aggregated"},
		"subjects":[{"kind":"ServiceAccount","name":"kro","namespace":"kro-system"}],
		"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"fleet-site-kro"}}`))
	// RBAC is the API server's authorizer here, as on a site.
	if err := e.kro.Create(e.ctx, object(t, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"probe","namespace":"kube-system"}}`)); !apierrors.IsForbidden(err) {
		t.Fatalf("the API server does not authorize kro by RBAC: %v", err)
	}
	return e
}

func (e *env) apply(object *unstructured.Unstructured) {
	e.t.Helper()
	object = object.DeepCopy()
	if err := e.admin.Create(e.ctx, object); apierrors.IsAlreadyExists(err) {
		existing := object.DeepCopy()
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(object), existing); err != nil {
			e.t.Fatal(err)
		}
		object.SetResourceVersion(existing.GetResourceVersion())
		err = e.admin.Update(e.ctx, object)
		if err != nil {
			e.t.Fatal(err)
		}
	} else if err != nil {
		e.t.Fatalf("apply %s %s: %v", object.GetKind(), object.GetName(), err)
	}
}

// policy returns a live policy, for a case to weaken one rule.
func (e *env) policy(name string) *unstructured.Unstructured {
	e.t.Helper()
	policy := &unstructured.Unstructured{}
	policy.SetAPIVersion("admissionregistration.k8s.io/v1")
	policy.SetKind("ValidatingAdmissionPolicy")
	if err := e.admin.Get(e.ctx, client.ObjectKey{Name: name}, policy); err != nil {
		e.t.Fatal(err)
	}
	return policy
}

// weaken replaces the validation whose message starts with prefix by one
// that always passes, and returns a function that restores the policy.
func (e *env) weaken(name, prefix string) func() {
	e.t.Helper()
	policy := e.policy(name)
	original := policy.DeepCopy()
	validations, _, _ := unstructured.NestedSlice(policy.Object, "spec", "validations")
	found := false
	for index, validation := range validations {
		rule := validation.(map[string]any)
		if strings.HasPrefix(fmt.Sprint(rule["message"]), prefix) {
			rule["expression"], found = "true", true
			validations[index] = rule
		}
	}
	if !found {
		e.t.Fatalf("%s has no validation %q", name, prefix)
	}
	_ = unstructured.SetNestedSlice(policy.Object, validations, "spec", "validations")
	if err := e.admin.Update(e.ctx, policy); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		current := e.policy(name)
		original.SetResourceVersion(current.GetResourceVersion())
		if err := e.admin.Update(e.ctx, original); err != nil {
			e.t.Fatal(err)
		}
	}
}

// outcome sends a request as kro, retrying while a just-changed policy has
// not yet taken effect, and reports whether it was admitted and which layer
// refused it.
func (e *env) outcome(send func(context.Context, client.Client) error, want string) string {
	e.t.Helper()
	got := ""
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		err := send(e.ctx, e.kro)
		switch {
		case err == nil:
			got = "admitted"
		case strings.Contains(err.Error(), "ValidatingAdmissionPolicy"):
			got = "policy"
		case apierrors.IsForbidden(err):
			got = "rbac"
		default:
			got = "error: " + err.Error()
		}
		if got == want {
			return got
		}
	}
	return got
}

func dryRun(object *unstructured.Unstructured) func(context.Context, client.Client) error {
	return func(ctx context.Context, c client.Client) error {
		return c.Create(ctx, object.DeepCopy(), client.DryRunAll)
	}
}

func role(t *testing.T, namespace string, rules ...string) *unstructured.Unstructured {
	return object(t, fmt.Sprintf(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":{"name":"w-%s","namespace":%q},"rules":[%s]}`, handoff, namespace, strings.Join(rules, ",")))
}

const (
	ownHandoff     = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationhandoffs"],"resourceNames":["` + handoff + `"],"verbs":["get"]}`
	ownPayload     = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationpayloads"],"resourceNames":["` + handoff + `-payload"],"verbs":["get"]}`
	otherPayload   = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationpayloads"],"resourceNames":["op-other-payload"],"verbs":["get"]}`
	anyPayload     = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationpayloads"],"verbs":["get"]}`
	ownCredential  = `{"apiGroups":[""],"resources":["secrets"],"resourceNames":["bmc-0123456789abcdef0123"],"verbs":["get"]}`
	anyCredential  = `{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}`
	roleRuleText   = "a worker Role may grant only"
	bindingRule    = "a worker RoleBinding may bind only"
	clusterRBACMsg = "kro may not write cluster-wide RBAC"
)

func binding(t *testing.T, namespace string, subjects ...string) *unstructured.Unstructured {
	return object(t, fmt.Sprintf(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleBinding","metadata":{"name":"w-%s","namespace":%q},"subjects":[%s],
		"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"w-%s"}}`, handoff, namespace, strings.Join(subjects, ","), handoff))
}

const (
	worker      = `{"kind":"ServiceAccount","name":"w-` + handoff + `","namespace":"fleet-site"}`
	otherWorker = `{"kind":"ServiceAccount","name":"w-op-other","namespace":"fleet-site"}`
	kroAccount  = `{"kind":"ServiceAccount","name":"kro","namespace":"kro-system"}`
	kroUser     = `{"kind":"User","apiGroup":"rbac.authorization.k8s.io","name":"` + kro + `"}`
	everyone    = `{"kind":"Group","apiGroup":"rbac.authorization.k8s.io","name":"system:serviceaccounts"}`
)

type probe struct {
	name string
	send func(context.Context, client.Client) error
	// policy and rule name the one validation that must refuse the probe.
	policy, rule string
}

// check runs each probe: refused by the named rule as rendered, and
// admitted once that rule alone is removed.
func (e *env) check(probes []probe) {
	for _, p := range probes {
		e.t.Run(p.name, func(t *testing.T) {
			if got := e.outcome(p.send, "policy"); got != "policy" {
				t.Fatalf("as rendered: %s, want refused by policy", got)
			}
			restore := e.weaken(p.policy, p.rule)
			defer restore()
			if got := e.outcome(p.send, "admitted"); got != "admitted" {
				t.Fatalf("without %q: %s, want admitted; the probe does not test that rule", p.rule, got)
			}
		})
	}
}

// TestKroCanCreateOnlyPerOperationRBAC covers the four bounds on kro's RBAC
// writes at a site: Role shape, binding subject, no cluster-wide RBAC, and
// no widening of an existing Role.
func TestKroCanCreateOnlyPerOperationRBAC(t *testing.T) {
	e := start(t, filepath.Join("config", "site", "base"))
	identity := "fleet-site-kro-worker-identity"

	// The per-operation shapes are admitted.
	for name, admitted := range map[string]*unstructured.Unstructured{
		"the worker Role": role(t, coordination, ownHandoff, ownPayload),
	} {
		if got := e.outcome(dryRun(admitted), "admitted"); got != "admitted" {
			t.Fatalf("%s: %s", name, got)
		}
	}

	// 1. Role shape. Each Role is within kro's own grant, so only the
	// policy can refuse it.
	e.check([]probe{
		{"a Role without its payload rule", dryRun(role(t, coordination, ownHandoff)), identity, roleRuleText},
		{"a Role reading another operation's payload", dryRun(role(t, coordination, ownHandoff, otherPayload)), identity, roleRuleText},
		{"a Role reading every payload", dryRun(role(t, coordination, ownHandoff, anyPayload)), identity, roleRuleText},
		{"a Role with a third rule", dryRun(role(t, coordination, ownHandoff, ownPayload, ownHandoff)), identity, roleRuleText},
	})
	// Shapes outside kro's own grant are refused by RBAC before admission.
	for name, outside := range map[string]*unstructured.Unstructured{
		"a Role reading Secrets":  role(t, coordination, ownHandoff, anyCredential),
		"a Role listing payloads": role(t, coordination, ownHandoff, strings.Replace(ownPayload, `"verbs":["get"]`, `"verbs":["get","list"]`, 1)),
	} {
		if got := e.outcome(dryRun(outside), "rbac"); got != "rbac" {
			t.Errorf("%s: %s, want refused by RBAC", name, got)
		}
	}

	// 2. Binding subject. The per-operation Role exists, so RBAC resolves
	// it and only the subject is wrong.
	if err := e.kro.Create(e.ctx, role(t, coordination, ownHandoff, ownPayload)); err != nil {
		t.Fatal(err)
	}
	if got := e.outcome(dryRun(binding(t, coordination, worker)), "admitted"); got != "admitted" {
		t.Fatalf("the worker binding: %s", got)
	}
	e.check([]probe{
		{"a binding to kro's own ServiceAccount", dryRun(binding(t, coordination, kroAccount)), identity, bindingRule},
		{"a binding to another operation's worker", dryRun(binding(t, coordination, otherWorker)), identity, bindingRule},
		{"a binding to kro as a user", dryRun(binding(t, coordination, kroUser)), identity, bindingRule},
		{"a binding to every ServiceAccount", dryRun(binding(t, coordination, everyone)), identity, bindingRule},
		{"a binding to its worker and kro", dryRun(binding(t, coordination, worker, kroAccount)), identity, bindingRule},
	})

	// 4. No widening: an update of kro's own Role passes the same rule.
	widen := func(rules ...string) func(context.Context, client.Client) error {
		return func(ctx context.Context, c client.Client) error {
			current := role(t, coordination)
			if err := c.Get(ctx, client.ObjectKeyFromObject(current), current); err != nil {
				return err
			}
			updated := role(t, coordination, rules...)
			updated.SetResourceVersion(current.GetResourceVersion())
			return c.Update(ctx, updated, client.DryRunAll)
		}
	}
	e.check([]probe{
		{"widening the Role to every payload", widen(ownHandoff, anyPayload), identity, roleRuleText},
		{"widening the Role to another payload", widen(ownHandoff, otherPayload), identity, roleRuleText},
	})

	// 3. No cluster-wide RBAC. kro holds no such grant, so RBAC refuses it;
	// the policy refuses it too, which a temporary grant shows.
	clusterRole := object(t, `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRole","metadata":{"name":"w-probe"},"rules":[`+anyCredential+`]}`)
	clusterBinding := object(t, `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRoleBinding","metadata":{"name":"w-probe"},"subjects":[`+kroAccount+`],"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"view"}}`)
	for name, cluster := range map[string]*unstructured.Unstructured{"a ClusterRole": clusterRole, "a ClusterRoleBinding to kro": clusterBinding} {
		if got := e.outcome(dryRun(cluster), "rbac"); got != "rbac" {
			t.Errorf("%s: %s, want refused by RBAC", name, got)
		}
	}
	e.apply(object(t, `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRole","metadata":{"name":"probe-cluster-rbac"},
		"rules":[{"apiGroups":["rbac.authorization.k8s.io"],"resources":["clusterroles","clusterrolebindings"],"verbs":["create","escalate","bind"]},{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}]}`))
	e.apply(object(t, `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRoleBinding","metadata":{"name":"probe-cluster-rbac"},
		"subjects":[`+kroAccount+`],"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"probe-cluster-rbac"}}`))
	e.check([]probe{
		{"a ClusterRole, with a grant", dryRun(clusterRole), "fleet-site-kro-cluster-rbac", clusterRBACMsg},
		{"a ClusterRoleBinding, with a grant", dryRun(clusterBinding), "fleet-site-kro-cluster-rbac", clusterRBACMsg},
	})
}

// TestRedfishCredentialRolesAreOneGetOnOneSecret covers the Redfish site's
// credential Roles: only get on one named BMC credential Secret.
func TestRedfishCredentialRolesAreOneGetOnOneSecret(t *testing.T) {
	e := start(t, filepath.Join("config", "site", "redfish", "base"))
	identity := "fleet-site-kro-worker-identity"
	if got := e.outcome(dryRun(role(t, credentials, ownCredential)), "admitted"); got != "admitted" {
		t.Fatalf("the credential Role: %s", got)
	}
	if got := e.outcome(dryRun(role(t, coordination, ownHandoff, ownPayload)), "admitted"); got != "admitted" {
		t.Fatalf("the worker Role: %s", got)
	}
	e.check([]probe{
		{"a credential Role with a second rule", dryRun(role(t, credentials, ownCredential, ownCredential)), identity, roleRuleText},
		{"a credential Role naming another Secret", dryRun(role(t, credentials, strings.Replace(ownCredential, "bmc-0123456789abcdef0123", "fleet-site-bmc-credentials", 1))), identity, roleRuleText},
		{"a worker Role reading every payload", dryRun(role(t, coordination, ownHandoff, anyPayload)), identity, roleRuleText},
	})
	if got := e.outcome(dryRun(role(t, credentials, anyCredential)), "rbac"); got != "policy" && got != "rbac" {
		t.Errorf("a credential Role reading every Secret: %s", got)
	}
}
