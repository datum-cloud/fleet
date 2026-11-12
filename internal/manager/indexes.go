package manager

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/milo"
)

const (
	IndexClaimUID                         = "fleet.manager/claim-uid"
	IndexClaimClassName                   = "fleet.manager/claim-class-name"
	IndexClaimProfileName                 = "fleet.manager/claim-profile-name"
	IndexClaimLocationName                = "fleet.manager/claim-location-name"
	IndexClaimInstallation                = "fleet.manager/claim-installation-name"
	IndexClaimNetwork                     = "fleet.manager/claim-network"
	IndexInstallationClaim                = "fleet.manager/installation-claim-name"
	IndexClassMachineNS                   = "fleet.manager/class-machine-namespace"
	IndexPrivateClaimUID                  = "fleet.manager/private-claim-uid"
	IndexPrivateNamespaceUID              = "fleet.manager/private-namespace-uid"
	IndexPrivateMachineUID                = "fleet.manager/private-machine-uid"
	IndexPrivateInstallUID                = "fleet.manager/private-installation-uid"
	IndexPrivateNetworkUID                = "fleet.manager/private-network-uid"
	IndexPrivateNICClaimUID               = "fleet.manager/private-nic-claim-uid"
	IndexPrivateInterfaceUID              = "fleet.manager/private-interface-uid"
	IndexPrivateAuthorityID               = "fleet.manager/private-authority-record-id"
	IndexPrivateQuotaUID                  = "fleet.manager/private-quota-uid"
	IndexPrivateEntitlementUID            = "fleet.manager/private-entitlement-uid"
	IndexPrivatePendingEntitlementProject = "fleet.manager/private-pending-entitlement-project"
	IndexPrivateConsumerProject           = "fleet.manager/private-consumer-project"
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

// RegisterPrivateIndexes installs reverse edges on the management-cluster
// cache. Only service-owned Secrets in privateNamespace enter this cache.
func RegisterPrivateIndexes(ctx context.Context, indexer client.FieldIndexer, privateNamespace string) error {
	return registerIndexes(ctx, indexer, []indexRegistration{
		{&corev1.Secret{}, IndexPrivateClaimUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return one(string(route.ClaimUID)) })},
		{&corev1.Secret{}, IndexPrivateNamespaceUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uidStrings(route.NamespaceUIDs) })},
		{&corev1.Secret{}, IndexPrivateMachineUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uidStrings(route.MachineUIDs) })},
		{&corev1.Secret{}, IndexPrivateInstallUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return one(string(route.InstallationUID)) })},
		{&corev1.Secret{}, IndexPrivateNetworkUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uidStrings(route.NetworkUIDs) })},
		{&corev1.Secret{}, IndexPrivateNICClaimUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uidStrings(route.NICClaimUIDs) })},
		{&corev1.Secret{}, IndexPrivateInterfaceUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uidStrings(route.InterfaceUIDs) })},
		{&corev1.Secret{}, IndexPrivateAuthorityID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uniqueStrings(route.AuthorityRecordIDs) })},
		{&corev1.Secret{}, IndexPrivateQuotaUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uidStrings(route.QuotaUIDs) })},
		{&corev1.Secret{}, IndexPrivateEntitlementUID, privateMetadataIndexer(privateNamespace, func(route controller.PrivateRecordRoute) []string { return uidStrings(route.EntitlementUIDs) })},
		{&corev1.Secret{}, IndexPrivatePendingEntitlementProject, pendingEntitlementProjects(privateNamespace)},
		{&corev1.Secret{}, IndexPrivateConsumerProject, func(object client.Object) []string {
			secret, ok := object.(*corev1.Secret)
			if !ok || !trustedPrivateSecret(secret, privateNamespace) {
				return nil
			}
			route, ok := routePrivate(secret, privateNamespace)
			if !ok {
				return nil
			}
			return one(projectIndexKey(route.ConsumerProject.AuthorityID, route.ConsumerProject.UID))
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

func privateMetadataIndexer(namespace string, selectValues func(controller.PrivateRecordRoute) []string) client.IndexerFunc {
	return func(object client.Object) []string {
		secret, ok := object.(*corev1.Secret)
		if !ok {
			return nil
		}
		route, ok := routePrivate(secret, namespace)
		if !ok {
			return nil
		}
		return uniqueStrings(selectValues(route))
	}
}

func trustedPrivateSecret(secret *corev1.Secret, namespace string) bool {
	_, ok := routePrivate(secret, namespace)
	return ok
}

func trustedMiloAuthoritySecret(secret *corev1.Secret, namespace string) bool {
	return trustedPrivateSecret(secret, namespace) && secret.Type == corev1.SecretType("fleet.datumapis.com/private-milo-authority")
}

func pendingEntitlementProjects(privateNamespace string) client.IndexerFunc {
	return func(object client.Object) []string {
		secret, ok := object.(*corev1.Secret)
		if !ok || !trustedMiloAuthoritySecret(secret, privateNamespace) {
			return nil
		}
		route, ok := routePrivate(secret, privateNamespace)
		if !ok || !route.PendingEntitlement {
			return nil
		}
		return one(projectIndexKey(route.ConsumerProject.AuthorityID, route.ConsumerProject.UID))
	}
}

func one(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

func uidStrings(values []types.UID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, string(value))
		}
	}
	return uniqueStrings(result)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func routePrivate(secret *corev1.Secret, privateNamespace string) (controller.PrivateRecordRoute, bool) {
	if secret == nil || privateNamespace == "" || secret.Namespace != privateNamespace {
		return controller.PrivateRecordRoute{}, false
	}
	if route, err := controller.DecodePrivateLifecycleRoute(secret); err == nil {
		return route, route.ConsumerProject.Valid()
	}
	if route, err := milo.DecodePrivateAuthorityRoute(secret); err == nil {
		return route, route.ConsumerProject.Valid()
	}
	return controller.PrivateRecordRoute{}, false
}

func projectIndexKey(authorityID string, projectUID types.UID) string {
	return authorityID + "/" + string(projectUID)
}
