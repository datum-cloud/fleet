package registration

import (
	"context"
	"errors"
	"slices"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func VerifyFence(ctx context.Context, reader client.Reader) error {
	var configuration admissionv1.ValidatingWebhookConfiguration
	if err := reader.Get(ctx, client.ObjectKey{Name: "fleet-capacity-registration"}, &configuration); err != nil {
		return errors.New("project-plane registration admission configuration is unavailable")
	}
	if !configuration.DeletionTimestamp.IsZero() {
		return errors.New("registration admission configuration is deleting")
	}
	for _, webhook := range configuration.Webhooks {
		if webhook.Name != "registration.fleet.datumapis.com" {
			continue
		}
		if webhook.FailurePolicy == nil || *webhook.FailurePolicy != admissionv1.Fail || webhook.SideEffects == nil || *webhook.SideEffects != admissionv1.SideEffectClassNoneOnDryRun || len(webhook.MatchConditions) != 0 || (webhook.NamespaceSelector != nil && (len(webhook.NamespaceSelector.MatchLabels) != 0 || len(webhook.NamespaceSelector.MatchExpressions) != 0)) || (webhook.ObjectSelector != nil && (len(webhook.ObjectSelector.MatchLabels) != 0 || len(webhook.ObjectSelector.MatchExpressions) != 0)) || len(webhook.ClientConfig.CABundle) == 0 {
			return errors.New("registration admission must be fail-closed, unfiltered, and have an explicit serving CA")
		}
		for _, required := range []struct {
			group, resource, version string
			operations               []admissionv1.OperationType
		}{
			{"metal.datumapis.com", "machinecapacities", "v1alpha1", []admissionv1.OperationType{admissionv1.Create, admissionv1.Update, admissionv1.Delete}},
			{"metal.datumapis.com", "machinecapacities/status", "v1alpha1", []admissionv1.OperationType{admissionv1.Update}},
			{"fleet.datumapis.com", "devices", "v1alpha1", []admissionv1.OperationType{admissionv1.Create, admissionv1.Update, admissionv1.Delete}},
			{"", "namespaces", "v1", []admissionv1.OperationType{admissionv1.Delete}},
			{"resourcemanager.miloapis.com", "projects", "v1alpha1", []admissionv1.OperationType{admissionv1.Delete}},
		} {
			for _, operation := range required.operations {
				found := false
				wantedScope := admissionv1.NamespacedScope
				if required.resource == "namespaces" || required.resource == "projects" {
					wantedScope = admissionv1.ClusterScope
				}
				for _, rule := range webhook.Rules {
					if (rule.Scope == nil || *rule.Scope == admissionv1.AllScopes || *rule.Scope == wantedScope) && slices.Contains(rule.APIGroups, required.group) && slices.Contains(rule.APIVersions, required.version) && slices.Contains(rule.Resources, required.resource) && slices.Contains(rule.Operations, operation) {
						found = true
					}
				}
				if !found {
					return errors.New("registration admission omits a required write fence")
				}
			}
		}
		return nil
	}
	return errors.New("registration admission webhook is missing")
}
