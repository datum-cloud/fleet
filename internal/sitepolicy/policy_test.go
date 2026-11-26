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
	// The Job policy reads the site image from its parameter.
	e.apply(object(t, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"fleet-site-worker-policy","namespace":"fleet-site"},"data":{"image":"site-image"}}`))
	// A new policy admits everything until it is compiled; nothing counts
	// as admitted until a known-refused request is refused by the policy.
	control := dryRun(object(t, fmt.Sprintf(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":{"name":"w-%s","namespace":%q},"rules":[]}`, handoff, coordination)))
	if got := e.outcome(control, "policy"); got != "policy" {
		t.Fatalf("the policies never took effect: %s", got)
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

// weaken replaces one clause, an exact substring of exactly one
// validation's expression, by true, and returns a function that restores
// the policy. Weakening a single clause, not a whole validation, shows that
// this clause is what refuses the probe.
func (e *env) weaken(name, clause string) func() {
	e.t.Helper()
	policy := e.policy(name)
	original := policy.DeepCopy()
	original.SetResourceVersion("")
	validations, _, _ := unstructured.NestedSlice(policy.Object, "spec", "validations")
	found := 0
	for index, validation := range validations {
		rule := validation.(map[string]any)
		expression := fmt.Sprint(rule["expression"])
		if n := strings.Count(expression, clause); n > 0 {
			found += n
			rule["expression"] = strings.ReplaceAll(expression, clause, "true")
			validations[index] = rule
		}
	}
	if found != 1 {
		e.t.Fatalf("%s has %d copies of the clause %q, want exactly one", name, found, clause)
	}
	_ = unstructured.SetNestedSlice(policy.Object, validations, "spec", "validations")
	if err := e.admin.Update(e.ctx, policy); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		current := e.policy(name)
		restored := current.DeepCopy()
		spec, _, _ := unstructured.NestedMap(original.Object, "spec")
		restored.Object["spec"] = spec
		if err := e.admin.Update(e.ctx, restored); err != nil {
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
	ownHandoff    = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationhandoffs"],"resourceNames":["` + handoff + `"],"verbs":["get"]}`
	ownPayload    = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationpayloads"],"resourceNames":["` + handoff + `-payload"],"verbs":["get"]}`
	otherPayload  = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationpayloads"],"resourceNames":["op-other-payload"],"verbs":["get"]}`
	anyPayload    = `{"apiGroups":["coordination.fleet.datumapis.com"],"resources":["machineallocationpayloads"],"verbs":["get"]}`
	ownCredential = `{"apiGroups":[""],"resources":["secrets"],"resourceNames":["bmc-0123456789abcdef0123"],"verbs":["get"]}`
	anyCredential = `{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}`
	// Clauses of the rendered policies, each refusing one probe.
	sizeClause         = "size(object.rules) == 2"
	payloadNameClause  = "object.rules[1].resourceNames == [variables.handoff + '-payload']"
	roleRefKindClause  = "object.roleRef.kind == 'Role'"
	ownRoleClause      = "object.roleRef.name == object.metadata.name"
	oneSubjectClause   = "size(object.subjects) == 1"
	subjectKindClause  = "object.subjects[0].kind == 'ServiceAccount'"
	subjectNameClause  = "object.subjects[0].name == object.metadata.name"
	subjectSpaceClause = "object.subjects[0].namespace == 'fleet-site'"
	clusterRBACClause  = "false"
	envClause          = "!has(c.envFrom)"
	secretKeyClause    = "!has(e.valueFrom.secretKeyRef)"
	commandClause      = "c.command[0] == 'fleet-multiplane'"
	ownHandoffClause   = "c.command[3] == variables.handoff"
	argsClause         = "!has(c.args)"
	credentialSize     = "size(object.rules) == 1"
	payloadNamedClause = "has(object.rules[1].resourceNames)"
)

// payloadRuleClauses are every clause about a worker Role's second rule.
var payloadRuleClauses = []string{
	"object.rules[1].apiGroups == ['coordination.fleet.datumapis.com']",
	"object.rules[1].resources == ['machineallocationpayloads']",
	"object.rules[1].verbs == ['get']",
	payloadNamedClause,
	payloadNameClause,
	"!has(object.rules[1].nonResourceURLs)",
}

// credentialName is the Redfish credential Role's Secret name clause.
const credentialName = "n.matches('^bmc-[a-f0-9]{20}$')"

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
	// workerElsewhere has the worker's name in kro's namespace.
	workerElsewhere = `{"kind":"ServiceAccount","name":"w-` + handoff + `","namespace":"kro-system"}`
)

// bindingTo binds the subjects to the named Role.
func bindingTo(t *testing.T, namespace, role string, subjects ...string) *unstructured.Unstructured {
	return object(t, fmt.Sprintf(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleBinding","metadata":{"name":"w-%s","namespace":%q},"subjects":[%s],
		"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":%q}}`, handoff, namespace, strings.Join(subjects, ","), role))
}

type probe struct {
	name string
	send func(context.Context, client.Client) error
	// policy and clauses name the clauses that refuse the probe. Several
	// clauses are named only where each alone refuses it, or where one
	// cannot be evaluated without the other.
	policy  string
	clauses []string
}

// check runs each probe: refused by the named rule as rendered, and
// admitted once that rule alone is removed.
func (e *env) check(probes []probe) {
	for _, p := range probes {
		e.t.Run(p.name, func(t *testing.T) {
			if got := e.outcome(p.send, "policy"); got != "policy" {
				t.Fatalf("as rendered: %s, want refused by policy", got)
			}
			var restores []func()
			for _, clause := range p.clauses {
				restores = append(restores, e.weaken(p.policy, clause))
			}
			defer func() {
				for i := len(restores) - 1; i >= 0; i-- {
					restores[i]()
				}
			}()
			if got := e.outcome(p.send, "admitted"); got != "admitted" {
				t.Fatalf("without %q: %s, want admitted; the probe does not test those clauses", p.clauses, got)
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
		// Without a second rule, no clause about it can be evaluated, so all
		// of them and the rule count refuse it together.
		{"a Role without its payload rule", dryRun(role(t, coordination, ownHandoff)), identity, append([]string{sizeClause}, payloadRuleClauses...)},
		{"a Role reading another operation's payload", dryRun(role(t, coordination, ownHandoff, otherPayload)), identity, []string{payloadNameClause}},
		{"a Role reading every payload", dryRun(role(t, coordination, ownHandoff, anyPayload)), identity, []string{payloadNamedClause, payloadNameClause}},
		{"a Role with a third rule", dryRun(role(t, coordination, ownHandoff, ownPayload, ownHandoff)), identity, []string{sizeClause}},
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
		{"a binding to kro's own ServiceAccount", dryRun(binding(t, coordination, kroAccount)), identity, []string{subjectNameClause, subjectSpaceClause}},
		{"a binding to another operation's worker", dryRun(binding(t, coordination, otherWorker)), identity, []string{subjectNameClause}},
		{"a binding to a worker name in another namespace", dryRun(binding(t, coordination, workerElsewhere)), identity, []string{subjectSpaceClause}},
		{"a binding to kro as a user", dryRun(binding(t, coordination, kroUser)), identity, []string{subjectKindClause, subjectNameClause, subjectSpaceClause}},
		{"a binding to every ServiceAccount", dryRun(binding(t, coordination, everyone)), identity, []string{subjectKindClause, subjectNameClause, subjectSpaceClause}},
		{"a binding to its worker and kro", dryRun(binding(t, coordination, worker, kroAccount)), identity, []string{oneSubjectClause}},
		// kro holds every permission of its own Role, so RBAC admits binding
		// a worker to it, and only the own-Role clause refuses it.
		{"a binding of the worker to kro's own Role", dryRun(bindingTo(t, coordination, "fleet-site-kro-worker-rbac", worker)), identity, []string{ownRoleClause}},
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
		{"widening the Role to every payload", widen(ownHandoff, anyPayload), identity, []string{payloadNamedClause, payloadNameClause}},
		{"widening the Role to another payload", widen(ownHandoff, otherPayload), identity, []string{payloadNameClause}},
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
		{"a ClusterRole, with a grant", dryRun(clusterRole), "fleet-site-kro-cluster-rbac", []string{clusterRBACClause}},
		{"a ClusterRoleBinding, with a grant", dryRun(clusterBinding), "fleet-site-kro-cluster-rbac", []string{clusterRBACClause}},
	})
}

// job is a worker Job for the handoff, as the graph renders it, with one
// change applied to its container.
func job(t *testing.T, change string) *unstructured.Unstructured {
	container := `"name":"worker","image":"site-image","command":["fleet-multiplane","site-worker","--handoff","` + handoff + `","--cell","cell-example"],
		"env":[{"name":"FLEET_SITE_ENROLLMENT","valueFrom":{"configMapKeyRef":{"name":"fleet-site-enrollment","key":"enrollment.json"}}}],
		"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}`
	if change != "" {
		container = change
	}
	return object(t, `{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"w-`+handoff+`","namespace":"fleet-site","labels":{"fleet.datumapis.com/handoff":"`+handoff+`"}},
		"spec":{"template":{"spec":{"restartPolicy":"Never","serviceAccountName":"w-`+handoff+`","containers":[{`+container+`}]}}}}`)
}

// TestKroJobsReadNoSecretAndRunOnlyTheWorker: the kubelet resolves a Pod's
// Secret references whatever its ServiceAccount may read, so a Job kro
// writes takes environment only from literals and the site enrollment, and
// runs only the site worker for its own handoff.
func TestKroJobsReadNoSecretAndRunOnlyTheWorker(t *testing.T) {
	e := start(t, filepath.Join("config", "site", "base"))
	jobs := "fleet-site-kro-worker-jobs"
	safe := `"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}`
	command := func(args ...string) string {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = fmt.Sprintf("%q", a)
		}
		return `"name":"worker","image":"site-image","command":[` + strings.Join(quoted, ",") + `],` + safe
	}
	if got := e.outcome(dryRun(job(t, "")), "admitted"); got != "admitted" {
		t.Fatalf("the worker Job: %s", got)
	}
	e.check([]probe{
		{"a Job taking environment from a Secret", dryRun(job(t, command("fleet-multiplane", "site-worker", "--handoff", handoff, "--cell", "c")+`,"envFrom":[{"secretRef":{"name":"fleet-site-bmc-credentials"}}]`)), jobs, []string{envClause}},
		{"a Job reading one Secret key", dryRun(job(t, command("fleet-multiplane", "site-worker", "--handoff", handoff, "--cell", "c")+`,"env":[{"name":"X","valueFrom":{"secretKeyRef":{"name":"fleet-site-bmc-credentials","key":"password"}}}]`)), jobs,
			[]string{"has(e.valueFrom.configMapKeyRef) && e.valueFrom.configMapKeyRef.name == 'fleet-site-enrollment'", secretKeyClause}},
		{"a Job running another program", dryRun(job(t, command("sh", "site-worker", "--handoff", handoff, "--cell", "c"))), jobs, []string{commandClause}},
		{"a Job for another handoff", dryRun(job(t, command("fleet-multiplane", "site-worker", "--handoff", "op-other", "--cell", "c"))), jobs, []string{ownHandoffClause}},
		{"a Job with arguments", dryRun(job(t, command("fleet-multiplane", "site-worker", "--handoff", handoff, "--cell", "c")+`,"args":["--extra"]`)), jobs, []string{argsClause}},
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
		{"a credential Role with a second rule", dryRun(role(t, credentials, ownCredential, ownCredential)), identity, []string{credentialSize}},
		{"a credential Role naming another Secret", dryRun(role(t, credentials, strings.Replace(ownCredential, "bmc-0123456789abcdef0123", "fleet-site-bmc-credentials", 1))), identity, []string{credentialName}},
		{"a worker Role reading every payload", dryRun(role(t, coordination, ownHandoff, anyPayload)), identity, []string{payloadNamedClause, payloadNameClause}},
		// kro holds get on every BMC credential to grant one, so only the
		// own-Role clause refuses binding a worker to kro's Role.
		{"a binding of the worker to kro's credential Role", dryRun(bindingTo(t, credentials, "fleet-site-kro-worker-credentials", worker)), identity, []string{ownRoleClause}},
	})
	if got := e.outcome(dryRun(role(t, credentials, anyCredential)), "rbac"); got != "policy" && got != "rbac" {
		t.Errorf("a credential Role reading every Secret: %s", got)
	}
}
