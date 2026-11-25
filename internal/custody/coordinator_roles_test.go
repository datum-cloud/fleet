package custody

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// plural is the resource name of a kind, as its CRD declares it.
func plural(kind string) string {
	name := strings.ToLower(kind)
	if strings.HasSuffix(name, "y") {
		return strings.TrimSuffix(name, "y") + "ies"
	}
	return name + "s"
}

// TestEveryCoordinatorRoleCoversEveryKind fails when a registered kind is
// missing from the coordinator role of a mode. The coordinator lists every
// registered kind, so a missing grant stops every admission in that mode.
func TestEveryCoordinatorRoleCoversEveryKind(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var plurals []string
	for _, kind := range registry.Kinds() {
		plurals = append(plurals, plural(kind.GVK.Kind))
	}
	plurals = append(plurals, "devicecommissioningplans", "devicecommissioningplanapprovals")

	data, err := os.ReadFile(filepath.Join("..", "..", "config", "standalone", "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	granted := map[string]bool{}
	for {
		var role rbacv1.Role
		if err := decoder.Decode(&role); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if role.Kind != "Role" || role.Name != "fleet-custody-coordinator" {
			continue
		}
		for _, rule := range role.Rules {
			if slices.Contains(rule.Verbs, "list") {
				for _, resource := range rule.Resources {
					granted[resource] = true
				}
			}
		}
	}
	milo, err := os.ReadFile(filepath.Join("..", "..", "hack", "multiplane", "milo.py"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator := string(milo)
	coordinator = coordinator[strings.Index(coordinator, "'coordinator':["):]
	coordinator = coordinator[:strings.Index(coordinator, "'release':")]
	for _, resource := range plurals {
		if !granted[resource] {
			t.Errorf("the standalone coordinator role cannot list %s", resource)
		}
		if !strings.Contains(coordinator, "'"+resource+"'") {
			t.Errorf("the multi-plane coordinator role cannot list %s", resource)
		}
	}
}
