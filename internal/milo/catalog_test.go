package milo

import (
	"os"
	"path/filepath"
	"testing"

	quotav1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	servicesv1 "go.miloapis.com/service-catalog/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestCatalogRegistrationMatchesAdmissionWithoutMutatingLegacyQuota(t *testing.T) {
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
	var current, legacy quotav1.ResourceRegistration
	read("machine-quota.yaml", &current)
	read("legacy-machine-quota.yaml", &legacy)
	if current.Name != "metal-machines" || current.Spec.ResourceType != MachineQuotaResource {
		t.Fatalf("new quota registration does not match Metal admission: %s / %s", current.Name, current.Spec.ResourceType)
	}
	if legacy.Name != "fleet-machines" || legacy.Spec.ResourceType != "fleet.datumapis.com/machines" {
		t.Fatal("legacy registration identity or immutable resourceType changed")
	}
	if legacy.Annotations["kustomize.toolkit.fluxcd.io/prune"] != "disabled" {
		t.Fatal("legacy quota obligations must survive Flux pruning")
	}
	for _, registration := range []*quotav1.ResourceRegistration{&current, &legacy} {
		if len(registration.Spec.ClaimingResources) != 1 || registration.Spec.ClaimingResources[0].Kind != "Machine" {
			t.Fatalf("invalid claiming resource for %s", registration.Name)
		}
	}
	if current.Spec.ClaimingResources[0].APIGroup != ServiceName || legacy.Spec.ClaimingResources[0].APIGroup != "fleet.datumapis.com" {
		t.Fatal("quota claiming resources must remain in their respective API groups")
	}
	var service servicesv1.Service
	var configuration servicesv1.ServiceConfiguration
	read("service.yaml", &service)
	read("service-configuration.yaml", &configuration)
	if service.Name != EntitlementName || service.Spec.ServiceName != ServiceName || configuration.Spec.ServiceRef.Name != service.Name {
		t.Fatal("catalog service references do not match admission")
	}
}
