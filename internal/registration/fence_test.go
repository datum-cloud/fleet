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
	for index := range configured.Webhooks {
		configured.Webhooks[index].ClientConfig.CABundle = []byte("environment-owned-serving-ca")
	}
	for _, test := range []struct {
		name    string
		mutate  func(*admissionv1.ValidatingWebhookConfiguration)
		allowed bool
	}{
		{"complete", func(*admissionv1.ValidatingWebhookConfiguration) {}, true},
		{"incomplete deployment", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks = configuration.Webhooks[:1]
		}, false},
		{"no serving CA", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[0].ClientConfig.CABundle = nil
		}, false},
		{"fail open", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			policy := admissionv1.Ignore
			configuration.Webhooks[0].FailurePolicy = &policy
		}, false},
		{"missing Device protection", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[1].Rules = nil
		}, false},
		{"global inventory blast radius", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[1].MatchConditions = nil
		}, false},
		{"broadened inventory rule", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[0].Rules = append(configuration.Webhooks[0].Rules, configuration.Webhooks[1].Rules...)
		}, false},
		{"wrong retained project scope", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[1].MatchConditions = requiredFences([]string{"different"})[1].MatchConditions
		}, false},
		{"namespace name omitted", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[2].MatchConditions = configuration.Webhooks[2].MatchConditions[:1]
		}, false},
		{"global project deletion blast radius", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[3].MatchConditions = nil
		}, false},
		{"caller label condition", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[1].MatchConditions[0].Expression = "object.metadata.labels['provider'] == 'datum-cloud'"
		}, false},
		{"Device status protection omitted", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[1].Rules[0].Resources = []string{"devices"}
		}, false},
		{"inventory fail open", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			policy := admissionv1.Ignore
			configuration.Webhooks[1].FailurePolicy = &policy
		}, false},
		{"capacity exemption", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			configuration.Webhooks[0].MatchConditions = configuration.Webhooks[1].MatchConditions
		}, false},
		{"split authority", func(configuration *admissionv1.ValidatingWebhookConfiguration) {
			other := "https://another.invalid/webhook"
			configuration.Webhooks[1].ClientConfig.URL = &other
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
			if err := VerifyFence(context.Background(), api, []string{"datum-cloud"}); (err == nil) != test.allowed {
				t.Fatalf("unexpected fence result: %v", err)
			}
		})
	}
}

func TestIncompleteFenceDeniesRegistrationWithoutReservation(t *testing.T) {
	handler, device := fixture(t)
	scheme := runtime.NewScheme()
	if err := admissionv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configuration := &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "fleet-capacity-registration"}}
	api := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configuration).Build()
	handler.VerifyFence = func(ctx context.Context) error { return VerifyFence(ctx, api, handler.Store.Projects) }
	if handler.Handle(context.Background(), registrationRequest(t, device, "incomplete")).Allowed {
		t.Fatal("incomplete admission deployment admitted registration")
	}
	_, ledger, err := handler.Store.Read(context.Background())
	if err != nil || len(ledger.Entries) != 0 {
		t.Fatalf("incomplete fence changed registration state: %+v %v", ledger, err)
	}
}
