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

type iamRole struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		Inherited []struct{ Name string } `json:"inheritedRoles"`
		Included  []string                `json:"includedPermissions"`
	} `json:"spec"`
}

// roles reads every Metal IAM role and resolves inherited permissions.
func roles(t *testing.T) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "config", "components", "iam", "roles", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]iamRole{}
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
			var r iamRole
			if err := decoder.Decode(&r); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			raw[r.Metadata.Name] = r
		}
	}
	var resolve func(name string, seen map[string]bool) []string
	resolve = func(name string, seen map[string]bool) []string {
		if seen[name] {
			return nil
		}
		seen[name] = true
		r, ok := raw[name]
		if !ok {
			t.Fatalf("role %s is inherited but not defined", name)
		}
		out := slices.Clone(r.Spec.Included)
		for _, parent := range r.Spec.Inherited {
			out = append(out, resolve(parent.Name, seen)...)
		}
		return out
	}
	out := map[string][]string{}
	for name := range raw {
		out[name] = resolve(name, map[string]bool{})
	}
	return out
}

// TestCustomersCannotWriteImagesOrReadPublications proves the catalog's
// entitlement cannot be forged through IAM: no customer role writes a
// MachineImage or touches a publication record, and a claimant may use a
// published image.
func TestCustomersCannotWriteImagesOrReadPublications(t *testing.T) {
	all := roles(t)
	for _, name := range []string{"metal.datumapis.com-viewer", "metal.datumapis.com-claimant", "metal.datumapis.com-consumer"} {
		permissions, ok := all[name]
		if !ok {
			t.Fatalf("role %s is missing", name)
		}
		for _, permission := range permissions {
			if strings.HasPrefix(permission, "metal.datumapis.com/machineimagepublications.") {
				t.Errorf("%s holds %s", name, permission)
			}
			if verb, ok := strings.CutPrefix(permission, "metal.datumapis.com/machineimages."); ok && !slices.Contains([]string{"list", "get", "watch", "use"}, verb) {
				t.Errorf("%s holds %s", name, permission)
			}
		}
	}
	if !slices.Contains(all["metal.datumapis.com-claimant"], "metal.datumapis.com/machineimages.use") {
		t.Error("a claimant cannot use a published image")
	}
	if !slices.Contains(all["metal.datumapis.com-controller"], "metal.datumapis.com/machineimagepublications.get") {
		t.Error("the Fleet provider cannot read publication records")
	}
}
