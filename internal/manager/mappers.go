package manager

import (
	"context"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

// Mappers contains exact, indexed event routing for one project cache. The
// Project identity is configuration supplied by the authenticated cache owner,
// never read from a consumer object.
type Mappers struct {
	// Reader is the routed Milo project cache.
	Reader  client.Reader
	Project controller.ProjectIdentity
	// ClusterName is the engaged project cluster stamped on every request
	// these mappers produce.
	ClusterName multicluster.ClusterName
}

func (m Mappers) RequestFor(_ context.Context, object client.Object) []ProjectRequest {
	if object == nil || !m.Project.Valid() || object.GetNamespace() == "" || object.GetName() == "" || object.GetUID() == "" {
		return nil
	}
	return []ProjectRequest{{ClusterName: m.ClusterName, AuthorityID: m.Project.AuthorityID, ProjectUID: m.Project.UID, Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}}
}

func (m Mappers) installationsForClaimE(ctx context.Context, object client.Object) ([]ProjectRequest, error) {
	if m.Reader == nil || object == nil {
		return nil, ErrEventWiringUnavailable
	}
	var list fleetv1.InstallationList
	if err := m.Reader.List(ctx, &list, client.InNamespace(object.GetNamespace()), client.MatchingFields{IndexInstallationClaim: object.GetNamespace() + "/" + object.GetName()}); err != nil {
		return nil, err
	}
	result := make([]ProjectRequest, 0, len(list.Items))
	for i := range list.Items {
		result = append(result, m.RequestFor(ctx, &list.Items[i])...)
	}
	return uniqueRequests(result), nil
}

func (m Mappers) claimsForMachineE(ctx context.Context, object client.Object) ([]ProjectRequest, error) {
	machine, ok := object.(*fleetv1.MachineCapacity)
	if !ok || m.Reader == nil {
		return nil, ErrEventWiringUnavailable
	}
	var classes fleetv1.MachineClassList
	if err := m.Reader.List(ctx, &classes, client.MatchingFields{IndexClassMachineNS: machine.Namespace}); err != nil {
		return nil, err
	}
	var result []ProjectRequest
	for i := range classes.Items {
		class := &classes.Items[i]
		if class.Spec.Capacity == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&class.Spec.Capacity.MachineCapacities.Selector)
		if err != nil || !selector.Matches(labels.Set(machine.Labels)) {
			continue
		}
		claims, listErr := m.claimsByFieldE(ctx, IndexClaimClassName, class.Name)
		if listErr != nil {
			return nil, listErr
		}
		result = append(result, claims...)
	}
	return uniqueRequests(result), nil
}

func (m Mappers) claimsByFieldE(ctx context.Context, field, value string) ([]ProjectRequest, error) {
	if m.Reader == nil || value == "" {
		return nil, ErrEventWiringUnavailable
	}
	var list fleetv1.MachineList
	if err := m.Reader.List(ctx, &list, client.MatchingFields{field: value}); err != nil {
		return nil, err
	}
	result := make([]ProjectRequest, 0, len(list.Items))
	for i := range list.Items {
		result = append(result, m.RequestFor(ctx, &list.Items[i])...)
	}
	return uniqueRequests(result), nil
}

func uniqueRequests(values []ProjectRequest) []ProjectRequest {
	set := map[ProjectRequest]struct{}{}
	for _, value := range values {
		if value.Valid() {
			set[value] = struct{}{}
		}
	}
	result := make([]ProjectRequest, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].UID < result[j].UID
	})
	return result
}
