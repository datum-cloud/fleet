package v1alpha1

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var (
	configRoot     = filepath.Join("..", "..", "..", "..", "config")
	componentRoot  = filepath.Join(configRoot, "components", "device-operations")
	operationKinds = []string{"DeviceInstallation", "DeviceWipe", "DeviceRecovery"}
)

func decodeAll[T any](t *testing.T, path string) []T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []T
	for {
		var item T
		if err := decoder.Decode(&item); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, item)
	}
}

func decodeDir[T any](t *testing.T, dir string) []T {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var out []T
	for _, file := range files {
		if filepath.Base(file) != "kustomization.yaml" {
			out = append(out, decodeAll[T](t, file)...)
		}
	}
	return out
}

func loadCRDs(t *testing.T) map[string]*extensionsv1.CustomResourceDefinition {
	t.Helper()
	out := map[string]*extensionsv1.CustomResourceDefinition{}
	for _, crd := range decodeAll[extensionsv1.CustomResourceDefinition](t, filepath.Join(componentRoot, "crds", "fleet.datumapis.com.yaml")) {
		copied := crd
		out[crd.Spec.Names.Kind] = &copied
	}
	return out
}

func TestCRDContract(t *testing.T) {
	crds := loadCRDs(t)
	if len(crds) != 4 || crds["DeviceCustody"] == nil {
		t.Fatalf("got %d CRDs, want DeviceCustody and three operation kinds", len(crds))
	}
	for kind, crd := range crds {
		version := crd.Spec.Versions[0]
		if crd.Spec.Group != GroupVersion.Group || crd.Name != crd.Spec.Names.Plural+"."+GroupVersion.Group || crd.Spec.Scope != extensionsv1.NamespaceScoped || len(crd.Spec.Versions) != 1 || version.Name != GroupVersion.Version || !version.Served || !version.Storage {
			t.Fatalf("%s identity", kind)
		}
		if crd.Annotations["discovery.miloapis.com/parent-contexts"] != "Project" {
			t.Fatalf("%s must be served only in Milo project control planes", kind)
		}
		properties := version.Schema.OpenAPIV3Schema.Properties
		if kind == "DeviceCustody" {
			// Custody keeps its fields in spec, like a Lease.
			if version.Subresources != nil || len(properties["status"].Properties) != 0 {
				t.Fatal("DeviceCustody has no status")
			}
		} else {
			if version.Subresources == nil || version.Subresources.Status == nil {
				t.Fatalf("%s status must be a subresource, written only by the coordinator", kind)
			}
			spec := properties["spec"]
			if !slices.Contains(spec.Required, "creator") || !slices.Contains(spec.Required, "deviceRef") || !slices.Contains(spec.Required, "authority") {
				t.Fatalf("%s must carry the operation envelope", kind)
			}
			if len(spec.XValidations) == 0 || spec.XValidations[0].Rule != "self == oldSelf" {
				t.Fatalf("%s spec must be immutable", kind)
			}
		}
		defaulted := crd.DeepCopy()
		extensionsv1.SetObjectDefaults_CustomResourceDefinition(defaulted)
		defaulted.Status.StoredVersions = []string{GroupVersion.Version}
		var internal apiextensions.CustomResourceDefinition
		if err := extensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(defaulted, &internal, nil); err != nil {
			t.Fatal(err)
		}
		if errs := crdvalidation.ValidateCustomResourceDefinition(context.Background(), &internal); len(errs) > 0 {
			t.Fatalf("%s: %v", kind, errs.ToAggregate())
		}
	}
	// Bootstrap is an opaque reference and a digest, never a payload.
	bootstrap := crds["DeviceInstallation"].Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["bootstrap"]
	if len(bootstrap.Properties) != 2 || bootstrap.Properties["ref"].Type != "string" || bootstrap.Properties["digest"].Pattern == "" {
		t.Fatalf("bootstrap = %+v", bootstrap.Properties)
	}
}

