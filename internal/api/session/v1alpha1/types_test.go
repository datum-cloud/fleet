package v1alpha1

import (
	"os"
	"path/filepath"
	"testing"

	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

func TestAddToSchemeRegistersSessionKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"MachineManagementSession", "MachineManagementSessionList", "MachineManagementSessionBinding", "MachineManagementSessionBindingList", "ProjectedMachineManagementSession", "ProjectedMachineManagementSessionList"} {
		if _, err := scheme.New(GroupVersion.WithKind(kind)); err != nil {
			t.Errorf("kind %s is not registered: %v", kind, err)
		}
	}
}

func TestGeneratedSessionCRDsDoNotProjectPrivateRouting(t *testing.T) {
	public := readCRD(t, "fleet.datumapis.com_machinemanagementsessions.yaml")
	publicSpec := property(t, public, "spec")
	if _, ok := publicSpec.Properties["machineRef"]; !ok {
		t.Fatal("public session does not reference Machine")
	}
	if _, ok := publicSpec.Properties["machineClaimRef"]; ok {
		t.Fatal("legacy MachineClaim reference remains public")
	}
	if len(publicSpec.XValidations) == 0 {
		t.Fatal("public session spec is mutable")
	}

	projected := readCRD(t, "fleet.datumapis.com_projectedmachinemanagementsessions.yaml")
	projectedSpec := property(t, projected, "spec")
	for _, forbidden := range []string{"sessionUID", "machineUID", "allocationName", "bindingID", "bindingEpoch", "memberCluster", "locationUID", "adapterRoute", "bmcSystemID", "secretRef"} {
		if _, ok := projectedSpec.Properties[forbidden]; ok {
			t.Errorf("projected session leaks %s", forbidden)
		}
	}
	if _, ok := projectedSpec.Properties["machineCapacityRef"]; !ok {
		t.Fatal("projected session does not carry MachineCapacity identity")
	}
	if len(projectedSpec.XValidations) < 3 {
		t.Fatal("projected session lacks immutable and monotonic authority validation")
	}
}

func readCRD(t *testing.T, name string) *extensionsv1.CustomResourceDefinition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "crd", "bases", name))
	if err != nil {
		t.Fatal(err)
	}
	var crd extensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatal(err)
	}
	return &crd
}

func property(t *testing.T, crd *extensionsv1.CustomResourceDefinition, name string) extensionsv1.JSONSchemaProps {
	t.Helper()
	if crd == nil || len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		t.Fatal("CRD schema is missing")
	}
	property, ok := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties[name]
	if !ok {
		t.Fatalf("CRD has no %s", name)
	}
	return property
}
