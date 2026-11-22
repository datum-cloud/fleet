package registration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func requiredFences(projects []string) []admissionv1.ValidatingWebhook {
	names, _ := json.Marshal(projects)
	projectContext := fmt.Sprintf("has(request.userInfo.extra) && %q in request.userInfo.extra && request.userInfo.extra[%q] == ['Project'] && %q in request.userInfo.extra && request.userInfo.extra[%q] == ['resourcemanager.miloapis.com'] && %q in request.userInfo.extra && size(request.userInfo.extra[%q]) == 1 && request.userInfo.extra[%q][0] != ''", iam.ParentKindExtraKey, iam.ParentKindExtraKey, iam.ParentAPIGroupExtraKey, iam.ParentAPIGroupExtraKey, iam.ParentNameExtraKey, iam.ParentNameExtraKey, iam.ParentNameExtraKey)
	protectedContext := fmt.Sprintf("!(%s) || request.userInfo.extra[%q][0] in %s", projectContext, iam.ParentNameExtraKey, names)
	namespaced, cluster := admissionv1.NamespacedScope, admissionv1.ClusterScope
	writes := []admissionv1.OperationType{admissionv1.Create, admissionv1.Update, admissionv1.Delete}
	return []admissionv1.ValidatingWebhook{
		{Name: "registration.fleet.datumapis.com", Rules: []admissionv1.RuleWithOperations{{Operations: writes, Rule: admissionv1.Rule{APIGroups: []string{"metal.datumapis.com"}, APIVersions: []string{"v1alpha1"}, Resources: []string{"machinecapacities", "machinecapacities/status"}, Scope: &namespaced}}}},
		{Name: "inventory.registration.fleet.datumapis.com", Rules: []admissionv1.RuleWithOperations{{Operations: writes, Rule: admissionv1.Rule{APIGroups: []string{"fleet.datumapis.com"}, APIVersions: []string{"v1alpha1"}, Resources: []string{"devices", "devices/status"}, Scope: &namespaced}}}, MatchConditions: []admissionv1.MatchCondition{{Name: "provider-project-context", Expression: protectedContext}}},
		{Name: "namespace.registration.fleet.datumapis.com", Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Delete}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"namespaces"}, Scope: &cluster}}}, MatchConditions: []admissionv1.MatchCondition{{Name: "provider-project-context", Expression: protectedContext}, {Name: "inventory-namespace", Expression: "!has(request.name) || request.name == '' || request.name == 'default'"}}},
		{Name: "project.registration.fleet.datumapis.com", Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Delete}, Rule: admissionv1.Rule{APIGroups: []string{"resourcemanager.miloapis.com"}, APIVersions: []string{"v1alpha1"}, Resources: []string{"projects"}, Scope: &cluster}}}, MatchConditions: []admissionv1.MatchCondition{{Name: "provider-project-name", Expression: "!has(request.name) || request.name == '' || request.name in " + string(names)}}},
	}
}

func VerifyFence(ctx context.Context, reader client.Reader, projects []string) error {
	_, err := VerifiedFenceDigest(ctx, reader, projects)
	return err
}

func VerifiedFenceDigest(ctx context.Context, reader client.Reader, projects []string) (string, error) {
	var configuration admissionv1.ValidatingWebhookConfiguration
	if err := reader.Get(ctx, client.ObjectKey{Name: "fleet-capacity-registration"}, &configuration); err != nil {
		return "", errors.New("project-plane registration admission configuration is unavailable")
	}
	if err := verifyFenceConfiguration(configuration, projects); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(configuration.Webhooks)
	return digest(encoded), err
}

func verifyFenceConfiguration(configuration admissionv1.ValidatingWebhookConfiguration, projects []string) error {
	if len(projects) == 0 || slices.Contains(projects, "") || !slices.IsSorted(projects) || len(slices.Compact(slices.Clone(projects))) != len(projects) {
		return errors.New("registration admission requires the exact sorted provider scope")
	}
	if !configuration.DeletionTimestamp.IsZero() {
		return errors.New("registration admission configuration is deleting")
	}
	required := requiredFences(projects)
	if len(configuration.Webhooks) != len(required) {
		return errors.New("registration admission requires separate capacity, inventory, namespace and project fences")
	}
	for _, expected := range required {
		index := slices.IndexFunc(configuration.Webhooks, func(webhook admissionv1.ValidatingWebhook) bool { return webhook.Name == expected.Name })
		if index < 0 {
			return errors.New("registration admission webhook is missing")
		}
		webhook := configuration.Webhooks[index]
		if webhook.FailurePolicy == nil || *webhook.FailurePolicy != admissionv1.Fail || webhook.SideEffects == nil || *webhook.SideEffects != admissionv1.SideEffectClassNoneOnDryRun || webhook.MatchPolicy == nil || *webhook.MatchPolicy != admissionv1.Equivalent || !slices.Contains(webhook.AdmissionReviewVersions, "v1") || (webhook.NamespaceSelector != nil && (len(webhook.NamespaceSelector.MatchLabels) != 0 || len(webhook.NamespaceSelector.MatchExpressions) != 0)) || (webhook.ObjectSelector != nil && (len(webhook.ObjectSelector.MatchLabels) != 0 || len(webhook.ObjectSelector.MatchExpressions) != 0)) || len(webhook.ClientConfig.CABundle) == 0 {
			return errors.New("registration admission must be fail-closed, without label selectors, and have an explicit serving CA")
		}
		if !reflect.DeepEqual(webhook.Rules, expected.Rules) || !slices.Equal(webhook.MatchConditions, expected.MatchConditions) {
			return errors.New("registration admission rules and trusted-context conditions must exactly match the retained provider scope")
		}
		if !reflect.DeepEqual(webhook.ClientConfig, configuration.Webhooks[0].ClientConfig) {
			return errors.New("registration fences must use the same authenticated authority endpoint")
		}
	}
	return nil
}
