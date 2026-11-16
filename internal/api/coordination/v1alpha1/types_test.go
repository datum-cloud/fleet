package v1alpha1

import (
	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestSessionAPIBoundaries(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := sessionv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, group string }{
		{"MachineManagementSession", "metal.datumapis.com"},
		{"MachineManagementSessionBinding", "metal.datumapis.com"},
		{"ProjectedMachineManagementSession", "coordination.fleet.datumapis.com"},
	} {
		for _, group := range []string{"metal.datumapis.com", "coordination.fleet.datumapis.com", "fleet.datumapis.com"} {
			_, err := s.New(schema.GroupVersionKind{Group: group, Version: "v1alpha1", Kind: tc.kind})
			if (err == nil) != (group == tc.group) {
				t.Errorf("%s in %s: %v", tc.kind, group, err)
			}
		}
	}
}
