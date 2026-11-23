package registration

import (
	"os"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

// inventoryBackedCondition is the packaged webhook's only match condition. It
// routes a create under the derived name and any request whose new or old
// object carries a registration annotation, so adding, changing or removing
// the marker is always gated.
const inventoryBackedCondition = "(request.operation == 'CREATE' && object != null" +
	" && has(object.metadata.name) && object.metadata.name.startsWith('device-'))" +
	" || (object != null && has(object.metadata.annotations)" +
	" && object.metadata.annotations.exists(key, key.startsWith('fleet.datumapis.com/registration-')))" +
	" || (oldObject != null && has(oldObject.metadata.annotations)" +
	" && oldObject.metadata.annotations.exists(key, key.startsWith('fleet.datumapis.com/registration-')))"

// The packaged webhook is fail-closed over writes to inventory-backed
// capacity only. It must not route status writes, other capacity or any
// other resource.
func TestPackagedWebhookFencesOnlyInventoryBackedCapacity(t *testing.T) {
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
	if webhook.TimeoutSeconds == nil || *webhook.TimeoutSeconds != 20 {
		t.Fatal("capacity webhook timeout drifted")
	}
	if webhook.NamespaceSelector != nil || webhook.ObjectSelector != nil {
		t.Fatal("capacity webhook must not be narrowed by selectors")
	}
	if len(webhook.MatchConditions) != 1 || webhook.MatchConditions[0].Name != "inventory-backed-capacity" || webhook.MatchConditions[0].Expression != inventoryBackedCondition {
		t.Fatalf("capacity webhook match conditions drifted: %+v", webhook.MatchConditions)
	}
	if len(webhook.Rules) != 1 {
		t.Fatal("capacity webhook must have exactly one rule")
	}
	rule := webhook.Rules[0]
	if !slices.Equal(rule.APIGroups, []string{"metal.datumapis.com"}) || !slices.Equal(rule.APIVersions, []string{"v1alpha1"}) || !slices.Equal(rule.Resources, []string{"machinecapacities"}) || !slices.Equal(rule.Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update, admissionregistrationv1.Delete}) || rule.Scope == nil || *rule.Scope != admissionregistrationv1.NamespacedScope {
		t.Fatalf("capacity webhook rule drifted: %+v", rule)
	}
	// The condition's literals must stay in step with the handler's marker.
	for _, literal := range []string{"'" + CapacityNamePrefix + "'", "'" + RegistrationAnnotationPrefix + "'"} {
		if !strings.Contains(inventoryBackedCondition, literal) {
			t.Fatalf("match condition does not use the handler's marker %s", literal)
		}
	}
	for _, annotation := range []string{DeviceUIDAnnotation, DeviceVersionAnnotation, OperationAnnotation} {
		if !strings.HasPrefix(annotation, RegistrationAnnotationPrefix) {
			t.Fatalf("registration annotation %s is outside the marker prefix", annotation)
		}
	}
}
