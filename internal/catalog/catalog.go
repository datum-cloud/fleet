// Package catalog is the Metal image catalog: the provider publishes
// immutable MachineImages by digest, publication copies one into a consumer
// project with a protected record in the provider project, and admission
// resolves an Installation's image through that record to the exact digest.
//
// Resolution never reads the consumer copy's status, and never trusts an
// object only by name: the record's name derives from the consumer tenant,
// copy and provider image UIDs, and its spec pins the digest, so an object
// deleted and recreated under the same name finds no record. A tenant is a
// Milo Project, or a Namespace in standalone mode; any other kind is
// refused.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/tenancy"
)

// Refusal reasons an Installation reports as Accepted=False.
const (
	ReasonImageNotFound            = "ImageNotFound"
	ReasonImageNotEntitled         = "ImageNotEntitled"
	ReasonImageWithdrawn           = "ImageWithdrawn"
	ReasonImageIncompatible        = "ImageIncompatible"
	ReasonAuthorizationUnavailable = "AuthorizationUnavailable"
)

// Refusal is an admission refusal. Nothing was copied or created for it.
type Refusal struct {
	Reason  string
	Message string
}

func (r *Refusal) Error() string { return r.Reason + ": " + r.Message }

func refuse(reason, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// ErrAuthorizationUnavailable is what an Authorizer returns when it could
// not decide. Admission treats it as unknown, never as permission.
var ErrAuthorizationUnavailable = errors.New("authorization unavailable")

// ErrConflict means a publication already exists with other contents.
var ErrConflict = errors.New("catalog: the publication exists with other contents")

// Authorizer decides whether the principal that created the Installation
// may use both images: the consumer copy and its provider source. It
// returns nil to allow, ErrAuthorizationUnavailable when it cannot decide,
// and any other error to deny.
type Authorizer func(ctx context.Context, installation *fleetv1.Installation, consumer, provider *fleetv1.MachineImage) error

// Image is a resolved provider image and its definition.
type Image struct {
	Name       string
	Definition fleetv1.ImageDefinition
}

// Catalog reads the provider project's images and publication records.
type Catalog struct {
	// Provider reads the provider project. It must read through to the API
	// server.
	Provider client.Reader
	// Publisher is the provider's tenant. A consumer copy must name it.
	Publisher fleetv1.TypedReference
	// Namespace holds the provider's images and publication records.
	Namespace string
	Authorize Authorizer
}

// PublicationName is the only name a publication record may have. It
// derives from the consumer tenant incarnation, the consumer copy's
// namespace, name and UID, and the provider image's name and UID, so a
// record entitles only the incarnations it was written for.
func PublicationName(consumer tenancy.Consumer, local, source *fleetv1.MachineImage) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{consumer.Ref.APIGroup, consumer.Ref.Kind, consumer.Ref.Name, string(consumer.UID), local.Namespace, local.Name, string(local.UID), source.Name, string(source.UID)}, "\x00")))
	return "imgpub-" + hex.EncodeToString(sum[:])[:32]
}

// Resolve resolves an Installation's image to the provider incarnation and
// digest its publication record pins, and checks entitlement and use
// permission. It returns a *Refusal for every reason the request is refused.
func (c *Catalog) Resolve(ctx context.Context, consumer tenancy.Consumer, installation *fleetv1.Installation) (Image, error) {
	if consumer.Client == nil {
		return Image{}, errors.New("catalog: the consumer has no API client")
	}
	local := &fleetv1.MachineImage{}
	if err := consumer.Client.Get(ctx, client.ObjectKey{Namespace: installation.Namespace, Name: installation.Spec.ImageRef.Name}, local); apierrors.IsNotFound(err) {
		return Image{}, refuse(ReasonImageNotFound, "no MachineImage %q in this namespace", installation.Spec.ImageRef.Name)
	} else if err != nil {
		return Image{}, err
	}
	notEntitled := refuse(ReasonImageNotEntitled, "MachineImage %q is not published to this project", local.Name)
	if tenancy.Supported(consumer.Ref) != nil || tenancy.Supported(c.Publisher) != nil {
		return Image{}, refuse(ReasonImageNotEntitled, "a tenant must be a Milo Project or, in standalone mode, a Namespace")
	}
	if local.Spec.Source == nil || local.Spec.Source.PublisherRef != c.Publisher {
		return Image{}, notEntitled
	}
	source := &fleetv1.MachineImage{}
	if err := c.Provider.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: local.Spec.Source.Name}, source); apierrors.IsNotFound(err) {
		return Image{}, notEntitled
	} else if err != nil {
		return Image{}, err
	}
	if local.UID == "" || source.UID == "" || consumer.UID == "" {
		return Image{}, notEntitled
	}
	record := &fleetv1.MachineImagePublication{}
	if err := c.Provider.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: PublicationName(consumer, local, source)}, record); apierrors.IsNotFound(err) {
		return Image{}, notEntitled
	} else if err != nil {
		return Image{}, err
	}
	if record.Spec != publication(consumer, local, source) {
		return Image{}, notEntitled
	}
	if source.Spec.Availability != fleetv1.ImageAvailable {
		return Image{}, refuse(ReasonImageWithdrawn, "MachineImage %q is withdrawn", local.Name)
	}
	if c.Authorize == nil {
		return Image{}, refuse(ReasonAuthorizationUnavailable, "no image authorizer is configured")
	}
	if err := c.Authorize(ctx, installation, local, source); errors.Is(err, ErrAuthorizationUnavailable) {
		return Image{}, refuse(ReasonAuthorizationUnavailable, "the image-use check could not complete")
	} else if err != nil {
		return Image{}, refuse(ReasonImageNotEntitled, "the creator may not use MachineImage %q", local.Name)
	}
	return Image{Name: source.Name, Definition: *source.Spec.Definition}, nil
}

