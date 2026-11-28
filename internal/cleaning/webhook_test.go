package cleaning

import (
	"context"
	"encoding/json"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func review(t *testing.T, operation admissionv1.Operation, resource string, object any) admission.Request {
	t.Helper()
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: operation,
		Resource:  metav1.GroupVersionResource{Group: ops.GroupVersion.Group, Version: ops.GroupVersion.Version, Resource: resource},
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

func TestWebhook(t *testing.T) {
	valid := &ops.CleaningPolicy{TypeMeta: metav1.TypeMeta{APIVersion: ops.GroupVersion.String(), Kind: "CleaningPolicy"}, ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"}, Spec: purgePolicy()}
	fallback := valid.DeepCopyObject().(*ops.CleaningPolicy)
	fallback.Spec.Rules[0].Methods = append(fallback.Spec.Rules[0].Methods, ops.MethodOverwrite)
	unknown := map[string]any{"apiVersion": ops.GroupVersion.String(), "kind": "CleaningPolicy", "metadata": map[string]any{"name": "p"},
		"spec": map[string]any{"assurance": "Clear", "rules": []any{}, "extra": true}}
	cases := []struct {
		name    string
		request admission.Request
		allowed bool
	}{
		{"valid create", review(t, admissionv1.Create, "cleaningpolicies", valid), true},
		{"update", review(t, admissionv1.Update, "cleaningpolicies", valid), false},
		{"fallback below assurance", review(t, admissionv1.Create, "cleaningpolicies", fallback), false},
		{"unknown field", review(t, admissionv1.Create, "cleaningpolicies", unknown), false},
		{"another resource", review(t, admissionv1.Create, "devicewipes", valid), false},
		{"delete", review(t, admissionv1.Delete, "cleaningpolicies", valid), false},
	}
	for _, tc := range cases {
		if got := (Webhook{}).Handle(context.Background(), tc.request); got.Allowed != tc.allowed {
			t.Errorf("%s: allowed = %v, want %v (%v)", tc.name, got.Allowed, tc.allowed, got.Result)
		}
	}
}
