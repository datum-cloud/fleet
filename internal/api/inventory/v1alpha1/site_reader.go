package v1alpha1

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ReadSiteFacilities(ctx context.Context, reader client.Reader, site *Site) ([]Facility, error) {
	if reader == nil || site == nil || site.Namespace == "" || site.UID == "" || !site.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("site membership unavailable")
	}
	facilities := make([]Facility, 0, len(site.Spec.FacilityRefs))
	seen := map[string]bool{}
	seenNames := map[string]bool{}
	for _, reference := range site.Spec.FacilityRefs {
		if reference.Name == "" || reference.UID == "" || seen[reference.UID] || seenNames[reference.Name] {
			return nil, fmt.Errorf("invalid facility membership reference")
		}
		seen[reference.UID] = true
		seenNames[reference.Name] = true
		var facility Facility
		if err := reader.Get(ctx, client.ObjectKey{Namespace: site.Namespace, Name: reference.Name}, &facility); err != nil {
			return nil, fmt.Errorf("facility membership unavailable")
		}
		if string(facility.UID) != reference.UID || !facility.DeletionTimestamp.IsZero() {
			return nil, fmt.Errorf("facility membership incarnation unavailable")
		}
		facilities = append(facilities, facility)
	}
	return facilities, nil
}
