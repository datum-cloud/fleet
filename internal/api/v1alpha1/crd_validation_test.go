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

func TestCapacityUnconfiguredCleaningIsDisabledOnly(t *testing.T) {
	crd := loadCRDs(t)["machinecapacities.metal.datumapis.com"]
	for _, test := range []struct {
		name, policy, cleaning string
		allowed                bool
	}{
		{"inert registration", "Disabled", "", true},
		{"legacy synthetic", "Enabled", "synthetic-tenant-reuse-v1", true},
		{"unconfigured enabled", "Enabled", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := map[string]any{"apiVersion": "metal.datumapis.com/v1alpha1", "kind": "MachineCapacity", "metadata": map[string]any{"name": "record", "namespace": "default"}, "spec": map[string]any{"deviceRef": map[string]any{"name": "device"}, "allocationPolicy": test.policy, "cleaningPolicyRef": map[string]any{"name": test.cleaning}, "reclaimPolicy": "Retain"}}
			if errs := validateObject(t, crd, object, nil); (len(errs) == 0) != test.allowed {
				t.Fatalf("unexpected cleaning contract result: %v", errs)
			}
		})
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

// TestMachineImageDefinitionAndSourceNeverChange proves the catalog is
// immutable: a provider may only withdraw a definition, and a published
// copy can never be pointed at another source.
func TestMachineImageDefinitionAndSourceNeverChange(t *testing.T) {
	crd := loadCRDs(t)["machineimages.metal.datumapis.com"]
	digest := "sha256:" + strings.Repeat("ab", 32)
	definition := func(version, availability string) map[string]any {
		return map[string]any{"apiVersion": "metal.datumapis.com/v1alpha1", "kind": "MachineImage", "metadata": map[string]any{"name": "example-linux", "namespace": "default"}, "spec": map[string]any{
			"definition":   map[string]any{"family": "example-linux", "version": version, "architecture": "amd64", "firmwareModes": []any{"UEFI"}, "bootstrapFormats": []any{"NoCloud"}, "digest": digest},
			"availability": availability,
		}}
	}
	publisher := func(group, kind, name string) map[string]any {
		return map[string]any{"apiVersion": "metal.datumapis.com/v1alpha1", "kind": "MachineImage", "metadata": map[string]any{"name": "example-linux", "namespace": "default"}, "spec": map[string]any{
			"source": map[string]any{"publisherRef": map[string]any{"apiGroup": group, "kind": kind, "name": name}, "name": "example-linux"},
		}}
	}
	source := func(project string) map[string]any {
		return publisher("resourcemanager.miloapis.com", "Project", project)
	}
	both := definition("1.2", "Available")
	both["spec"].(map[string]any)["source"] = source("provider-example")["spec"].(map[string]any)["source"]
	bareProject := publisher("resourcemanager.miloapis.com", "Project", "provider-example")
	bareProject["spec"].(map[string]any)["source"] = map[string]any{"project": "provider-example", "name": "example-linux"}
	noAvailability := definition("1.2", "Available")
	delete(noAvailability["spec"].(map[string]any), "availability")
	copyWithAvailability := source("provider-example")
	copyWithAvailability["spec"].(map[string]any)["availability"] = "Available"
	copyWithDefinition := source("provider-example")
	copyWithDefinition["spec"].(map[string]any)["definition"] = definition("1.2", "Available")["spec"].(map[string]any)["definition"]
	copyBecomesDefinition := source("provider-example")
	delete(copyBecomesDefinition["spec"].(map[string]any), "source")
	copyBecomesDefinition["spec"].(map[string]any)["definition"] = copyWithDefinition["spec"].(map[string]any)["definition"]
	copyBecomesDefinition["spec"].(map[string]any)["availability"] = "Available"
	badDigest := definition("1.2", "Available")
	badDigest["spec"].(map[string]any)["definition"].(map[string]any)["digest"] = "sha256:short"
	for _, test := range []struct {
		name        string
		object, old map[string]any
		allowed     bool
	}{
		{"a definition", definition("1.2", "Available"), nil, true},
		{"a published copy", source("provider-example"), nil, true},
		{"both a definition and a source", both, nil, false},
		{"a definition without an availability", noAvailability, nil, false},
		{"a digest that is not sha256", badDigest, nil, false},
		{"a published copy with an availability", copyWithAvailability, nil, false},
		{"adding an availability to a published copy", copyWithAvailability, source("provider-example"), false},
		{"adding a definition to a published copy", copyWithDefinition, source("provider-example"), false},
		{"turning a published copy into a definition", copyBecomesDefinition, source("provider-example"), false},
		{"withdrawing a definition", definition("1.2", "Withdrawn"), definition("1.2", "Available"), true},
		// A provider may make a withdrawn release available again.
		{"restoring a withdrawn definition", definition("1.2", "Available"), definition("1.2", "Withdrawn"), true},
		{"changing a definition", definition("1.3", "Available"), definition("1.2", "Available"), false},
		{"turning a definition into a copy", source("provider-example"), definition("1.2", "Available"), false},
		{"repointing a published copy", source("provider-other"), source("provider-example"), false},
		{"a standalone copy published from a Namespace", publisher("", "Namespace", "fleet-provider"), nil, true},
		{"a publisher of another kind", publisher("resourcemanager.miloapis.com", "Organization", "provider-example"), nil, false},
		{"a Project publisher in the core group", publisher("", "Project", "provider-example"), nil, false},
		{"a Namespace publisher in another group", publisher("resourcemanager.miloapis.com", "Namespace", "fleet-provider"), nil, false},
		{"a publisher without a name", publisher("resourcemanager.miloapis.com", "Project", ""), nil, false},
		{"a bare project field", bareProject, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if errs := validateObject(t, crd, test.object, test.old); (len(errs) == 0) != test.allowed {
				t.Fatalf("allowed = %v, errors: %v", len(errs) == 0, errs)
			}
		})
	}
}

// TestPublicationRecordsReferenceASupportedTenant: a publication record's
// consumer is a typed reference to a Milo Project or a Namespace.
func TestPublicationRecordsReferenceASupportedTenant(t *testing.T) {
	crd := loadCRDs(t)["machineimagepublications.metal.datumapis.com"]
	record := func(group, kind string) map[string]any {
		return map[string]any{"apiVersion": "metal.datumapis.com/v1alpha1", "kind": "MachineImagePublication", "metadata": map[string]any{"name": "imgpub-example", "namespace": "fleet-provider"}, "spec": map[string]any{
			"consumerRef": map[string]any{"apiGroup": group, "kind": kind, "name": "consumer-example"},
			"image":       map[string]any{"namespace": "default", "name": "example-linux"},
			"source":      map[string]any{"name": "example-linux"},
			"digest":      "sha256:" + strings.Repeat("ab", 32),
		}}
	}
	for _, test := range []struct {
		name        string
		group, kind string
		allowed     bool
	}{
		{"a Milo Project", "resourcemanager.miloapis.com", "Project", true},
		{"a standalone Namespace", "", "Namespace", true},
		{"another kind", "resourcemanager.miloapis.com", "Organization", false},
		{"a Project in the core group", "", "Project", false},
		{"a Namespace in another group", "resourcemanager.miloapis.com", "Namespace", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if errs := validateObject(t, crd, record(test.group, test.kind), nil); (len(errs) == 0) != test.allowed {
				t.Fatalf("allowed = %v, errors: %v", len(errs) == 0, errs)
			}
		})
	}
}
