package location

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"

	fleetnetwork "go.datum.net/fleet/internal/network"
)

const FleetServiceName = "fleet"

var (
	ErrUnavailable        = errors.New("location authority is unavailable")
	ErrLocationNotReady   = errors.New("location is not ready")
	ErrServiceUnavailable = errors.New("Fleet is not available at the location")
)

type AuthorityResolver interface {
	Resolve(context.Context, string) (Resolution, error)
}

// Evidence is a trusted, replayable receipt produced by the owner of the
// ServiceAvailability projection. The current Milo ServiceAvailability API does
// not expose the Location UID it evaluated or a refresh timestamp, so neither
// value may be inferred from its condition or annotations.
type Evidence struct {
	ProjectionLocationUID            types.UID
	ProjectionLocationGeneration     int64
	ProjectionLocationDigest         string
	ProjectionAvailabilityUID        types.UID
	ProjectionAvailabilityGeneration int64
	ProjectionAvailabilityDigest     string
	SourceLocationUID                types.UID
	SourceLocationGeneration         int64
	SourceAvailabilityUID            types.UID
	SourceAvailabilityGeneration     int64
	ObservedAt                       time.Time
	ExpiresAt                        time.Time
	SourceRevision                   string
}

// EvidenceVerifier validates the authority and integrity of an Evidence
// receipt. Production wiring must obtain this from Milo's projection owner; a
// project-local object or caller-supplied annotation is not sufficient.
type EvidenceVerifier interface {
	Verify(context.Context, *locationsv1alpha1.Location, *servicesv1alpha1.ServiceAvailability) (Evidence, error)
}

type EvidenceVerifierFunc func(context.Context, *locationsv1alpha1.Location, *servicesv1alpha1.ServiceAvailability) (Evidence, error)

func (f EvidenceVerifierFunc) Verify(ctx context.Context, location *locationsv1alpha1.Location, availability *servicesv1alpha1.ServiceAvailability) (Evidence, error) {
	if f == nil {
		return Evidence{}, ErrUnavailable
	}
	return f(ctx, location, availability)
}

// Resolution carries the one-shot deadline that a lifecycle coordinator must
// schedule through the event manager. Rechecking at ExpiresAt makes freshness
// expire without polling or another Kubernetes API event.
type Resolution struct {
	Authority fleetnetwork.LocationAuthority
	ExpiresAt time.Time
}

// Resolver converts Milo's current Location and ServiceAvailability evidence
// into the exact lifetime authority retained by Fleet allocation records.
// Callers must re-run it when either dependency changes; this package performs
// no polling and treats missing, stale, deleting, or ambiguous evidence as an
// unavailable authority.
type Resolver struct {
	Reader      client.Reader
	ServiceName string
	Verifier    EvidenceVerifier
	Now         func() time.Time
}

