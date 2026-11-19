package v1alpha1

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/yaml"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
)

var observerRoot = filepath.Join("..", "..", "..", "..", "config", "inventory-observer")

func loadDocuments(t *testing.T, name string) []*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(observerRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		if object != nil {
			out = append(out, &unstructured.Unstructured{Object: object})
		}
	}
}

func loadCRD(t *testing.T) *extensionsv1.CustomResourceDefinition {
	t.Helper()
	documents := loadDocuments(t, "crd.yaml")
	if len(documents) != 1 {
		t.Fatalf("got %d documents in crd.yaml, want 1", len(documents))
	}
	var crd extensionsv1.CustomResourceDefinition
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(documents[0].Object, &crd); err != nil {
		t.Fatal(err)
	}
	return &crd
}

func TestObservationCRDMatchesGoIdentityAndPassesDefinitionValidation(t *testing.T) {
	crd := loadCRD(t)
	if crd.Spec.Group != GroupVersion.Group || crd.Name != "inventoryobservations."+GroupVersion.Group || crd.Spec.Names.Kind != "InventoryObservation" || crd.Spec.Scope != extensionsv1.NamespaceScoped {
		t.Fatalf("CRD identity = %s %s %s", crd.Name, crd.Spec.Names.Kind, crd.Spec.Scope)
	}
	if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Name != GroupVersion.Version || crd.Spec.Versions[0].Subresources == nil || crd.Spec.Versions[0].Subresources.Status == nil {
		t.Fatal("CRD must serve exactly v1alpha1 with a status subresource")
	}
	if _, ok := crd.Annotations["discovery.miloapis.com/parent-contexts"]; ok {
		t.Fatal("observation CRD must not be exposed through Milo project discovery")
	}
	defaulted := crd.DeepCopy()
	extensionsv1.SetObjectDefaults_CustomResourceDefinition(defaulted)
	defaulted.Status.StoredVersions = []string{GroupVersion.Version}
	var internal apiextensions.CustomResourceDefinition
	if err := extensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(defaulted, &internal, nil); err != nil {
		t.Fatal(err)
	}
	if errs := crdvalidation.ValidateCustomResourceDefinition(context.Background(), &internal); len(errs) > 0 {
		t.Fatal(errs.ToAggregate())
	}
}

func validate(t *testing.T, object, old map[string]any) field.ErrorList {
	t.Helper()
	crd := loadCRD(t)
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	validator, _, err := validation.NewSchemaValidator(&schema)
	if err != nil {
		t.Fatal(err)
	}
	errs := validation.ValidateCustomResource(nil, object, validator)
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	celErrs, _ := cel.NewValidator(structural, true, celconfig.PerCallLimit).Validate(context.Background(), nil, structural, object, old, celconfig.RuntimeCELCostBudget)
	return append(errs, celErrs...)
}

func observationObject(source map[string]any) map[string]any {
	return map[string]any{"apiVersion": GroupVersion.String(), "kind": "InventoryObservation", "metadata": map[string]any{"name": "server-01"}, "spec": map[string]any{"source": source}}
}

func TestObservationSourceIsValidatedAndImmutable(t *testing.T) {
	valid := observationObject(map[string]any{"system": "NetBox", "kind": "Device", "id": int64(1234), "expectedName": "server-01.example"})
	if errs := validate(t, valid, nil); len(errs) > 0 {
		t.Fatalf("valid observation rejected: %v", errs)
	}
	for name, source := range map[string]map[string]any{
		"unknown system": {"system": "Spreadsheet", "kind": "Device", "id": int64(1)},
		"unknown kind":   {"system": "NetBox", "kind": "Cable", "id": int64(1)},
		"zero id":        {"system": "NetBox", "kind": "Device", "id": int64(0)},
		"missing id":     {"system": "NetBox", "kind": "Device"},
	} {
		if errs := validate(t, observationObject(source), nil); len(errs) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	for name, source := range map[string]map[string]any{
		"retarget id":     {"system": "NetBox", "kind": "Device", "id": int64(1235), "expectedName": "server-01.example"},
		"change name":     {"system": "NetBox", "kind": "Device", "id": int64(1234), "expectedName": "server-02.example"},
		"drop name guard": {"system": "NetBox", "kind": "Device", "id": int64(1234)},
	} {
		if errs := validate(t, observationObject(source), valid); len(errs) == 0 {
			t.Errorf("%s accepted as an update", name)
		}
	}
}

func TestObservationStatusAcceptsOnlyObservedOnlyTrust(t *testing.T) {
	object := observationObject(map[string]any{"system": "NetBox", "kind": "Device", "id": int64(1)})
	object["status"] = map[string]any{"trustProfile": "ServerWitnessedBMC-v1"}
	if errs := validate(t, object, nil); len(errs) == 0 {
		t.Fatal("observer status claimed a stronger trust profile")
	}
}

func TestObserverRoleCanOnlyReadObservationsAndWriteTheirStatus(t *testing.T) {
	var roles []rbacv1.Role
	for _, document := range loadDocuments(t, "rbac.yaml") {
		switch document.GetKind() {
		case "ClusterRole", "ClusterRoleBinding":
			t.Fatalf("observer must not hold cluster-wide RBAC: %s", document.GetName())
		case "Role":
			var role rbacv1.Role
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(document.Object, &role); err != nil {
				t.Fatal(err)
			}
			roles = append(roles, role)
		}
	}
	if len(roles) != 1 {
		t.Fatalf("got %d Roles, want 1", len(roles))
	}
	allowed := map[string]map[string]bool{
		"inventoryobservations":        {"get": true, "list": true},
		"inventoryobservations/status": {"update": true},
	}
	for _, rule := range roles[0].Rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != GroupVersion.Group || len(rule.ResourceNames) != 0 || len(rule.NonResourceURLs) != 0 {
			t.Fatalf("unexpected rule scope: %+v", rule)
		}
		for _, resource := range rule.Resources {
			for _, verb := range rule.Verbs {
				if !allowed[resource][verb] {
					t.Fatalf("observer Role grants %s on %s", verb, resource)
				}
			}
		}
	}
}
