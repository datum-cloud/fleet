package v1alpha1

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
)

// internalSchema converts one CRD version schema to the API server's internal
// representation, which is what admission validates against.
func internalSchema(t *testing.T, crd *extensionsv1.CustomResourceDefinition) *apiextensions.JSONSchemaProps {
	t.Helper()
	var out apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &out, nil); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestCRDsPassAPIServerDefinitionValidation(t *testing.T) {
	for name, crd := range loadCRDs(t) {
		defaulted := crd.DeepCopy()
		extensionsv1.SetObjectDefaults_CustomResourceDefinition(defaulted)
		defaulted.Status.StoredVersions = []string{GroupVersion.Version}
		var internal apiextensions.CustomResourceDefinition
		if err := extensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(defaulted, &internal, nil); err != nil {
			t.Fatal(err)
		}
		if errs := crdvalidation.ValidateCustomResourceDefinition(context.Background(), &internal); len(errs) > 0 {
			t.Errorf("%s: %v", name, errs.ToAggregate())
		}
	}
}

func validateObject(t *testing.T, crd *extensionsv1.CustomResourceDefinition, object, old map[string]any) field.ErrorList {
	t.Helper()
	schema := internalSchema(t, crd)
	validator, _, err := validation.NewSchemaValidator(schema)
	if err != nil {
		t.Fatal(err)
	}
	errs := validation.ValidateCustomResource(nil, object, validator)
	structural, err := structuralschema.NewStructural(schema)
	if err != nil {
		t.Fatal(err)
	}
	celErrs, _ := cel.NewValidator(structural, true, celconfig.PerCallLimit).Validate(context.Background(), nil, structural, object, old, celconfig.RuntimeCELCostBudget)
	return append(errs, celErrs...)
}

func profileObject(cpu, memory any) map[string]any {
	return map[string]any{
		"apiVersion": "metal.datumapis.com/v1alpha1", "kind": "MachineProfile",
		"metadata": map[string]any{"name": "general-8c-32g"},
		"spec":     map[string]any{"cpu": cpu, "memory": memory},
	}
}

func TestMachineProfileAcceptsOnlyExactCPUAndMemory(t *testing.T) {
	crd := loadCRDs(t)["machineprofiles.metal.datumapis.com"]
	exactCPU := map[string]any{"architecture": "amd64", "cores": int64(8)}
	exactMemory := map[string]any{"capacity": "32Gi"}
	for _, capacity := range []string{"32Gi", "1Ti", "34359738368", "64G"} {
		if errs := validateObject(t, crd, profileObject(exactCPU, map[string]any{"capacity": capacity}), nil); len(errs) > 0 {
			t.Errorf("exact memory %q rejected: %v", capacity, errs.ToAggregate())
		}
	}
	for name, test := range map[string]struct{ cpu, memory any }{
		"legacy minimum cores":     {map[string]any{"architecture": "amd64", "cores": map[string]any{"min": int64(16)}}, exactMemory},
		"legacy exact cores":       {map[string]any{"architecture": "amd64", "cores": map[string]any{"exact": int64(8)}}, exactMemory},
		"legacy memory range":      {exactCPU, map[string]any{"capacity": map[string]any{"min": "32Gi", "max": "64Gi"}}},
		"missing cores":            {map[string]any{"architecture": "amd64"}, exactMemory},
		"zero cores":               {map[string]any{"architecture": "amd64", "cores": int64(0)}, exactMemory},
		"negative cores":           {map[string]any{"architecture": "amd64", "cores": int64(-1)}, exactMemory},
		"missing memory":           {exactCPU, map[string]any{}},
		"zero memory":              {exactCPU, map[string]any{"capacity": "0"}},
		"fractional memory":        {exactCPU, map[string]any{"capacity": "1.5Gi"}},
		"milli memory":             {exactCPU, map[string]any{"capacity": "500m"}},
		"unknown memory unit":      {exactCPU, map[string]any{"capacity": "32GB"}},
		"missing CPU architecture": {map[string]any{"cores": int64(8)}, exactMemory},
	} {
		if errs := validateObject(t, crd, profileObject(test.cpu, test.memory), nil); len(errs) == 0 {
			t.Errorf("%s was accepted", name)
		}
	}
}

