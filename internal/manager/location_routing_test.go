package manager

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

func TestServiceAvailabilityRoutesOnlyMatchingLocationClaims(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	central := &fleetv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "central", UID: "central-uid"},
		Spec:       fleetv1.MachineSpec{LocationRef: fleetv1.LocalReference{Name: "us-central-1"}},
	}
	east := &fleetv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "east", UID: "east-uid"},
		Spec:       fleetv1.MachineSpec{LocationRef: fleetv1.LocalReference{Name: "us-east-1"}},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(central, east).
		WithIndex(&fleetv1.Machine{}, IndexClaimLocationName, func(object client.Object) []string {
			return one(object.(*fleetv1.Machine).Spec.LocationRef.Name)
		}).Build()
	project := controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "project-uid"}
	mappers := Mappers{Reader: reader, Project: project}
	availability := &servicesv1alpha1.ServiceAvailability{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet--us-central-1", UID: "availability-uid"},
		Spec: servicesv1alpha1.ServiceAvailabilitySpec{
			ServiceRef:  servicesv1alpha1.ServiceRef{Name: "fleet"},
			LocationRef: servicesv1alpha1.LocationRef{Name: "us-central-1"},
		},
	}
	routes := mappers.Route(RouteAvailability, "")(context.Background(), availability)
	if len(routes) != 1 || routes[0].Key != "us-central-1" {
		t.Fatalf("routes = %#v", routes)
	}
	routes[0].Deleted = true // A tombstone resolves from its retained spec.
	requests, err := mappers.ResolveRoute(context.Background(), routes[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Name != central.Name || requests[0].UID != central.UID {
		t.Fatalf("requests = %#v", requests)
	}
}
