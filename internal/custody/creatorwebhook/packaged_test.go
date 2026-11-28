package creatorwebhook

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// TestPackagedResourcesMatch keeps the webhook's resource list, the packaged
// webhook registration and the standalone creator policy in step. A kind
// added to one and not the others would leave its creator unchecked in that
// mode.
func TestPackagedResourcesMatch(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	want := slices.Sorted(slices.Values(Resources))

	data, err := os.ReadFile(filepath.Join(root, "config", "components", "device-operations", "webhook.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	webhooks := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := yaml.UnmarshalStrict(data, webhooks); err != nil {
		t.Fatal(err)
	}
	var registered []string
	for _, webhook := range webhooks.Webhooks {
		for _, rule := range webhook.Rules {
			registered = append(registered, rule.Resources...)
		}
	}
	if got := slices.Sorted(slices.Values(registered)); !slices.Equal(got, want) {
		t.Fatalf("webhook.yaml registers %v, the webhook governs %v", got, want)
	}

	data, err = os.ReadFile(filepath.Join(root, "config", "standalone", "site.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policies int
	for _, document := range bytes.Split(data, []byte("\n---\n")) {
		var meta metav1.TypeMeta
		if err := yaml.Unmarshal(document, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Kind != "ValidatingAdmissionPolicy" {
			continue
		}
		policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
		if err := yaml.UnmarshalStrict(document, policy); err != nil {
			t.Fatal(err)
		}
		policies++
		var matched []string
		for _, rule := range policy.Spec.MatchConstraints.ResourceRules {
			matched = append(matched, rule.Resources...)
		}
		if got := slices.Sorted(slices.Values(matched)); !slices.Equal(got, want) {
			t.Fatalf("the standalone creator policy matches %v, the webhook governs %v", got, want)
		}
	}
	if policies != 1 {
		t.Fatalf("the standalone site must package one creator policy, found %d", policies)
	}
}
