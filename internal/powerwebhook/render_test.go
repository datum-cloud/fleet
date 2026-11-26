package powerwebhook

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const standaloneWriter = "system:serviceaccount:fleet-system:fleet-standalone-smoke"

// renderRoots builds every Kustomize root under config, keyed by directory.
// Components are rendered through the roots that include them.
func renderRoots(t *testing.T, kustomize, config string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	err := filepath.WalkDir(config, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "kustomization.yaml" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var kustomization struct{ Kind string }
		if err := yaml.Unmarshal(data, &kustomization); err != nil {
			return err
		}
		if kustomization.Kind == "Component" {
			return nil
		}
		dir := filepath.Dir(path)
		rendered, err := exec.Command(kustomize, "build", dir).Output()
		if err != nil {
			t.Fatalf("kustomize build %s: %v", dir, err)
		}
		decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
		for {
			var object map[string]any
			if err := decoder.Decode(&object); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", dir, err)
			}
			if object != nil {
				out[dir] = append(out[dir], object)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// walk visits every map in a rendered object, including objects embedded as
// templates, such as a graph's resources, and YAML or JSON held in strings.
func walk(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, item := range typed {
			walk(item, visit)
		}
	case []any:
		for _, item := range typed {
			walk(item, visit)
		}
	case string:
		if strings.Contains(typed, "kind") {
			decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(typed), 4096)
			for {
				var object map[string]any
				if decoder.Decode(&object) != nil {
					return
				}
				walk(object, visit)
			}
		}
	}
}

func strs(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func anyOf(values []string, wanted ...string) bool {
	return slices.ContainsFunc(values, func(value string) bool { return slices.Contains(wanted, value) })
}

func name(object map[string]any) (string, string) {
	metadata, _ := object["metadata"].(map[string]any)
	namespace, _ := metadata["namespace"].(string)
	value, _ := metadata["name"].(string)
	return namespace, value
}

// rbacStatusWriter reports whether a Kubernetes Role or ClusterRole can
// update or patch Machine status. An aggregated ClusterRole takes its rules
// from other roles at runtime, so it counts as a writer until reviewed.
func rbacStatusWriter(object map[string]any) bool {
	if kind, _ := object["kind"].(string); kind != "Role" && kind != "ClusterRole" {
		return false
	}
	if apiVersion, _ := object["apiVersion"].(string); !strings.HasPrefix(apiVersion, "rbac.authorization.k8s.io/") {
		return false
	}
	if _, aggregated := object["aggregationRule"]; aggregated {
		return true
	}
	rules, _ := object["rules"].([]any)
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		if anyOf(strs(rule["apiGroups"]), "*", "metal.datumapis.com") && anyOf(strs(rule["resources"]), "*", "*/status", "machines/*", "machines/status") && anyOf(strs(rule["verbs"]), "*", "update", "patch") {
			return true
		}
	}
	return false
}

// TestOnlyThePowerWriterCanWriteMachineStatus renders every root under config
// and lists the roles that can write Machine status. Roles made outside
// config, by a script or at runtime, are not covered. Kubernetes RBAC grants it only to
// the standalone smoke runner, the power projector's identity there. Milo
// grants updateStatus and status permissions only to the controller role,
// the manager's. Milo's authorizer checks a status write against update and
// patch, so the consumer roles can write status too; the Machine power
// webhook refuses their power changes. A new holder of any of these fails.
func TestOnlyThePowerWriterCanWriteMachineStatus(t *testing.T) {
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("kustomize required")
		}
		t.Skip("kustomize required")
	}
	roots := renderRoots(t, kustomize, filepath.Join(root, "config"))
	if len(roots) < 10 {
		t.Fatalf("rendered %d roots", len(roots))
	}
	var rbac []string
	bound := map[string][]string{}
	milo := map[string]struct {
		permissions []string
		inherits    []string
	}{}
	for dir, objects := range roots {
		for _, object := range objects {
			walk(object, func(object map[string]any) {
				namespace, objectName := name(object)
				kind, _ := object["kind"].(string)
				apiVersion, _ := object["apiVersion"].(string)
				switch {
				case rbacStatusWriter(object):
					rbac = append(rbac, dir+" "+kind+" "+namespace+"/"+objectName)
				case (kind == "RoleBinding" || kind == "ClusterRoleBinding") && strings.HasPrefix(apiVersion, "rbac.authorization.k8s.io/"):
					ref, _ := object["roleRef"].(map[string]any)
					role, _ := ref["name"].(string)
					subjects, _ := object["subjects"].([]any)
					for _, item := range subjects {
						subject, _ := item.(map[string]any)
						subjectKind, _ := subject["kind"].(string)
						subjectNamespace, _ := subject["namespace"].(string)
						subjectName, _ := subject["name"].(string)
						if subjectKind == "ServiceAccount" {
							subjectName = "system:serviceaccount:" + subjectNamespace + ":" + subjectName
						}
						key := dir + " " + namespace + "/" + role
						bound[key] = append(bound[key], subjectName)
					}
				case kind == "Role" && strings.HasPrefix(apiVersion, "iam.miloapis.com/"):
					spec, _ := object["spec"].(map[string]any)
					entry := milo[objectName]
					entry.permissions = append(entry.permissions, strs(spec["includedPermissions"])...)
					inherited, _ := spec["inheritedRoles"].([]any)
					for _, item := range inherited {
						role, _ := item.(map[string]any)
						if roleName, ok := role["name"].(string); ok {
							entry.inherits = append(entry.inherits, roleName)
						}
					}
					milo[objectName] = entry
				}
			})
		}
	}
	sort.Strings(rbac)
	want := []string{
		filepath.Join(root, "config", "standalone") + " Role fleet-project-a/fleet-synthetic-consumer-writer",
		filepath.Join(root, "config", "standalone") + " Role fleet-project-b/fleet-synthetic-consumer-writer",
	}
	if !slices.Equal(rbac, want) {
		t.Fatalf("Kubernetes roles that write Machine status:\n%s\nwant only the standalone power writer's:\n%s", strings.Join(rbac, "\n"), strings.Join(want, "\n"))
	}
	for _, role := range rbac {
		key := strings.Replace(role, " Role ", " ", 1)
		if subjects := bound[key]; !slices.Equal(subjects, []string{standaloneWriter}) {
			t.Fatalf("%s is bound to %v, want only %s", role, subjects, standaloneWriter)
		}
	}
	policy, err := os.ReadFile(filepath.Join(root, "config", "standalone", "machine-power.yaml"))
	if err != nil || !strings.Contains(string(policy), "request.userInfo.username == '"+standaloneWriter+"'") {
		t.Fatalf("the standalone power policy must admit exactly %s", standaloneWriter)
	}

	// holders resolves inheritance and returns the Milo roles that hold any
	// of the permissions, directly or through an inherited role.
	holders := func(permissions ...string) []string {
		var out []string
		var holds func(string, map[string]bool) bool
		holds = func(role string, seen map[string]bool) bool {
			if seen[role] {
				return false
			}
			seen[role] = true
			entry := milo[role]
			if slices.ContainsFunc(entry.permissions, func(p string) bool {
				return slices.Contains(permissions, p) || (strings.Contains(p, "*") && (strings.HasPrefix(p, "metal.datumapis.com/") || strings.HasPrefix(p, "*")))
			}) {
				return true
			}
			return slices.ContainsFunc(entry.inherits, func(parent string) bool { return holds(parent, seen) })
		}
		for role := range milo {
			if holds(role, map[string]bool{}) {
				out = append(out, role)
			}
		}
		sort.Strings(out)
		return out
	}
	if len(milo) == 0 {
		t.Fatal("no Milo roles rendered")
	}
	if got := holders("metal.datumapis.com/machines.updateStatus", "metal.datumapis.com/machines/status.update", "metal.datumapis.com/machines/status.patch"); !slices.Equal(got, []string{"metal.datumapis.com-controller"}) {
		t.Fatalf("Milo roles with Machine status permissions: %v, want only the controller role", got)
	}
	if got := holders("metal.datumapis.com/machines.update", "metal.datumapis.com/machines.patch"); !slices.Equal(got, []string{"metal.datumapis.com-claimant", "metal.datumapis.com-consumer", "metal.datumapis.com-controller"}) {
		t.Fatalf("Milo roles that can write Machine status through update or patch: %v", got)
	}

	// The guards are in the roots that hold Machines.
	contains := func(dir, kind, objectName string) bool {
		return slices.ContainsFunc(roots[filepath.Join(root, "config", dir)], func(object map[string]any) bool {
			_, n := name(object)
			return object["kind"] == kind && n == objectName
		})
	}
	if !contains("milo", "ValidatingWebhookConfiguration", "fleet-machine-power") {
		t.Fatal("config/milo must install the Machine power webhook")
	}
	if !contains("standalone", "ValidatingAdmissionPolicy", "fleet-machine-power") || !contains("standalone", "ValidatingAdmissionPolicyBinding", "fleet-machine-power") {
		t.Fatal("config/standalone must install the Machine power policy and its binding")
	}
}
