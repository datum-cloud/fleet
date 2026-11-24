package registration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/admission"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/cel/environment"
)

// deletionPolicy reads the component's ValidatingAdmissionPolicy.
func deletionPolicy(t *testing.T) *admissionregistrationv1.ValidatingAdmissionPolicy {
	t.Helper()
	data, err := os.ReadFile("../../config/components/device-deletion-protection/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var policy admissionregistrationv1.ValidatingAdmissionPolicy
		err := decoder.Decode(&policy)
		if errors.Is(err, io.EOF) {
			t.Fatal("component has no ValidatingAdmissionPolicy")
		}
		if err != nil {
			t.Fatal(err)
		}
		if policy.Kind == "ValidatingAdmissionPolicy" {
			return &policy
		}
	}
}

type deletionInput struct {
	username         string
	groups           []string
	lifecycle        string
	namespaceDeleted bool
	params           *corev1.ConfigMap
}

// TestDeletionPolicyCleanupIdentityOffline evaluates the policy's expression
// with the API server's admission CEL compiler, for inputs envtest cannot
// produce, such as an empty username.
func TestDeletionPolicyCleanupIdentityOffline(t *testing.T) {
	policy := deletionPolicy(t)
	if policy.Spec.ParamKind == nil || policy.Spec.ParamKind.APIVersion != "v1" || policy.Spec.ParamKind.Kind != "ConfigMap" {
		t.Fatalf("paramKind = %+v, want core/v1 ConfigMap", policy.Spec.ParamKind)
	}
	if len(policy.Spec.Validations) != 1 {
		t.Fatalf("validations = %d, want 1", len(policy.Spec.Validations))
	}
	compiler := plugincel.NewConditionCompiler(environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()))
	evaluator := compiler.CompileCondition([]plugincel.ExpressionAccessor{&validating.ValidationCondition{Expression: policy.Spec.Validations[0].Expression}}, plugincel.OptionalVariableDeclarations{HasParams: true}, environment.StoredExpressions)

	listed := func(value string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "synthetic-device-cleanup", Namespace: "synthetic-device-cleanup"}, Data: map[string]string{"cleanupUsernames": value}}
	}
	list := listed(" synthetic-cleanup ,\n\n , \r\nsystem:synthetic-purger\r\n,")
	for _, test := range []struct {
		name  string
		input deletionInput
		want  bool
	}{
		{"no params: listed name is not exempt", deletionInput{username: "synthetic-cleanup", lifecycle: "Active"}, false},
		{"no params: Retired is deletable", deletionInput{username: "synthetic-operator", lifecycle: "Retired"}, true},
		{"no params: deleting namespace", deletionInput{username: "synthetic-operator", lifecycle: "Active", namespaceDeleted: true}, true},
		{"listed name with padding", deletionInput{username: "synthetic-cleanup", lifecycle: "Active", params: list}, true},
		{"listed name after CRLF and blank entries", deletionInput{username: "system:synthetic-purger", lifecycle: "Decommissioning", params: list}, true},
		{"unlisted name", deletionInput{username: "synthetic-operator", lifecycle: "Active", params: list}, false},
		{"prefix of a listed name", deletionInput{username: "synthetic-clean", lifecycle: "Active", params: list}, false},
		{"superset of a listed name", deletionInput{username: "synthetic-cleanup-extra", lifecycle: "Active", params: list}, false},
		{"listed name embedded", deletionInput{username: "xsynthetic-cleanup", lifecycle: "Active", params: list}, false},
		{"case differs", deletionInput{username: "Synthetic-Cleanup", lifecycle: "Active", params: list}, false},
		{"listed name as a group only", deletionInput{username: "synthetic-operator", groups: []string{"synthetic-cleanup"}, lifecycle: "Active", params: list}, false},
		{"empty username against empty entries", deletionInput{username: "", lifecycle: "Active", params: listed(",\n , \n")}, false},
		{"empty username against an empty value", deletionInput{username: "", lifecycle: "Active", params: listed("")}, false},
		{"wildcard entry is literal", deletionInput{username: "synthetic-operator", lifecycle: "Active", params: listed("*")}, false},
		{"ConfigMap without the key", deletionInput{username: "synthetic-cleanup", lifecycle: "Active", params: &corev1.ConfigMap{Data: map[string]string{"other": "synthetic-cleanup"}}}, false},
		{"ConfigMap without data", deletionInput{username: "synthetic-cleanup", lifecycle: "Active", params: &corev1.ConfigMap{}}, false},
		{"params set: Retired still deletable", deletionInput{username: "synthetic-operator", lifecycle: "Retired", params: list}, true},
		{"params set: deleting namespace still deletable", deletionInput{username: "synthetic-operator", lifecycle: "Active", namespaceDeleted: true, params: list}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := evaluateDeletion(t, evaluator, test.input); got != test.want {
				t.Fatalf("admitted = %v, want %v", got, test.want)
			}
		})
	}
}

func evaluateDeletion(t *testing.T, evaluator plugincel.ConditionEvaluator, input deletionInput) bool {
	t.Helper()
	gvk := schema.GroupVersionKind{Group: "fleet.datumapis.com", Version: "v1alpha1", Kind: "Device"}
	gvr := schema.GroupVersionResource{Group: "fleet.datumapis.com", Version: "v1alpha1", Resource: "devices"}
	old := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "metadata": map[string]any{"name": "synthetic-device", "namespace": "default"}, "spec": map[string]any{"lifecycle": input.lifecycle}}}
	caller := &user.DefaultInfo{Name: input.username, Groups: input.groups}
	attributes := admission.NewAttributesRecord(nil, old, gvk, "default", "synthetic-device", gvr, "", admission.Delete, &metav1.DeleteOptions{}, false, caller)
	versioned := &admission.VersionedAttributes{Attributes: attributes, VersionedOldObject: admission.NewLazyObject(old), VersionedKind: gvk}
	request := &admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind(gvk), Resource: metav1.GroupVersionResource(gvr), Name: "synthetic-device", Namespace: "default", Operation: admissionv1.Delete, UserInfo: authenticationv1.UserInfo{Username: input.username, Groups: input.groups}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	if input.namespaceDeleted {
		now := metav1.Now()
		namespace.DeletionTimestamp = &now
	}
	var params runtime.Object
	if input.params != nil {
		params = input.params
	}
	results, _, err := evaluator.ForInput(context.Background(), versioned, request, plugincel.OptionalVariableBindings{VersionedParams: params}, namespace, celconfig.RuntimeCELCostBudget)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("evaluation failed: %+v", results)
	}
	admitted, ok := results[0].EvalResult.Value().(bool)
	if !ok {
		t.Fatalf("expression returned %v, want a bool", results[0].EvalResult)
	}
	return admitted
}
