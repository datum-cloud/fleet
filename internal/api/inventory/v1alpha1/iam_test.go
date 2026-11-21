package v1alpha1

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
)

type protectedResource struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		ServiceRef  struct{ Name string } `json:"serviceRef"`
		Kind        string                `json:"kind"`
		Plural      string                `json:"plural"`
		Singular    string                `json:"singular"`
		Permissions []string              `json:"permissions"`
		Parents     []struct {
			APIGroup string `json:"apiGroup"`
			Kind     string `json:"kind"`
		} `json:"parentResources"`
	} `json:"spec"`
}

type role struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		Inherited []struct{ Name string } `json:"inheritedRoles"`
		Included  []string                `json:"includedPermissions"`
	} `json:"spec"`
}

func decodeDir[T any](t *testing.T, dir string) []T {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var out []T
	for _, file := range files {
		if filepath.Base(file) == "kustomization.yaml" {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			var item T
			if err := decoder.Decode(&item); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			out = append(out, item)
		}
	}
	return out
}

func TestProtectedResourcesMatchTheCRDs(t *testing.T) {
	crds := loadCRDs(t)
	resources := decodeDir[protectedResource](t, filepath.Join(inventoryRoot, "protected-resources"))
	if len(resources) != len(crds) {
		t.Fatalf("got %d ProtectedResources for %d CRDs", len(resources), len(crds))
	}
	for _, resource := range resources {
		crd := crds[resource.Spec.Kind]
		if crd == nil {
			t.Fatalf("ProtectedResource %s names unknown kind %s", resource.Metadata.Name, resource.Spec.Kind)
		}
		// The authorization provider indexes ProtectedResources by
		// "{apiGroup}/{plural}", so serviceRef must be the API group.
		if resource.Spec.ServiceRef.Name != GroupVersion.Group || resource.Spec.Plural != crd.Spec.Names.Plural || resource.Spec.Singular != crd.Spec.Names.Singular {
			t.Fatalf("ProtectedResource %s does not match %s", resource.Metadata.Name, crd.Name)
		}
		if resource.Metadata.Name != GroupVersion.Group+"-"+crd.Spec.Names.Singular {
			t.Fatalf("ProtectedResource name %s", resource.Metadata.Name)
		}
		if len(resource.Spec.Parents) != 1 || resource.Spec.Parents[0].APIGroup != "resourcemanager.miloapis.com" || resource.Spec.Parents[0].Kind != "Project" {
			t.Fatalf("%s must be project-scoped", resource.Metadata.Name)
		}
		if slices.Contains(resource.Spec.Permissions, "updateStatus") {
			t.Fatalf("%s grants status writes; status has no writer yet", resource.Metadata.Name)
		}
	}
}

func TestInventoryRolesSplitReadFromWrite(t *testing.T) {
	declared := map[string]bool{}
	for _, resource := range decodeDir[protectedResource](t, filepath.Join(inventoryRoot, "protected-resources")) {
		for _, verb := range resource.Spec.Permissions {
			declared[GroupVersion.Group+"/"+resource.Spec.Plural+"."+verb] = true
		}
	}
	roles := map[string]role{}
	for _, r := range decodeDir[role](t, filepath.Join(inventoryRoot, "roles")) {
		roles[r.Metadata.Name] = r
	}
	wantVerbs := map[string][]string{
		"fleet.datumapis.com-inventory-viewer":  {"get", "list", "watch"},
		"fleet.datumapis.com-inventory-editor":  {"create", "update", "patch", "delete"},
		"fleet.datumapis.com-inventory-manager": {"create", "update", "patch"},
	}
	if len(roles) != len(wantVerbs) {
		t.Fatalf("want exactly viewer, editor and manager roles, got %v", roles)
	}
	for name, verbs := range wantVerbs {
		t.Run(name, func(t *testing.T) {
			current, ok := roles[name]
			if !ok {
				t.Fatalf("missing role %s", name)
			}
			var expected []string
			for _, resource := range []string{"deviceclaims", "devices", "facilities", "racks", "cables", "powerfeeds"} {
				for _, verb := range verbs {
					expected = append(expected, GroupVersion.Group+"/"+resource+"."+verb)
				}
			}
			actual := slices.Clone(current.Spec.Included)
			slices.Sort(expected)
			slices.Sort(actual)
			if !slices.Equal(actual, expected) {
				t.Fatalf("%s permissions = %v, want %v", name, actual, expected)
			}
			for _, permission := range actual {
				if !declared[permission] {
					t.Fatalf("role permission %s has no ProtectedResource", permission)
				}
			}
			if name == "fleet.datumapis.com-inventory-viewer" {
				if len(current.Spec.Inherited) != 0 {
					t.Fatal("viewer must not inherit additional permissions")
				}
			} else if len(current.Spec.Inherited) != 1 || current.Spec.Inherited[0].Name != "fleet.datumapis.com-inventory-viewer" {
				t.Fatal("editor and manager must inherit only the viewer role")
			}
		})
	}
}

func TestOnlyMiloServesTheInventoryKinds(t *testing.T) {
	read := func(path string) string {
		data, err := os.ReadFile(filepath.Join(configRoot, path, "kustomization.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if !strings.Contains(read("milo"), "../components/inventory\n") {
		t.Fatal("config/milo must register the inventory kinds")
	}
	for _, root := range []string{"base/crd", "overlays/management-plane", "overlays/synthetic-staging"} {
		if strings.Contains(read(root), "inventory") {
			t.Fatalf("config/%s must not install the inventory kinds on the management cluster", root)
		}
	}
	// Metal roles reach consumers. None may grant inventory access.
	for _, r := range decodeDir[role](t, filepath.Join(configRoot, "components", "iam", "roles")) {
		for _, permission := range r.Spec.Included {
			if strings.HasPrefix(permission, GroupVersion.Group+"/") {
				t.Fatalf("metal role %s grants inventory permission %s", r.Metadata.Name, permission)
			}
		}
		for _, inherited := range r.Spec.Inherited {
			if strings.HasPrefix(inherited.Name, GroupVersion.Group+"-") {
				t.Fatalf("metal role %s inherits inventory role %s", r.Metadata.Name, inherited.Name)
			}
		}
	}
}
