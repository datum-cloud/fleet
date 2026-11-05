package location

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	resolver := resolverWithEvidence(reader, now, location, availability)

	resolution, err := resolver.Resolve(context.Background(), location.Name)
	if err != nil {
		t.Fatal(err)
	}
	if authority := resolution.Authority; authority.Name != location.Name || authority.UID != types.UID("canonical-location-uid") || authority.Revision == "" {
		t.Fatalf("authority = %#v", authority)
	}
	if !resolution.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("expiresAt = %v", resolution.ExpiresAt)
	}

	blocked := availability.DeepCopy()
	blocked.Status.Conditions[0].Status = metav1.ConditionFalse
	reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(location, blocked).Build()
	_, err = resolverWithEvidence(reader, now, location, blocked).Resolve(context.Background(), location.Name)
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("unavailable service error = %v", err)
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
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(location).Build()
	_, err := resolverWithEvidence(reader, now, location, nil).Resolve(context.Background(), location.Name)
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("missing availability error = %v", err)
	}

	first := readyAvailability()
	second := readyAvailability()
	second.Name, second.UID = "fleet-central-second", types.UID("availability-uid-2")
	reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(location, first, second).Build()
	_, err = resolverWithEvidence(reader, now, location, first).Resolve(context.Background(), location.Name)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ambiguous availability error = %v", err)
	}
}

func TestResolverRejectsUntrustedExpiredAndReplacementEvidence(t *testing.T) {
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
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	if _, err := (Resolver{Reader: reader, Now: func() time.Time { return now }}).Resolve(context.Background(), location.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing verifier error = %v", err)
	}

	expired := evidenceFor(now, location, availability)
	expired.ExpiresAt = now
	resolver := Resolver{Reader: reader, Now: func() time.Time { return now }, Verifier: staticEvidence(expired)}
	if _, err := resolver.Resolve(context.Background(), location.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired evidence error = %v", err)
	}

	replacement := location.DeepCopy()
	replacement.UID = "replacement-location-uid"
	reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(replacement, availability).Build()
	resolver = Resolver{Reader: reader, Now: func() time.Time { return now }, Verifier: staticEvidence(evidenceFor(now, location, availability))}
	if _, err := resolver.Resolve(context.Background(), replacement.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replacement lifetime error = %v", err)
	}

	// Status updates do not increment generation. A receipt for an earlier
	// projection snapshot must not survive a True-to-True semantic update.
	changedAvailability := availability.DeepCopy()
	changedAvailability.Status.Conditions[0].Message = "new platform observation"
	reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(location, changedAvailability).Build()
	resolver = Resolver{Reader: reader, Now: func() time.Time { return now }, Verifier: staticEvidence(evidenceFor(now, location, availability))}
	if _, err := resolver.Resolve(context.Background(), location.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("status substitution error = %v", err)
	}
}

func TestResolutionDeadlineExpiresWithoutAPIEvent(t *testing.T) {
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
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	evidence := evidenceFor(now, location, availability)
	resolver := Resolver{Reader: reader, Now: func() time.Time { return now }, Verifier: staticEvidence(evidence)}

	resolution, err := resolver.Resolve(context.Background(), location.Name)
	if err != nil {
		t.Fatal(err)
	}
	now = resolution.ExpiresAt
	if _, err := resolver.Resolve(context.Background(), location.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("evidence remained usable at its deadline: %v", err)
	}
}

func resolverWithEvidence(reader client.Reader, now time.Time, location *locationsv1alpha1.Location, availability *servicesv1alpha1.ServiceAvailability) Resolver {
	return Resolver{Reader: reader, Now: func() time.Time { return now }, Verifier: staticEvidence(evidenceFor(now, location, availability))}
}

func evidenceFor(now time.Time, location *locationsv1alpha1.Location, availability *servicesv1alpha1.ServiceAvailability) Evidence {
	evidence := Evidence{
		SourceLocationUID:            "canonical-location-uid",
		SourceLocationGeneration:     12,
		SourceAvailabilityUID:        "canonical-availability-uid",
		SourceAvailabilityGeneration: 9,
		ObservedAt:                   now.Add(-time.Minute),
		ExpiresAt:                    now.Add(time.Minute),
		SourceRevision:               "receipt-r1",
	}
	if location != nil {
		evidence.ProjectionLocationUID = location.UID
		evidence.ProjectionLocationGeneration = location.Generation
		evidence.ProjectionLocationDigest, _ = projectionDigest(location)
	}
	if availability != nil {
		evidence.ProjectionAvailabilityUID = availability.UID
		evidence.ProjectionAvailabilityGeneration = availability.Generation
		evidence.ProjectionAvailabilityDigest, _ = projectionDigest(availability)
	}
	return evidence
}

func staticEvidence(evidence Evidence) EvidenceVerifier {
	return EvidenceVerifierFunc(func(context.Context, *locationsv1alpha1.Location, *servicesv1alpha1.ServiceAvailability) (Evidence, error) {
		return evidence, nil
	})
}

func readyLocation() *locationsv1alpha1.Location {
	return &locationsv1alpha1.Location{
		// This is shaped like Milo's project-local projection: its UID and
		// generation are local, and the mirrored source Ready condition has its
		// observedGeneration deliberately cleared.
		ObjectMeta: metav1.ObjectMeta{Name: "us-central-1", UID: "project-location-uid", Generation: 1},
		Spec: locationsv1alpha1.LocationSpec{
			LocationClassRef: locationsv1alpha1.LocationClassReference{Name: "datum-managed"},
			Topology:         map[string]string{"topology.datum.net/city-code": "ORD"},
		},
		Status: locationsv1alpha1.LocationStatus{Conditions: []metav1.Condition{{
			Type: locationsv1alpha1.LocationConditionReady, Status: metav1.ConditionTrue, Reason: locationsv1alpha1.LocationReasonReady, ObservedGeneration: 0,
		}}},
	}
}

func readyAvailability() *servicesv1alpha1.ServiceAvailability {
	return &servicesv1alpha1.ServiceAvailability{
		// Milo copies source status verbatim, so observedGeneration identifies
		// the source generation and is unrelated to this mirror's generation.
		ObjectMeta: metav1.ObjectMeta{Name: "fleet--us-central-1", UID: "project-availability-uid", Generation: 1},
		Spec: servicesv1alpha1.ServiceAvailabilitySpec{
			ServiceRef:  servicesv1alpha1.ServiceRef{Name: FleetServiceName},
			LocationRef: servicesv1alpha1.LocationRef{Name: "us-central-1"},
		},
		Status: servicesv1alpha1.ServiceAvailabilityStatus{
			ObservedGeneration: 9,
			Conditions:         []metav1.Condition{{Type: "Available", Status: metav1.ConditionTrue, Reason: "ServiceOperational", ObservedGeneration: 9}},
		},
	}
}
