package manager

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

const (
	IndexClaimUID          = "fleet.manager/claim-uid"
	IndexClaimClassName    = "fleet.manager/claim-class-name"
	IndexClaimProfileName  = "fleet.manager/claim-profile-name"
	IndexClaimLocationName = "fleet.manager/claim-location-name"
	IndexClaimInstallation = "fleet.manager/claim-installation-name"
	IndexClaimNetwork      = "fleet.manager/claim-network"
	IndexInstallationClaim = "fleet.manager/installation-claim-name"
	IndexClassMachineNS    = "fleet.manager/class-machine-namespace"
)

type indexRegistration struct {
	object client.Object
	field  string
	fn     client.IndexerFunc
}

// RegisterProjectIndexes installs reverse edges on the routed Milo project
// cache. Public resources must never be indexed from the host API.
func RegisterProjectIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return registerIndexes(ctx, indexer, []indexRegistration{
		{&fleetv1.Machine{}, IndexClaimUID, func(o client.Object) []string { return one(string(o.GetUID())) }},
		{&fleetv1.Machine{}, IndexClaimClassName, func(o client.Object) []string { return one(o.(*fleetv1.Machine).Spec.MachineClassRef.Name) }},
		{&fleetv1.Machine{}, IndexClaimProfileName, func(o client.Object) []string { return one(o.(*fleetv1.Machine).Spec.MachineProfileRef.Name) }},
		{&fleetv1.Machine{}, IndexClaimLocationName, func(o client.Object) []string { return one(o.(*fleetv1.Machine).Spec.LocationRef.Name) }},
		{&fleetv1.Machine{}, IndexClaimInstallation, func(o client.Object) []string {
			claim := o.(*fleetv1.Machine)
			if ref := claim.Spec.InitialInstallationRef; ref != nil && claim.Namespace != "" && ref.Name != "" {
				return one(claim.Namespace + "/" + ref.Name)
			}
			return nil
		}},
		{&fleetv1.Machine{}, IndexClaimNetwork, func(o client.Object) []string {
			claim := o.(*fleetv1.Machine)
			values := make([]string, 0, len(claim.Spec.NetworkInterfaces))
			for _, attachment := range claim.Spec.NetworkInterfaces {
				namespace := attachment.Network.Namespace
				if namespace == "" {
					namespace = claim.Namespace
				}
				if namespace != "" && attachment.Network.Name != "" {
					values = append(values, namespace+"/"+attachment.Network.Name)
				}
			}
			return values
		}},
		{&fleetv1.Installation{}, IndexInstallationClaim, func(o client.Object) []string {
			installation := o.(*fleetv1.Installation)
			if installation.Namespace == "" || installation.Spec.MachineRef.Name == "" {
				return nil
			}
			return one(installation.Namespace + "/" + installation.Spec.MachineRef.Name)
		}},
		{&fleetv1.MachineClass{}, IndexClassMachineNS, func(o client.Object) []string {
			c := o.(*fleetv1.MachineClass)
			if c.Spec.Capacity == nil {
				return nil
			}
			return one(c.Spec.Capacity.MachineCapacities.Namespace)
		}},
	})
}

func registerIndexes(ctx context.Context, indexer client.FieldIndexer, registrations []indexRegistration) error {
	for _, registration := range registrations {
		if err := indexer.IndexField(ctx, registration.object, registration.field, registration.fn); err != nil {
			return fmt.Errorf("register %s: %w", registration.field, err)
		}
	}
	return nil
}

func one(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}
