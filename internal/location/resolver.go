package location

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	Resolve(context.Context, string) (fleetnetwork.LocationAuthority, error)
}

// Resolver converts Milo's current Location and ServiceAvailability evidence
// into the exact lifetime authority retained by Fleet allocation records.
// Callers must re-run it when either dependency changes; this package performs
// no polling and treats missing, stale, deleting, or ambiguous evidence as an
// unavailable authority.
type Resolver struct {
	Reader      client.Reader
	ServiceName string
}

func (r Resolver) Resolve(ctx context.Context, name string) (fleetnetwork.LocationAuthority, error) {
	if r.Reader == nil || name == "" {
		return fleetnetwork.LocationAuthority{}, ErrUnavailable
	}
	serviceName := r.ServiceName
	if serviceName == "" {
		serviceName = FleetServiceName
	}

	var location locationsv1alpha1.Location
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: name}, &location); err != nil {
		if apierrors.IsNotFound(err) {
			return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: %s does not exist", ErrUnavailable, name)
		}
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: read Location: %v", ErrUnavailable, err)
	}
	ready := apiMeta.FindStatusCondition(location.Status.Conditions, locationsv1alpha1.LocationConditionReady)
	if location.UID == "" || !location.DeletionTimestamp.IsZero() || !currentTrue(ready, location.Generation) {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: %s lacks current Ready=True evidence", ErrLocationNotReady, name)
	}

	var availabilities servicesv1alpha1.ServiceAvailabilityList
	if err := r.Reader.List(ctx, &availabilities); err != nil {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: list ServiceAvailability: %v", ErrUnavailable, err)
	}
	var matched *servicesv1alpha1.ServiceAvailability
	for i := range availabilities.Items {
		item := &availabilities.Items[i]
		if item.Spec.ServiceRef.Name != serviceName || item.Spec.LocationRef.Name != name {
			continue
		}
		if matched != nil {
			return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: multiple ServiceAvailability objects match %s at %s", ErrUnavailable, serviceName, name)
		}
		matched = item
	}
	if matched == nil {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: no ServiceAvailability matches %s at %s", ErrServiceUnavailable, serviceName, name)
	}
	available := apiMeta.FindStatusCondition(matched.Status.Conditions, "Available")
	if matched.UID == "" || !matched.DeletionTimestamp.IsZero() || matched.Status.ObservedGeneration != matched.Generation || !currentTrue(available, matched.Generation) {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: %s lacks current Available=True evidence", ErrServiceUnavailable, matched.Name)
	}

	revision, err := authorityRevision(location, *matched, ready, available)
	if err != nil {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: compute revision: %v", ErrUnavailable, err)
	}
	return fleetnetwork.LocationAuthority{Name: location.Name, UID: location.UID, Revision: revision}, nil
}

func currentTrue(condition *metav1.Condition, generation int64) bool {
	return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == generation
}

func authorityRevision(location locationsv1alpha1.Location, availability servicesv1alpha1.ServiceAvailability, ready, available *metav1.Condition) (string, error) {
	inputs := struct {
		LocationUID                    string `json:"locationUID"`
		LocationGeneration             int64  `json:"locationGeneration"`
		LocationReadyTransition        string `json:"locationReadyTransition"`
		AvailabilityUID                string `json:"availabilityUID"`
		AvailabilityGeneration         int64  `json:"availabilityGeneration"`
		AvailabilityObservedGeneration int64  `json:"availabilityObservedGeneration"`
		AvailabilityTransition         string `json:"availabilityTransition"`
	}{
		LocationUID: string(location.UID), LocationGeneration: location.Generation,
		LocationReadyTransition: ready.LastTransitionTime.UTC().Format(time.RFC3339Nano),
		AvailabilityUID:         string(availability.UID), AvailabilityGeneration: availability.Generation,
		AvailabilityObservedGeneration: availability.Status.ObservedGeneration,
		AvailabilityTransition:         available.LastTransitionTime.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
