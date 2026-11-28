package capacity

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
	sigsyaml "sigs.k8s.io/yaml"
)

// TestEveryEvaluatorListsDevicesProjectWide fails when an identity that
// evaluates capacity cannot list Devices in every namespace of the provider
// project. Duplicate lists them all, so a namespaced grant would fail every
// evaluation, and with it every allocation, in that mode.
func TestEveryEvaluatorListsDevicesProjectWide(t *testing.T) {
	root := filepath.Join("..", "..")
	// Milo binds roles on the project, so a role's permission is project-wide.
	for _, name := range []string{"installation-requester", "capacity-projector"} {
		data, err := os.ReadFile(filepath.Join(root, "config", "components", "device-operations", "roles", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var role struct {
			Spec struct {
				IncludedPermissions []string `json:"includedPermissions"`
			} `json:"spec"`
		}
		if err := sigsyaml.Unmarshal(data, &role); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(role.Spec.IncludedPermissions, "fleet.datumapis.com/devices.list") {
			t.Errorf("the %s role cannot list Devices", name)
		}
	}

	// Standalone: each evaluating ServiceAccount is bound cluster-wide to a
	// ClusterRole that lists Devices.
	data, err := os.ReadFile(filepath.Join(root, "config", "standalone", "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	listers := map[string]bool{}
	var bindings []rbacv1.ClusterRoleBinding
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var object struct {
			rbacv1.ClusterRole `json:",inline"`
			RoleRef            rbacv1.RoleRef   `json:"roleRef"`
			Subjects           []rbacv1.Subject `json:"subjects"`
		}
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		switch object.Kind {
		case "ClusterRole":
			for _, rule := range object.Rules {
				if slices.Contains(rule.Resources, "devices") && slices.Contains(rule.Verbs, "list") && len(rule.ResourceNames) == 0 {
					listers[object.Name] = true
				}
			}
		case "ClusterRoleBinding":
			bindings = append(bindings, rbacv1.ClusterRoleBinding{RoleRef: object.RoleRef, Subjects: object.Subjects})
		}
	}
	for _, account := range []string{"fleet-standalone-smoke", "fleet-capacity-projector"} {
		bound := false
		for _, binding := range bindings {
			for _, subject := range binding.Subjects {
				if subject.Kind == "ServiceAccount" && subject.Name == account && binding.RoleRef.Kind == "ClusterRole" && listers[binding.RoleRef.Name] {
					bound = true
				}
			}
		}
		if !bound {
			t.Errorf("the standalone %s ServiceAccount cannot list Devices in every namespace", account)
		}
	}

	// Multi-plane: the Metal provider and the projector hold ClusterRoles in
	// the provider project that list Devices.
	milo, err := os.ReadFile(filepath.Join(root, "hack", "multiplane", "milo.py"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(milo)
	provider := source[strings.Index(source, "if role=='provider':"):strings.Index(source, "components={")]
	projector := source[strings.Index(source, "'projector':["):]
	projector = projector[:strings.Index(projector, "]}]")]
	for name, rules := range map[string]string{"Metal provider": provider, "projector": projector} {
		if !strings.Contains(rules, "'devices'") || !strings.Contains(rules, "'list'") {
			t.Errorf("the multi-plane %s cannot list Devices", name)
		}
	}
}