type protectedResource struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		ServiceRef  struct{ Name string } `json:"serviceRef"`
		Kind        string                `json:"kind"`
		Plural      string                `json:"plural"`
		Permissions []string              `json:"permissions"`
		Subresource []struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		} `json:"subresources"`
	} `json:"spec"`
}

type role struct {
	Metadata struct{ Name string } `json:"metadata"`
	Spec     struct {
		Inherited []struct{ Name string } `json:"inheritedRoles"`
		Included  []string                `json:"includedPermissions"`
	} `json:"spec"`
}

// declared returns every permission the ProtectedResources in dir declare.
func declared(t *testing.T, dir string, into map[string]bool) {
	t.Helper()
	for _, resource := range decodeDir[protectedResource](t, dir) {
		for _, verb := range resource.Spec.Permissions {
			into[resource.Spec.ServiceRef.Name+"/"+resource.Spec.Plural+"."+verb] = true
		}
		for _, sub := range resource.Spec.Subresource {
			for _, verb := range sub.Permissions {
				into[resource.Spec.ServiceRef.Name+"/"+resource.Spec.Plural+"/"+sub.Name+"."+verb] = true
			}
		}
	}
}

func TestRolesKeepOneWriterAndKeepStaffFromCustomerSecrets(t *testing.T) {
	crds := loadCRDs(t)
	resources := decodeDir[protectedResource](t, filepath.Join(componentRoot, "protected-resources"))
	if len(resources) != len(crds) {
		t.Fatalf("%d ProtectedResources for %d CRDs", len(resources), len(crds))
	}
	for _, resource := range resources {
		crd := crds[resource.Spec.Kind]
		if crd == nil || resource.Spec.ServiceRef.Name != GroupVersion.Group || resource.Spec.Plural != crd.Spec.Names.Plural || resource.Metadata.Name != GroupVersion.Group+"-"+crd.Spec.Names.Singular {
			t.Fatalf("ProtectedResource %s does not match a CRD", resource.Metadata.Name)
		}
	}
	permissions := map[string]bool{}
	declared(t, filepath.Join(componentRoot, "protected-resources"), permissions)
	declared(t, filepath.Join(configRoot, "components", "inventory", "protected-resources"), permissions)
	declared(t, filepath.Join(configRoot, "components", "iam", "protected-resources"), permissions)
	roles := map[string]role{}
	for _, r := range decodeDir[role](t, filepath.Join(componentRoot, "roles")) {
		roles[r.Metadata.Name] = r
		for _, permission := range r.Spec.Included {
			if !permissions[permission] {
				t.Fatalf("%s grants undeclared permission %s", r.Metadata.Name, permission)
			}
		}
	}
	p := func(resource string, verbs ...string) []string {
		var out []string
		for _, verb := range verbs {
			out = append(out, resource+"."+verb)
		}
		return out
	}
	group := GroupVersion.Group + "/"
	read := []string{"get", "list", "watch"}
	want := map[string][]string{
		"fleet.datumapis.com-device-operations-viewer":      slices.Concat(p(group+"devicecustodies", read...), p(group+"deviceinstallations", read...), p(group+"devicewipes", read...), p(group+"devicerecoveries", read...)),
		"fleet.datumapis.com-device-recovery-operator":      slices.Concat(p(group+"devicerecoveries", "create"), p("metal.datumapis.com/machineallocations", read...)),
		"fleet.datumapis.com-device-installation-requester": slices.Concat(p(group+"deviceinstallations", "create", "get", "list", "watch"), p(group+"devicecustodies", read...)),
		"fleet.datumapis.com-device-wipe-requester":         slices.Concat(p(group+"devicewipes", "create", "get", "list", "watch"), p(group+"devicecustodies", read...)),
	}
	coordinator := slices.Concat(p(group+"devicecustodies", "get", "list", "watch", "create", "update", "delete"), p(group+"devices", read...), p("metal.datumapis.com/machineallocations", read...), p("metal.datumapis.com/machinecapacities", read...))
	for _, plural := range []string{"deviceinstallations", "devicewipes", "devicerecoveries"} {
		coordinator = slices.Concat(coordinator, p(group+plural, "get", "list", "watch", "delete", "updateStatus"), []string{group + plural + "/status.update"})
	}
	want["fleet.datumapis.com-device-coordinator"] = coordinator
	if len(roles) != len(want) {
		t.Fatalf("roles = %v", roles)
	}
	for name, expected := range want {
		current, ok := roles[name]
		if !ok {
			t.Fatalf("missing role %s", name)
		}
		actual := slices.Clone(current.Spec.Included)
		slices.Sort(actual)
		slices.Sort(expected)
		if !slices.Equal(actual, expected) {
			t.Fatalf("%s = %v, want %v", name, actual, expected)
		}
		inherits := len(current.Spec.Inherited) != 0
		if name == "fleet.datumapis.com-device-recovery-operator" {
			if len(current.Spec.Inherited) != 1 || current.Spec.Inherited[0].Name != "fleet.datumapis.com-device-operations-viewer" {
				t.Fatal("the recovery operator inherits only the viewer")
			}
		} else if inherits {
			t.Fatalf("%s must not inherit", name)
		}
		for _, permission := range actual {
			parts := strings.SplitN(strings.TrimPrefix(permission, group), ".", 2)
			resource, verb := parts[0], parts[1]
			// Only the coordinator writes custody or operation status.
			if name != "fleet.datumapis.com-device-coordinator" && (strings.Contains(permission, "/status.") || strings.HasSuffix(permission, ".updateStatus") || (resource == "devicecustodies" && !slices.Contains(read, verb))) {
				t.Fatalf("%s writes custody or status: %s", name, permission)
			}
			// No role reads a sealed store, a Secret or a bootstrap payload.
			if strings.Contains(permission, "secret") || strings.Contains(permission, "sealed") || strings.Contains(permission, "bootstrap") {
				t.Fatalf("%s grants %s", name, permission)
			}
		}
	}
	// Staff can never create provider or release kinds.
	for _, staff := range []string{"fleet.datumapis.com-device-operations-viewer", "fleet.datumapis.com-device-recovery-operator"} {
		for _, permission := range roles[staff].Spec.Included {
			if strings.HasSuffix(permission, ".create") && permission != group+"devicerecoveries.create" {
				t.Fatalf("%s may create %s", staff, permission)
			}
		}
	}
}

