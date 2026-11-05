package location

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

func TestResolverRequiresCurrentLocationAndServiceEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := locationsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	location := readyLocation()
	availability := readyAvailability()
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(location, availability).Build()
	resolver := Resolver{Reader: reader}

	authority, err := resolver.Resolve(context.Background(), location.Name)
	if err != nil {
		t.Fatal(err)
	}
	if authority.Name != location.Name || authority.UID != location.UID || authority.Revision == "" {
		t.Fatalf("authority = %#v", authority)
	}

	stale := availability.DeepCopy()
	stale.Status.ObservedGeneration--
	reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(location, stale).Build()
	_, err = (Resolver{Reader: reader}).Resolve(context.Background(), location.Name)
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("stale availability error = %v", err)
	}
}

func TestResolverRejectsMissingAndAmbiguousAvailability(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := locationsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	location := readyLocation()
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(location).Build()
	_, err := (Resolver{Reader: reader}).Resolve(context.Background(), location.Name)
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("missing availability error = %v", err)
	}

	first := readyAvailability()
	second := readyAvailability()
	second.Name, second.UID = "fleet-central-second", types.UID("availability-uid-2")
	reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(location, first, second).Build()
	_, err = (Resolver{Reader: reader}).Resolve(context.Background(), location.Name)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ambiguous availability error = %v", err)
	}
}

func readyLocation() *locationsv1alpha1.Location {
	return &locationsv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{Name: "us-central-1", UID: "location-uid", Generation: 3},
		Status: locationsv1alpha1.LocationStatus{Conditions: []metav1.Condition{{
			Type: locationsv1alpha1.LocationConditionReady, Status: metav1.ConditionTrue, Reason: locationsv1alpha1.LocationReasonReady, ObservedGeneration: 3,
		}}},
	}
}

func readyAvailability() *servicesv1alpha1.ServiceAvailability {
	return &servicesv1alpha1.ServiceAvailability{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet--us-central-1", UID: "availability-uid", Generation: 2},
		Spec: servicesv1alpha1.ServiceAvailabilitySpec{
			ServiceRef:  servicesv1alpha1.ServiceRef{Name: FleetServiceName},
			LocationRef: servicesv1alpha1.LocationRef{Name: "us-central-1"},
		},
		Status: servicesv1alpha1.ServiceAvailabilityStatus{
			ObservedGeneration: 2,
			Conditions:         []metav1.Condition{{Type: "Available", Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 2}},
		},
	}
}
