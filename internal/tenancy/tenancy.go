// Package tenancy resolves a Metal consumer from its typed reference. The
// allocator, release, quota, the Machine status projector, staging and the
// standalone mode reach a consumer only through a Resolver, so standalone
// and managed consumers share one allocation and installation contract.
package tenancy

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// The consumer kinds Fleet supports: a Milo Project, and a Namespace, which
// stands in for a project in the standalone mode.
const (
	ProjectGroup  = "resourcemanager.miloapis.com"
	ProjectKind   = "Project"
	NamespaceKind = "Namespace"
)

// ErrUnsupportedConsumer means a reference names a consumer kind Fleet does
// not serve.
var ErrUnsupportedConsumer = errors.New("tenancy: unsupported consumer kind")

// ErrConsumerUnavailable means the consumer cannot be reached now.
var ErrConsumerUnavailable = errors.New("tenancy: consumer unavailable")

// ErrConsumerGone means the consumer no longer exists.
var ErrConsumerGone = errors.New("tenancy: consumer gone")

// Incarnation resolves a reference and requires the incarnation it was
// recorded for. A consumer recreated under the same name is ErrConsumerGone
// for the earlier incarnation.
func Incarnation(ctx context.Context, resolver Resolver, ref fleetv1.TypedReference, uid types.UID) (Consumer, error) {
	consumer, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return Consumer{}, err
	}
	if consumer.UID != uid {
		return Consumer{}, fmt.Errorf("%w: %s %s was recreated", ErrConsumerGone, ref.Kind, ref.Name)
	}
	return consumer, nil
}

// Project is the reference to a Milo Project.
func Project(name string) fleetv1.TypedReference {
	return fleetv1.TypedReference{APIGroup: ProjectGroup, Kind: ProjectKind, Name: name}
}

// Namespace is the reference to a standalone consumer namespace.
func Namespace(name string) fleetv1.TypedReference {
	return fleetv1.TypedReference{Kind: NamespaceKind, Name: name}
}

// Supported refuses a reference to any kind Fleet does not serve, or one
// without a name.
func Supported(ref fleetv1.TypedReference) error {
	switch {
	case ref.Name == "":
		return fmt.Errorf("%w: a consumer reference needs a name", ErrUnsupportedConsumer)
	case ref.APIGroup == ProjectGroup && ref.Kind == ProjectKind, ref.APIGroup == "" && ref.Kind == NamespaceKind:
		return nil
	}
	return fmt.Errorf("%w: %s/%s", ErrUnsupportedConsumer, ref.APIGroup, ref.Kind)
}

// Consumer is one consumer's current incarnation and its API.
type Consumer struct {
	// Ref names the consumer. It is the only part recorded in a spec.
	Ref fleetv1.TypedReference
	// UID is the consumer's incarnation. It goes into names and labels,
	// never into a spec.
	UID types.UID
	// Client reaches the consumer's API, where its Machines live.
	Client client.Client
}

// Resolver resolves a consumer reference to the consumer's current
// incarnation and API. It returns ErrUnsupportedConsumer for a kind it does
// not serve and ErrConsumerUnavailable when it cannot reach the consumer.
type Resolver interface {
	Resolve(ctx context.Context, ref fleetv1.TypedReference) (Consumer, error)
}

// ResolverFunc adapts a function to a Resolver.
type ResolverFunc func(context.Context, fleetv1.TypedReference) (Consumer, error)

func (f ResolverFunc) Resolve(ctx context.Context, ref fleetv1.TypedReference) (Consumer, error) {
	return f(ctx, ref)
}

// Namespaces resolves standalone consumers: each Namespace in one cluster
// stands in for a project, and its API is that cluster's.
type Namespaces struct {
	Client client.Client
}

func (n Namespaces) Resolve(ctx context.Context, ref fleetv1.TypedReference) (Consumer, error) {
	if err := Supported(ref); err != nil {
		return Consumer{}, err
	}
	if ref.Kind != NamespaceKind {
		return Consumer{}, fmt.Errorf("%w: the standalone mode serves Namespaces only", ErrUnsupportedConsumer)
	}
	namespace := &corev1.Namespace{}
	if err := n.Client.Get(ctx, client.ObjectKey{Name: ref.Name}, namespace); apierrors.IsNotFound(err) {
		return Consumer{}, fmt.Errorf("%w: %v", ErrConsumerGone, err)
	} else if err != nil {
		return Consumer{}, fmt.Errorf("%w: %v", ErrConsumerUnavailable, err)
	}
	return Consumer{Ref: ref, UID: namespace.UID, Client: n.Client}, nil
}

// Fixed resolves only the consumers it lists, each to its one incarnation.
// Any other reference is ErrConsumerGone.
func Fixed(consumers ...Consumer) Resolver {
	return ResolverFunc(func(_ context.Context, ref fleetv1.TypedReference) (Consumer, error) {
		if err := Supported(ref); err != nil {
			return Consumer{}, err
		}
		for _, consumer := range consumers {
			if consumer.Ref == ref {
				return consumer, nil
			}
		}
		return Consumer{}, fmt.Errorf("%w: %s %s", ErrConsumerGone, ref.Kind, ref.Name)
	})
}