func (r Resolver) Resolve(ctx context.Context, name string) (Resolution, error) {
	if r.Reader == nil || r.Verifier == nil || name == "" {
		return Resolution{}, ErrUnavailable
	}
	serviceName := r.ServiceName
	if serviceName == "" {
		serviceName = FleetServiceName
	}

	var location locationsv1alpha1.Location
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: name}, &location); err != nil {
		if apierrors.IsNotFound(err) {
			return Resolution{}, fmt.Errorf("%w: %s does not exist", ErrUnavailable, name)
		}
		return Resolution{}, fmt.Errorf("%w: read Location: %v", ErrUnavailable, err)
	}
	ready := apiMeta.FindStatusCondition(location.Status.Conditions, locationsv1alpha1.LocationConditionReady)
	// Milo deliberately clears observedGeneration when mirroring a platform
	// Location condition into a project because the source generation has no
	// meaning against the local projection. The trusted receipt below, not this
	// condition field, binds the projection to its canonical source lifetime.
	if location.UID == "" || !location.DeletionTimestamp.IsZero() || !conditionTrue(ready) {
		return Resolution{}, fmt.Errorf("%w: %s lacks current Ready=True evidence", ErrLocationNotReady, name)
	}

	var availabilities servicesv1alpha1.ServiceAvailabilityList
	if err := r.Reader.List(ctx, &availabilities); err != nil {
		return Resolution{}, fmt.Errorf("%w: list ServiceAvailability: %v", ErrUnavailable, err)
	}
	var matched *servicesv1alpha1.ServiceAvailability
	for i := range availabilities.Items {
		item := &availabilities.Items[i]
		if item.Spec.ServiceRef.Name != serviceName || item.Spec.LocationRef.Name != name {
			continue
		}
		if matched != nil {
			return Resolution{}, fmt.Errorf("%w: multiple ServiceAvailability objects match %s at %s", ErrUnavailable, serviceName, name)
		}
		matched = item
	}
	if matched == nil {
		return Resolution{}, fmt.Errorf("%w: no ServiceAvailability matches %s at %s", ErrServiceUnavailable, serviceName, name)
	}
	available := apiMeta.FindStatusCondition(matched.Status.Conditions, "Available")
	// ServiceAvailability status is copied verbatim from the platform object, so
	// its observedGeneration refers to the source generation rather than the
	// mirror generation. It cannot establish local or canonical identity.
	if matched.UID == "" || !matched.DeletionTimestamp.IsZero() || !conditionTrue(available) {
		return Resolution{}, fmt.Errorf("%w: %s lacks current Available=True evidence", ErrServiceUnavailable, matched.Name)
	}
	evidence, err := r.Verifier.Verify(ctx, &location, matched)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: verify availability evidence: %v", ErrUnavailable, err)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if err := validateEvidence(evidence, &location, matched, now()); err != nil {
		return Resolution{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	revision, err := authorityRevision(location, *matched, ready, available, evidence)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: compute revision: %v", ErrUnavailable, err)
	}
	return Resolution{Authority: fleetnetwork.LocationAuthority{Name: location.Name, UID: evidence.SourceLocationUID, Revision: revision}, ExpiresAt: evidence.ExpiresAt}, nil
}

func conditionTrue(condition *metav1.Condition) bool {
	return condition != nil && condition.Status == metav1.ConditionTrue
}

func validateEvidence(e Evidence, location *locationsv1alpha1.Location, availability *servicesv1alpha1.ServiceAvailability, now time.Time) error {
	if location == nil || availability == nil || strings.TrimSpace(e.SourceRevision) == "" || e.ProjectionLocationUID == "" || e.ProjectionLocationGeneration < 1 || e.ProjectionLocationDigest == "" || e.ProjectionAvailabilityUID == "" || e.ProjectionAvailabilityGeneration < 1 || e.ProjectionAvailabilityDigest == "" || e.SourceLocationUID == "" || e.SourceLocationGeneration < 1 || e.SourceAvailabilityUID == "" || e.SourceAvailabilityGeneration < 1 || e.ObservedAt.IsZero() || e.ExpiresAt.IsZero() {
		return errors.New("trusted availability receipt is incomplete")
	}
	if e.ProjectionLocationUID != location.UID || e.ProjectionLocationGeneration != location.Generation || e.ProjectionAvailabilityUID != availability.UID || e.ProjectionAvailabilityGeneration != availability.Generation {
		return errors.New("trusted availability receipt does not bind the current projection lifetimes")
	}
	locationDigest, err := projectionDigest(location)
	if err != nil || e.ProjectionLocationDigest != locationDigest {
		return errors.New("trusted availability receipt does not bind the current Location projection")
	}
	availabilityDigest, err := projectionDigest(availability)
	if err != nil || e.ProjectionAvailabilityDigest != availabilityDigest {
		return errors.New("trusted availability receipt does not bind the current ServiceAvailability projection")
	}
	if !e.ExpiresAt.After(e.ObservedAt) || e.ObservedAt.After(now) || !now.Before(e.ExpiresAt) {
		return errors.New("trusted availability receipt is not currently fresh")
	}
	return nil
}

