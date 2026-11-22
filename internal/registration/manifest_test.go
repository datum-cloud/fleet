package registration

import (
	"os"
	"slices"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

// The packaged webhook is the only gate on capacity writes, so it must stay
// fail-closed over every capacity write and must not reach anything else.
func TestPackagedWebhookFencesOnlyCapacityWrites(t *testing.T) {
	data, err := os.ReadFile("../../config/components/registration-admission/webhook.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var configuration admissionregistrationv1.ValidatingWebhookConfiguration
	if err := yaml.UnmarshalStrict(data, &configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Webhooks) != 1 {
		t.Fatalf("want one capacity webhook, got %d", len(configuration.Webhooks))
	}
	webhook := configuration.Webhooks[0]
	if webhook.FailurePolicy == nil || *webhook.FailurePolicy != admissionregistrationv1.Fail || webhook.SideEffects == nil || *webhook.SideEffects != admissionregistrationv1.SideEffectClassNone || webhook.MatchPolicy == nil || *webhook.MatchPolicy != admissionregistrationv1.Equivalent {
		t.Fatal("capacity webhook must fail closed, be side-effect free and match equivalent versions")
	}
	if webhook.NamespaceSelector != nil || webhook.ObjectSelector != nil || len(webhook.MatchConditions) != 0 {
		t.Fatal("capacity webhook must not be narrowed by selectors or conditions")
	}
	if len(webhook.Rules) != 1 {
		t.Fatal("capacity webhook must have exactly one rule")
	}
	rule := webhook.Rules[0]
	if !slices.Equal(rule.APIGroups, []string{"metal.datumapis.com"}) || !slices.Equal(rule.Resources, []string{"machinecapacities", "machinecapacities/status"}) || !slices.Equal(rule.Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update, admissionregistrationv1.Delete}) {
		t.Fatalf("capacity webhook rule drifted: %+v", rule)
	}
}
