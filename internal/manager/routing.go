package manager

import (
	"context"
	"encoding/json"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

type RouteKind string

const (
	RouteClass        RouteKind = "Class"
	RouteProfile      RouteKind = "Profile"
	RouteInstallation RouteKind = "Installation"
	RouteClaim        RouteKind = "Claim"
	RouteMachine      RouteKind = "Machine"
	RoutePrivateUID   RouteKind = "PrivateUID"
	RouteNetwork      RouteKind = "Network"
	RouteQuota        RouteKind = "Quota"
	RouteEntitlement  RouteKind = "Entitlement"
)

// RouteRequest keeps the dependency event itself in the workqueue. Index/List
// failures are returned by RouteReconciler and receive normal workqueue retry;
// they are never converted into an empty target set.
type RouteRequest struct {
	AuthorityID string
	ProjectUID  types.UID
	Kind        RouteKind
	Namespace   string
	Name        string
	UID         types.UID
	Key         string
	Field       string
}

func (r RouteRequest) Valid() bool {
	return r.AuthorityID != "" && r.ProjectUID != "" && r.Kind != "" && r.Name != ""
}

func (m Mappers) Route(kind RouteKind, field string) func(context.Context, client.Object) []RouteRequest {
	return func(_ context.Context, object client.Object) []RouteRequest {
		if object == nil || !m.Project.Valid() {
			return nil
		}
		request := RouteRequest{AuthorityID: m.Project.AuthorityID, ProjectUID: m.Project.UID, Kind: kind, Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID(), Field: field}
		switch kind {
		case RouteMachine:
			encoded, _ := json.Marshal(object.GetLabels())
			request.Key = string(encoded)
		case RoutePrivateUID:
			request.Key = string(object.GetUID())
		case RouteNetwork:
			request.Key = object.GetNamespace() + "/" + object.GetName()
		case RouteQuota:
			request.Key = object.GetLabels()["fleet.datumapis.com/machine-claim-uid"]
		case RouteEntitlement:
			if object.GetName() != "fleet" {
				return nil
			}
		}
		if !request.Valid() {
			return nil
		}
		return []RouteRequest{request}
	}
}

func (m Mappers) ResolveRoute(ctx context.Context, route RouteRequest) ([]ProjectRequest, error) {
	if !route.Valid() || route.AuthorityID != m.Project.AuthorityID || route.ProjectUID != m.Project.UID {
		return nil, ErrEventWiringUnavailable
	}
	switch route.Kind {
	case RouteClass:
		return m.claimsByFieldE(ctx, IndexClaimClassName, route.Name)
	case RouteProfile:
		return m.claimsByFieldE(ctx, IndexClaimProfileName, route.Name)
	case RouteInstallation:
		return m.claimsByFieldE(ctx, IndexClaimInstallation, route.Namespace+"/"+route.Name)
	case RouteClaim:
		claim := fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: route.Name, UID: route.UID}}
		return m.installationsForClaimE(ctx, &claim)
	case RouteMachine:
		var eventLabels map[string]string
		if json.Unmarshal([]byte(route.Key), &eventLabels) != nil {
			return nil, ErrEventWiringUnavailable
		}
		return m.claimsForMachineE(ctx, &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: route.Name, UID: route.UID, Labels: eventLabels}})
	case RoutePrivateUID:
		return m.claimsForPrivateFieldE(ctx, route.Field, route.Key)
	case RouteNetwork:
		claims, err := m.claimsByFieldE(ctx, IndexClaimNetwork, route.Key)
		if err != nil {
			return nil, err
		}
		retained, err := m.claimsForPrivateFieldE(ctx, IndexPrivateNetworkUID, string(route.UID))
		return uniqueRequests(append(claims, retained...)), err
	case RouteQuota:
		claims, err := m.claimsForPrivateFieldE(ctx, IndexPrivateQuotaUID, string(route.UID))
		if err != nil {
			return nil, err
		}
		if route.Key != "" {
			byClaim, listErr := m.claimsForPrivateFieldE(ctx, IndexPrivateClaimUID, route.Key)
			if listErr != nil {
				return nil, listErr
			}
			claims = append(claims, byClaim...)
		}
		return uniqueRequests(claims), nil
	case RouteEntitlement:
		claims, err := m.claimsForPrivateFieldE(ctx, IndexPrivateEntitlementUID, string(route.UID))
		if err != nil {
			return nil, err
		}
		byProject, listErr := m.claimsForPrivateFieldE(ctx, IndexPrivatePendingEntitlementProject, projectIndexKey(m.Project.AuthorityID, m.Project.UID))
		if listErr != nil {
			return nil, listErr
		}
		return uniqueRequests(append(claims, byProject...)), nil
	default:
		return nil, ErrEventWiringUnavailable
	}
}

type RouteReconciler struct {
	Project  controller.ProjectIdentity
	Verifier ProjectVerifier
	Mappers  Mappers
	Handler  Handler
}

func (r RouteReconciler) Reconcile(ctx context.Context, request RouteRequest) (reconcile.Result, error) {
	if !r.Project.Valid() || r.Verifier == nil || r.Handler == nil || request.AuthorityID != r.Project.AuthorityID || request.ProjectUID != r.Project.UID {
		return reconcile.Result{}, nil
	}
	if err := r.Verifier.VerifyProject(ctx, r.Project); err != nil {
		if errors.Is(err, ErrEventWiringUnavailable) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	targets, err := r.Mappers.ResolveRoute(ctx, request)
	if err != nil {
		return reconcile.Result{}, err
	}
	for _, target := range targets {
		if outcome := r.Handler.ReconcileEvent(ctx, target); outcome.RetryError != nil {
			return reconcile.Result{}, outcome.RetryError
		}
	}
	return reconcile.Result{}, nil
}
