package creatorwebhook

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestEveryOperationKindIsGoverned requires the webhook's Go resource list,
// the packaged webhook, and standalone's admission policy to name every
// operation kind the custody CRDs define, so a new kind cannot ship with an
// unchecked creator.
func TestEveryOperationKindIsGoverned(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var kinds []string
	for _, document := range documents(t, filepath.Join(root, "config", "components", "device-operations", "crds", "fleet.datumapis.com.yaml")) {
		var crd extensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(document, &crd); err != nil {
			t.Fatal(err)
		}
		if crd.Spec.Names.Kind != "DeviceCustody" {
			kinds = append(kinds, crd.Spec.Names.Plural)
		}
	}
	want := sorted(kinds)
	if got := sorted(Resources); !slices.Equal(got, want) {
		t.Fatalf("Resources = %v, want every operation kind %v", got, want)
	}
	var webhooks admissionregistrationv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal(documents(t, filepath.Join(root, "config", "components", "device-operations", "webhook.yaml"))[0], &webhooks); err != nil {
		t.Fatal(err)
	}
	if got := sorted(webhooks.Webhooks[0].Rules[0].Resources); !slices.Equal(got, want) {
		t.Fatalf("the packaged webhook governs %v, want %v", got, want)
	}
	found := false
	for _, document := range documents(t, filepath.Join(root, "config", "standalone", "site.yaml")) {
		var policy admissionregistrationv1.ValidatingAdmissionPolicy
		if yaml.Unmarshal(document, &policy) != nil || policy.Kind != "ValidatingAdmissionPolicy" || policy.Name != "fleet-operation-creator" {
			continue
		}
		found = true
		if got := sorted(policy.Spec.MatchConstraints.ResourceRules[0].Resources); !slices.Equal(got, want) {
			t.Fatalf("the standalone creator policy governs %v, want %v", got, want)
		}
	}
	if !found {
		t.Fatal("standalone has no fleet-operation-creator policy")
	}
}

func sorted(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

func documents(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var out [][]byte
	for {
		document, err := reader.Read()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(bytes.TrimSpace(document)) != 0 {
			out = append(out, document)
		}
	}
}
