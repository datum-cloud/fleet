package registration

import (
	"context"
	"os"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func TestPackagedProjectAdmissionFence(t *testing.T) {
	data, err := os.ReadFile("../../config/components/registration-admission/webhook.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var configured admissionv1.ValidatingWebhookConfiguration
	if err := yaml.UnmarshalStrict(data, &configured); err != nil {
		t.Fatal(err)
	}
	configured.Webhooks[0].ClientConfig.CABundle = []byte("environment-owned-serving-ca")
	for _, test := range []struct {
		name    string
		mutate  func(*admissionv1.ValidatingWebhookConfiguration)
		allowed bool
	}{
		{"complete", func(*admissionv1.ValidatingWebhookConfiguration) {}, true},
		{"no serving CA", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[0].ClientConfig.CABundle = nil
		}, false},
		{"fail open", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			policy := admissionv1.Ignore
			configuration.Webhooks[0].FailurePolicy = &policy
		}, false},
		{"missing Device protection", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[0].Rules = configuration.Webhooks[0].Rules[:1]
		}, false},
		{"caller annotation selector", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[0].ObjectSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"registration": "true"}}
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration := configured.DeepCopy()
			test.mutate(configuration)
			scheme := runtime.NewScheme()
			if err := admissionv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			api := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configuration).Build()
			if err := VerifyFence(context.Background(), api); (err == nil) != test.allowed {
				t.Fatalf("unexpected fence result: %v", err)
			}
		})
	}
}
