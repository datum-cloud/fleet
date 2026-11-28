package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/milo"
	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

type RouteKind string

const (
	RouteClass           RouteKind = "Class"
	RouteProfile         RouteKind = "Profile"
	RouteInstallation    RouteKind = "Installation"
	RouteMachine         RouteKind = "Machine"
	RouteMachineCapacity RouteKind = "MachineCapacity"
	RouteNetwork         RouteKind = "Network"
	RouteQuota           RouteKind = "Quota"
	RouteLocation        RouteKind = "Location"
	RouteAvailability    RouteKind = "ServiceAvailability"
)

// RouteRequest keeps the dependency event itself in the workqueue. Index/List
// failures are returned by RouteReconciler and receive normal workqueue retry;
// they are never converted into an empty target set.
type RouteRequest struct {
	ClusterName multicluster.ClusterName
	AuthorityID string
	ProjectUID  types.UID
	Kind        RouteKind
	Namespace   string
	Name        string
	UID         types.UID
	Key         string
	// Deleted marks a snapshot delivered by an informer Delete event. The
	// snapshot's UID and routing fields describe the dependency lifetime that
	// disappeared, so resolution must not compare it with a same-name
	// replacement.
	Deleted bool
}

func (r RouteRequest) Cluster() multicluster.ClusterName { return r.ClusterName }

func (r RouteRequest) WithCluster(name multicluster.ClusterName) RouteRequest {
	r.ClusterName = name
	return r
}

func (r RouteRequest) String() string {
	return fmt.Sprintf("cluster://%s/%s/%s/%s/%s", r.ClusterName, r.Kind, r.Namespace, r.Name, r.UID)
}

func (r RouteRequest) Valid() bool {
	return strings.TrimSpace(r.AuthorityID) != "" && r.ProjectUID != "" && r.Kind != "" && r.Name != "" && r.UID != ""
}

func (m Mappers) Route(kind RouteKind) func(context.Context, client.Object) []RouteRequest {
	return func(_ context.Context, object client.Object) []RouteRequest {
		if object == nil || !m.Project.Valid() {
			return nil
		}
		request := RouteRequest{ClusterName: m.ClusterName, AuthorityID: m.Project.AuthorityID, ProjectUID: m.Project.UID, Kind: kind, Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
		switch kind {
		case RouteMachineCapacity:
			encoded, _ := json.Marshal(object.GetLabels())
			request.Key = string(encoded)
		case RouteNetwork:
			request.Key = object.GetNamespace() + "/" + object.GetName()
		case RouteQuota:
			// A quota claim names its Machine only by this label; a claim
			// without it wakes nothing.
			request.Key = object.GetLabels()["fleet.datumapis.com/machine-uid"]
			if request.Key == "" {
				return nil
			}
		case RouteLocation:
			request.Key = object.GetName()
		case RouteAvailability:
			availability, ok := object.(*servicesv1alpha1.ServiceAvailability)
			if !ok || availability.Spec.ServiceRef.Name != milo.EntitlementName || availability.Spec.LocationRef.Name == "" {
				return nil
			}
			request.Key = availability.Spec.LocationRef.Name
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
	if !route.Deleted {
		current, err := m.routeLifetimeCurrent(ctx, route)
		if err != nil {
			return nil, err
		}
		if !current {
			return nil, nil
		}
	}
	switch route.Kind {
	case RouteClass:
		return m.claimsByFieldE(ctx, IndexClaimClassName, route.Name)
	case RouteProfile:
		return m.claimsByFieldE(ctx, IndexClaimProfileName, route.Name)
	case RouteInstallation:
		return m.claimsByFieldE(ctx, IndexClaimInstallation, route.Namespace+"/"+route.Name)
	case RouteMachine:
		claim := fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: route.Name, UID: route.UID}}
		return m.installationsForClaimE(ctx, &claim)
	case RouteMachineCapacity:
		var eventLabels map[string]string
		if json.Unmarshal([]byte(route.Key), &eventLabels) != nil {
			return nil, ErrEventWiringUnavailable
		}
		return m.claimsForMachineE(ctx, &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: route.Name, UID: route.UID, Labels: eventLabels}})
	case RouteNetwork:
		return m.claimsByFieldE(ctx, IndexClaimNetwork, route.Key)
	case RouteQuota:
		return m.claimsByFieldE(ctx, IndexClaimUID, route.Key)
	case RouteLocation, RouteAvailability:
		return m.claimsByFieldE(ctx, IndexClaimLocationName, route.Key)
	default:
		return nil, ErrEventWiringUnavailable
	}
}

// routeLifetimeCurrent rejects a queued event when the same dependency name now
// belongs to a different UID. NotFound is retained as a valid deletion event;
// the replacement's Add event carries its own lifetime.
func (m Mappers) routeLifetimeCurrent(ctx context.Context, route RouteRequest) (bool, error) {
	if m.Reader == nil {
		return false, ErrEventWiringUnavailable
	}
	object, err := routeObject(route)
	if err != nil {
		return false, err
	}
	err = m.Reader.Get(ctx, client.ObjectKey{Namespace: route.Namespace, Name: route.Name}, object)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return object.GetUID() == route.UID, nil
}

func routeObject(route RouteRequest) (client.Object, error) {
	switch route.Kind {
	case RouteClass:
		return &fleetv1.MachineClass{}, nil
	case RouteProfile:
		return &fleetv1.MachineProfile{}, nil
	case RouteInstallation:
		return &fleetv1.Installation{}, nil
	case RouteMachine:
		return &fleetv1.Machine{}, nil
	case RouteMachineCapacity:
		return &fleetv1.MachineCapacity{}, nil
	case RouteNetwork:
		return &networkingv1.Network{}, nil
	case RouteQuota:
		return &quotav1alpha1.ResourceClaim{}, nil
	case RouteLocation:
		return &locationsv1alpha1.Location{}, nil
	case RouteAvailability:
		return &servicesv1alpha1.ServiceAvailability{}, nil
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
	if !r.Project.Valid() || r.Verifier == nil || r.Handler == nil {
		return reconcile.Result{}, ErrEventWiringUnavailable
	}
	if !request.Valid() || request.AuthorityID != r.Project.AuthorityID || request.ProjectUID != r.Project.UID {
		return reconcile.Result{}, nil
	}
	if err := r.Verifier.VerifyProject(ctx, r.Project); err != nil {
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
