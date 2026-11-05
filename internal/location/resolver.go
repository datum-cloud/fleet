package location

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

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

// Resolver applies the same project-visible location contract used by the
// platform's existing services: a ready Location projection and one available
// ServiceAvailability projection for Fleet. Changes to either object are routed
// to affected claims by the manager watches; this package performs no polling.
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
	// Milo projections intentionally omit observedGeneration because the source
	// generation is not meaningful for the project-local object.
	if location.UID == "" || !location.DeletionTimestamp.IsZero() || !conditionTrue(ready) {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: %s lacks Ready=True", ErrLocationNotReady, name)
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
	// Mirrored availability status refers to the root object, not the local
	// projection generation, so availability is the condition verdict itself.
	if matched.UID == "" || !matched.DeletionTimestamp.IsZero() || !conditionTrue(available) {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: %s lacks Available=True", ErrServiceUnavailable, matched.Name)
	}

	revision, err := authorityRevision(location, *matched)
	if err != nil {
		return fleetnetwork.LocationAuthority{}, fmt.Errorf("%w: compute revision: %v", ErrUnavailable, err)
	}
	return fleetnetwork.LocationAuthority{Name: location.Name, UID: location.UID, Revision: revision}, nil
}

func conditionTrue(condition *metav1.Condition) bool {
	return condition != nil && condition.Status == metav1.ConditionTrue
}

// authorityRevision changes whenever the project-visible eligibility inputs
// change. It is an MVP projection revision, not canonical root provenance.
func authorityRevision(location locationsv1alpha1.Location, availability servicesv1alpha1.ServiceAvailability) (string, error) {
	input := struct {
		LocationName           string
		LocationUID            string
		LocationGeneration     int64
		LocationSpec           locationsv1alpha1.LocationSpec
		LocationStatus         locationsv1alpha1.LocationStatus
		AvailabilityName       string
		AvailabilityUID        string
		AvailabilityGeneration int64
		AvailabilitySpec       servicesv1alpha1.ServiceAvailabilitySpec
		AvailabilityStatus     servicesv1alpha1.ServiceAvailabilityStatus
	}{
		LocationName: location.Name, LocationUID: string(location.UID), LocationGeneration: location.Generation,
		LocationSpec: location.Spec, LocationStatus: location.Status,
		AvailabilityName: availability.Name, AvailabilityUID: string(availability.UID), AvailabilityGeneration: availability.Generation,
		AvailabilitySpec: availability.Spec, AvailabilityStatus: availability.Status,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
