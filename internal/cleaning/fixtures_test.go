package cleaning

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestShippedPoliciesAreValid keeps every packaged fixture policy one the
// allocator will pin.
func TestShippedPoliciesAreValid(t *testing.T) {
	root := filepath.Join("..", "..", "config")
	found := 0
	for _, path := range []string{
		filepath.Join(root, "standalone", "fixtures.yaml"),
		filepath.Join(root, "overlays", "synthetic-staging", "provider-project", "cleaning-policy.yaml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			var policy ops.CleaningPolicy
			if err := decoder.Decode(&policy); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if policy.Kind != "CleaningPolicy" {
				continue
			}
			found++
			if err := Validate(policy.Spec); err != nil {
				t.Errorf("%s %s: %v", path, policy.Name, err)
			}
		}
	}
	if found != 2 {
		t.Fatalf("found %d fixture policies, want 2", found)
	}
}