// projectionDigest binds the receipt to the complete typed local projection
// input while excluding API transport metadata. Status-only changes do not bump
// generation, so UID and generation alone are insufficient.
func projectionDigest(object any) (string, error) {
	var input any
	switch value := object.(type) {
	case *locationsv1alpha1.Location:
		input = struct {
			Name              string
			UID               types.UID
			Generation        int64
			DeletionTimestamp *metav1.Time
			Spec              locationsv1alpha1.LocationSpec
			Status            locationsv1alpha1.LocationStatus
		}{value.Name, value.UID, value.Generation, value.DeletionTimestamp, value.Spec, value.Status}
	case *servicesv1alpha1.ServiceAvailability:
		input = struct {
			Name              string
			UID               types.UID
			Generation        int64
			DeletionTimestamp *metav1.Time
			Spec              servicesv1alpha1.ServiceAvailabilitySpec
			Status            servicesv1alpha1.ServiceAvailabilityStatus
		}{value.Name, value.UID, value.Generation, value.DeletionTimestamp, value.Spec, value.Status}
	default:
		return "", errors.New("unsupported projection type")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func authorityRevision(location locationsv1alpha1.Location, availability servicesv1alpha1.ServiceAvailability, ready, available *metav1.Condition, evidence Evidence) (string, error) {
	inputs := struct {
		ProjectionLocationUID            string `json:"projectionLocationUID"`
		ProjectionLocationGeneration     int64  `json:"projectionLocationGeneration"`
		ProjectionLocationDigest         string `json:"projectionLocationDigest"`
		SourceLocationUID                string `json:"sourceLocationUID"`
		SourceLocationGeneration         int64  `json:"sourceLocationGeneration"`
		LocationReadyTransition          string `json:"locationReadyTransition"`
		ProjectionAvailabilityUID        string `json:"projectionAvailabilityUID"`
		ProjectionAvailabilityGeneration int64  `json:"projectionAvailabilityGeneration"`
		ProjectionAvailabilityDigest     string `json:"projectionAvailabilityDigest"`
		SourceAvailabilityUID            string `json:"sourceAvailabilityUID"`
		SourceAvailabilityGeneration     int64  `json:"sourceAvailabilityGeneration"`
		AvailabilityObservedGeneration   int64  `json:"availabilityObservedGeneration"`
		AvailabilityTransition           string `json:"availabilityTransition"`
		EvidenceObservedAt               string `json:"evidenceObservedAt"`
		EvidenceExpiresAt                string `json:"evidenceExpiresAt"`
		SourceRevision                   string `json:"sourceRevision"`
	}{
		ProjectionLocationUID: string(location.UID), ProjectionLocationGeneration: location.Generation,
		ProjectionLocationDigest: evidence.ProjectionLocationDigest,
		SourceLocationUID:        string(evidence.SourceLocationUID), SourceLocationGeneration: evidence.SourceLocationGeneration,
		LocationReadyTransition:   ready.LastTransitionTime.UTC().Format(time.RFC3339Nano),
		ProjectionAvailabilityUID: string(availability.UID), ProjectionAvailabilityGeneration: availability.Generation,
		ProjectionAvailabilityDigest: evidence.ProjectionAvailabilityDigest,
		SourceAvailabilityUID:        string(evidence.SourceAvailabilityUID), SourceAvailabilityGeneration: evidence.SourceAvailabilityGeneration,
		AvailabilityObservedGeneration: availability.Status.ObservedGeneration,
		AvailabilityTransition:         available.LastTransitionTime.UTC().Format(time.RFC3339Nano),
		EvidenceObservedAt:             evidence.ObservedAt.UTC().Format(time.RFC3339Nano),
		EvidenceExpiresAt:              evidence.ExpiresAt.UTC().Format(time.RFC3339Nano),
		SourceRevision:                 evidence.SourceRevision,
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
