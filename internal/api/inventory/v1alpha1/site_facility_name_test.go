package v1alpha1

import "testing"

func TestIndependentSiteRejectsImpossibleFacilityNames(t *testing.T) {
	for _, name := range []string{"premises..one", "premises.-one", "premises-.one"} {
		t.Run(name, func(t *testing.T) {
			candidate := syntheticSite()
			candidate.Spec.FacilityRefs[0].Name = name
			if errs := admit(t, "Site", object(t, candidate), nil); len(errs) == 0 {
				t.Fatal("admitted a reference that cannot name a Kubernetes Facility")
			}
		})
	}
}