func machineWithStatus(status map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "metal.datumapis.com/v1alpha1", "kind": "Machine",
		"metadata": map[string]any{"name": "example", "namespace": "default"},
		"spec": map[string]any{
			"machineClassRef":   map[string]any{"name": "standard"},
			"machineProfileRef": map[string]any{"name": "general-8c-32g"},
			"locationRef":       map[string]any{"name": "us-central-1"},
		},
		"status": status,
	}
}

func requested(cores int64, memory string) map[string]any {
	return map[string]any{
		"cpu":    map[string]any{"architecture": "amd64", "cores": cores},
		"memory": map[string]any{"capacity": memory},
		"source": map[string]any{
			"machineGeneration": int64(1),
			"profileRef":        map[string]any{"name": "general-8c-32g"},
			"profileGeneration": int64(1),
			"profileDigest":     "sha256:" + strings.Repeat("a", 64),
		},
	}
}

func TestMachineRequestedHardwareIsPinnedOnceAccepted(t *testing.T) {
	crd := loadCRDs(t)["machines.metal.datumapis.com"]
	pinned := machineWithStatus(map[string]any{"phase": "Pending", "requestedHardware": requested(8, "32Gi")})
	if errs := validateObject(t, crd, pinned, machineWithStatus(map[string]any{})); len(errs) > 0 {
		t.Fatalf("first resolution rejected: %v", errs.ToAggregate())
	}
	otherFields := machineWithStatus(map[string]any{"phase": "Ready", "requestedHardware": requested(8, "32Gi")})
	if errs := validateObject(t, crd, otherFields, pinned); len(errs) > 0 {
		t.Fatalf("unrelated status change rejected: %v", errs.ToAggregate())
	}
	for name, next := range map[string]map[string]any{
		"changed cores":  machineWithStatus(map[string]any{"phase": "Pending", "requestedHardware": requested(16, "32Gi")}),
		"changed memory": machineWithStatus(map[string]any{"phase": "Pending", "requestedHardware": requested(8, "64Gi")}),
		"removed":        machineWithStatus(map[string]any{"phase": "Pending"}),
	} {
		if errs := validateObject(t, crd, next, pinned); len(errs) == 0 {
			t.Errorf("%s snapshot was accepted", name)
		}
	}
	nonExact := machineWithStatus(map[string]any{"requestedHardware": requested(0, "32Gi")})
	if errs := validateObject(t, crd, nonExact, nil); len(errs) == 0 {
		t.Error("non-positive requested cores accepted")
	}
}

func TestMachinePrinterColumnsShowRequestedHardware(t *testing.T) {
	crd := loadCRDs(t)["machines.metal.datumapis.com"]
	columns := map[string]extensionsv1.CustomResourceColumnDefinition{}
	for _, column := range crd.Spec.Versions[0].AdditionalPrinterColumns {
		columns[column.Name] = column
	}
	for name, path := range map[string]string{
		"Requested CPU":    ".status.requestedHardware.cpu.cores",
		"Requested Memory": ".status.requestedHardware.memory.capacity",
	} {
		if column, ok := columns[name]; !ok || column.JSONPath != path || column.Priority != 0 {
			t.Errorf("default column %q = %#v, want %s", name, column, path)
		}
	}
	for name, path := range map[string]string{
		"Allocated CPU":    ".status.allocatedHardware.hardware.cpu.cores",
		"Allocated Memory": ".status.allocatedHardware.hardware.memory.capacity",
	} {
		if column, ok := columns[name]; !ok || column.JSONPath != path || column.Priority != 1 {
			t.Errorf("wide column %q = %#v, want %s at priority 1", name, column, path)
		}
	}
	for _, removed := range []string{"CPU", "Memory"} {
		if _, ok := columns[removed]; ok {
			t.Errorf("ambiguous column %q remains", removed)
		}
	}
}
