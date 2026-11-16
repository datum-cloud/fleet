package milo

import (
	"os"
	"path/filepath"
	"testing"

	quotav1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	servicesv1 "go.miloapis.com/service-catalog/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestCatalogRegistrationMatchesAdmission(t *testing.T) {
	read := func(name string, target any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "components", "service-catalog", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.UnmarshalStrict(data, target); err != nil {
			t.Fatal(err)
		}
	}
	var current quotav1.ResourceRegistration
	read("machine-quota.yaml", &current)
	if current.Name != "metal-machines" || current.Spec.ResourceType != MachineQuotaResource {
		t.Fatalf("new quota registration does not match Metal admission: %s / %s", current.Name, current.Spec.ResourceType)
	}
	if len(current.Spec.ClaimingResources) != 1 || current.Spec.ClaimingResources[0].Kind != "Machine" || current.Spec.ClaimingResources[0].APIGroup != ServiceName {
		t.Fatal("quota claiming resource does not match Metal admission")
	}
	var service servicesv1.Service
	var configuration servicesv1.ServiceConfiguration
	read("service.yaml", &service)
	read("service-configuration.yaml", &configuration)
	if service.Name != EntitlementName || service.Spec.ServiceName != ServiceName || configuration.Spec.ServiceRef.Name != service.Name {
		t.Fatal("catalog service references do not match admission")
	}
}
