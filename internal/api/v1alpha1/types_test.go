package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersPublicKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("register Fleet API: %v", err)
	}

	for _, kind := range []string{
		"MachineClass", "MachineClassList",
		"MachineProfile", "MachineProfileList",
		"MachineCapacity", "MachineCapacityList",
		"Machine", "MachineList",
		"MachineAllocation", "MachineAllocationList",
		"Installation", "InstallationList",
	} {
		gvk := GroupVersion.WithKind(kind)
		if _, err := scheme.New(gvk); err != nil {
			t.Errorf("kind %s is not registered: %v", gvk, err)
		}
	}
}

func TestDeepCopyObjectDoesNotAliasNestedState(t *testing.T) {
	original := &Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Labels: map[string]string{"owner": "consumer-a"}},
		Spec: MachineSpec{
			MachineClassRef:   LocalReference{Name: "standard"},
			MachineProfileRef: LocalReference{Name: "general-purpose"},
			LocationRef:       LocalReference{Name: "us-central-1"},
			NetworkInterfaces: []NetworkInterfaceRequest{{
				Name:       "management",
				Network:    NamespacedReference{Name: "management"},
				IPFamilies: []string{"IPv6"},
			}},
		},
	}

	copy := original.DeepCopyObject().(*Machine)
	copy.Labels["owner"] = "consumer-b"
	copy.Spec.NetworkInterfaces[0].IPFamilies[0] = "IPv4"

	if got := original.Labels["owner"]; got != "consumer-a" {
		t.Fatalf("metadata map aliased by deep copy: %q", got)
	}
	if got := original.Spec.NetworkInterfaces[0].IPFamilies[0]; got != "IPv6" {
		t.Fatalf("nested slice aliased by deep copy: %q", got)
	}
}