// publication is the record a copy and its provider image would have. It
// returns no spec a record can hold for a source without a definition.
func publication(consumer tenancy.Consumer, local, source *fleetv1.MachineImage) fleetv1.MachineImagePublicationSpec {
	spec := fleetv1.MachineImagePublicationSpec{
		ConsumerRef: consumer.Ref,
		Image:       fleetv1.PublishedImage{Namespace: local.Namespace, Name: local.Name},
		Source:      fleetv1.LocalReference{Name: source.Name},
	}
	if source.Spec.Definition != nil {
		spec.Digest = source.Spec.Definition.Digest
	}
	return spec
}

// Compatible checks an image against an allocation's hardware: the
// architecture must be equal, the server's firmware mode must be known and
// listed, and bootstrap data needs a bootstrap format.
func Compatible(definition fleetv1.ImageDefinition, hardware fleetv1.Hardware, bootstrap bool) error {
	if definition.Architecture != hardware.CPU.Architecture {
		return refuse(ReasonImageIncompatible, "the image is %s and the server is %s", definition.Architecture, hardware.CPU.Architecture)
	}
	if hardware.Firmware == nil || !slices.Contains(definition.FirmwareModes, hardware.Firmware.BootMode) {
		return refuse(ReasonImageIncompatible, "the server's firmware mode is unknown or not one the image boots in")
	}
	if bootstrap && len(definition.BootstrapFormats) == 0 {
		return refuse(ReasonImageIncompatible, "the image takes no bootstrap data")
	}
	return nil
}

// Publish publishes a provider image into a consumer project under the
// same name: the consumer copy first, then the record that entitles it.
// Until the record exists the copy entitles nothing, so a crash between
// the writes fails closed and a retry completes it. Publish is the
// publisher's action; it is idempotent and refuses to change an existing
// publication.
func (c *Catalog) Publish(ctx context.Context, provider client.Client, consumer tenancy.Consumer, namespace, name string) error {
	if err := tenancy.Supported(consumer.Ref); err != nil {
		return fmt.Errorf("catalog: consumer: %w", err)
	}
	if err := tenancy.Supported(c.Publisher); err != nil {
		return fmt.Errorf("catalog: publisher: %w", err)
	}
	if consumer.Client == nil {
		return errors.New("catalog: the consumer has no API client")
	}
	consumerAPI := consumer.Client
	source := &fleetv1.MachineImage{}
	if err := provider.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, source); err != nil {
		return err
	}
	if source.Spec.Definition == nil {
		return fmt.Errorf("catalog: %s is not a provider definition", name)
	}
	local := &fleetv1.MachineImage{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Spec: fleetv1.MachineImageSpec{Source: &fleetv1.SourceReference{PublisherRef: c.Publisher, Name: name}}}
	if err := consumerAPI.Create(ctx, local); apierrors.IsAlreadyExists(err) {
		if err := consumerAPI.Get(ctx, client.ObjectKeyFromObject(local), local); err != nil {
			return err
		}
		if local.Spec.Source == nil || *local.Spec.Source != (fleetv1.SourceReference{PublisherRef: c.Publisher, Name: name}) {
			return fmt.Errorf("%w: %s/%s names another source", ErrConflict, namespace, name)
		}
	} else if err != nil {
		return err
	}
	if local.UID == "" || source.UID == "" || consumer.UID == "" {
		return errors.New("catalog: a publication name binds object UIDs, and one is empty")
	}
	want := publication(consumer, local, source)
	record := &fleetv1.MachineImagePublication{ObjectMeta: metav1.ObjectMeta{Namespace: c.Namespace, Name: PublicationName(consumer, local, source)}, Spec: want}
	if err := provider.Create(ctx, record); apierrors.IsAlreadyExists(err) {
		if err := provider.Get(ctx, client.ObjectKeyFromObject(record), record); err != nil {
			return err
		}
		if record.Spec != want {
			return fmt.Errorf("%w: %s", ErrConflict, record.Name)
		}
	} else if err != nil {
		return err
	}
	definition := *source.Spec.Definition
	local.Status.Resolved = &definition
	status := metav1.ConditionTrue
	reason := "Published"
	if source.Spec.Availability != fleetv1.ImageAvailable {
		status, reason = metav1.ConditionFalse, "Withdrawn"
	}
	apiMeta.SetStatusCondition(&local.Status.Conditions, metav1.Condition{Type: "Available", Status: status, Reason: reason, ObservedGeneration: local.Generation})
	return consumerAPI.Status().Update(ctx, local)
}