// TestComponentIsInert proves Increment A wires nothing: no root includes the
// component, and the test-only kinds ship nowhere.
func TestComponentIsInert(t *testing.T) {
	err := filepath.WalkDir(configRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(path, componentRoot+string(filepath.Separator)) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if filepath.Base(path) == "kustomization.yaml" && strings.Contains(string(data), "device-operations") {
			t.Errorf("%s includes the device-operations component", path)
		}
		if strings.Contains(string(data), "DeviceTest") || strings.Contains(string(data), "devicetest") {
			t.Errorf("%s ships a test-only kind", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPhases(t *testing.T) {
	for _, phase := range []Phase{PhaseSucceeded, PhaseFailed, PhaseCancelled, PhaseOutcomeUnknown} {
		if !phase.Ended() || phase.Running() {
			t.Fatalf("%s must be ended", phase)
		}
	}
	for _, phase := range []Phase{PhaseAdmitted, PhaseDispatched, PhaseVerifying} {
		if phase.Ended() || !phase.Running() {
			t.Fatalf("%s must be running", phase)
		}
	}
	if PhasePending.Ended() || PhasePending.Running() {
		t.Fatal("Pending is neither")
	}
	for _, kind := range operationKinds {
		if _, ok := loadCRDs(t)[kind]; !ok {
			t.Fatalf("missing %s", kind)
		}
	}
}
