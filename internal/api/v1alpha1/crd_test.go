package v1alpha1

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestAllocationCRDContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "base", "crd", "fleet.datumapis.com.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	crds := map[string]*extensionsv1.CustomResourceDefinition{}
	for {
		var crd extensionsv1.CustomResourceDefinition
		if err := decoder.Decode(&crd); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if crd.Name != "" {
			copy := crd
			crds[crd.Name] = &copy
		}
	}

	for _, legacy := range []string{"machineclaims.fleet.datumapis.com"} {
		if crds[legacy] != nil {
			t.Fatalf("legacy CRD %q remains in the allocation API", legacy)
		}
	}

	capacity := crds["machinecapacities.fleet.datumapis.com"]
	assertCRDKind(t, capacity, "MachineCapacity")
	capacitySpec := schemaProperty(t, capacity, "spec")
	assertRequired(t, capacitySpec.Required, "deviceRef")
	if _, found := capacitySpec.Properties["providerRef"]; found {
		t.Fatal("MachineCapacity.spec.providerRef leaks an implementation-specific identity")
	}
	if len(capacitySpec.XValidations) == 0 || capacitySpec.XValidations[0].Rule != "self.deviceRef == oldSelf.deviceRef" {
		t.Fatal("MachineCapacity.spec.deviceRef is not immutable")
	}

	machine := crds["machines.fleet.datumapis.com"]
	assertCRDKind(t, machine, "Machine")
	machineSpec := schemaProperty(t, machine, "spec")
	assertRequired(t, machineSpec.Required, "machineClassRef", "machineProfileRef", "locationRef")

	allocation := crds["machineallocations.fleet.datumapis.com"]
	assertCRDKind(t, allocation, "MachineAllocation")
	allocationSpec := schemaProperty(t, allocation, "spec")
	assertRequired(t, allocationSpec.Required, "machineRef", "machineCapacityRef")
	if len(allocationSpec.XValidations) == 0 || allocationSpec.XValidations[0].Rule != "self == oldSelf" {
		t.Fatal("MachineAllocation.spec is not immutable")
	}
	allocationStatus := schemaProperty(t, allocation, "status")
	if len(allocationStatus.XValidations) == 0 {
		t.Fatal("MachineAllocation.status does not enforce terminal release")
	}

	installation := crds["installations.fleet.datumapis.com"]
	installationSpec := schemaProperty(t, installation, "spec")
	assertRequired(t, installationSpec.Required, "machineRef")
	if _, found := installationSpec.Properties["claimRef"]; found {
		t.Fatal("Installation.spec.claimRef remains after the Machine migration")
	}
}

func assertCRDKind(t *testing.T, crd *extensionsv1.CustomResourceDefinition, kind string) {
	t.Helper()
	if crd == nil {
		t.Fatalf("%s CRD is missing", kind)
	}
	if crd.Spec.Names.Kind != kind {
		t.Fatalf("CRD kind = %q, want %q", crd.Spec.Names.Kind, kind)
	}
}

func schemaProperty(t *testing.T, crd *extensionsv1.CustomResourceDefinition, name string) extensionsv1.JSONSchemaProps {
	t.Helper()
	if crd == nil || len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		t.Fatalf("CRD %q has no version schema", crd.Name)
	}
	property, found := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties[name]
	if !found {
		t.Fatalf("CRD %q has no %s schema", crd.Name, name)
	}
	return property
}

func assertRequired(t *testing.T, actual []string, expected ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, field := range actual {
		set[field] = true
	}
	for _, field := range expected {
		if !set[field] {
			t.Errorf("required fields %v do not include %q", actual, field)
		}
	}
}
