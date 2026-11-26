package milo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/tenancy"
)

var _ allocation.Quota = Quota{}

// Quota reserves one Machine's quota as a Milo ResourceClaim in the Machine's
// namespace. The claim is named from the Machine UID within its consumer
// project, as its allocation is, so a retried create collides. Only a Milo
// Project consumer has quota.
type Quota struct {
	// Consumers resolves the Machine's consumer project and its API.
	Consumers tenancy.Resolver
}

// ClaimName is the only name a Machine's quota claim may have.
func ClaimName(machine allocation.Machine) string {
	sum := sha256.Sum256([]byte(string(machine.ConsumerUID) + "/" + string(machine.UID)))
	return "fleet-machine-" + hex.EncodeToString(sum[:])[:16]
}

func desired(machine allocation.Machine) *quotav1alpha1.ResourceClaim {
	return &quotav1alpha1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: ClaimName(machine), Namespace: machine.Namespace, Labels: map[string]string{
			"fleet.datumapis.com/quota-claim": "machine", fleetv1.LabelMachineUID: string(machine.UID), fleetv1.LabelConsumerUID: string(machine.ConsumerUID),
		}},
		Spec: quotav1alpha1.ResourceClaimSpec{
			ConsumerRef: quotav1alpha1.ConsumerRef{APIGroup: machine.Consumer.APIGroup, Kind: machine.Consumer.Kind, Name: machine.Consumer.Name},
			Requests:    []quotav1alpha1.ResourceRequest{{ResourceType: MachineQuotaResource, Amount: 1}},
			ResourceRef: &quotav1alpha1.UnversionedObjectReference{APIGroup: fleetv1.GroupVersion.Group, Kind: "Machine", Namespace: machine.Namespace, Name: machine.Name},
		},
	}
}

func matches(machine allocation.Machine, claim *quotav1alpha1.ResourceClaim) bool {
	want := desired(machine)
	return claim.UID != "" && claim.Labels[fleetv1.LabelMachineUID] == want.Labels[fleetv1.LabelMachineUID] &&
		claim.Labels[fleetv1.LabelConsumerUID] == want.Labels[fleetv1.LabelConsumerUID] &&
		claim.Spec.ConsumerRef == want.Spec.ConsumerRef && claim.Spec.ResourceRef != nil && *claim.Spec.ResourceRef == *want.Spec.ResourceRef &&
		len(claim.Spec.Requests) == 1 && claim.Spec.Requests[0] == want.Spec.Requests[0]
}

// Acquire creates the claim once and returns its name when Milo granted it.
func (q Quota) Acquire(ctx context.Context, machine allocation.Machine) (string, error) {
	api, err := q.client(ctx, machine)
	if err != nil {
		return "", err
	}
	claim := &quotav1alpha1.ResourceClaim{}
	if err := api.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: ClaimName(machine)}, claim); apierrors.IsNotFound(err) {
		if err := api.Create(ctx, desired(machine)); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("%w: create ResourceClaim: %v", ErrQuotaUnknown, err)
		}
		return "", ErrQuotaPending
	} else if err != nil {
		return "", fmt.Errorf("%w: read ResourceClaim: %v", ErrQuotaUnknown, err)
	}
	if !matches(machine, claim) {
		return "", ErrQuotaInvalid
	}
	if claim.DeletionTimestamp != nil {
		return "", ErrQuotaUnknown
	}
	granted := apiMeta.FindStatusCondition(claim.Status.Conditions, quotav1alpha1.ResourceClaimGranted)
	switch {
	case granted == nil || claim.Status.ObservedGeneration != claim.Generation:
		return "", ErrQuotaPending
	case granted.Status == metav1.ConditionUnknown:
		return "", ErrQuotaUnknown
	case granted.Status != metav1.ConditionTrue:
		return "", ErrQuotaPending
	}
	return claim.Name, nil
}

// Release deletes the Machine's claim. It returns nil once the claim is
// gone.
func (q Quota) Release(ctx context.Context, machine allocation.Machine) error {
	name := ClaimName(machine)
	api, err := q.client(ctx, machine)
	// A consumer that is gone, or was recreated, took its claims with it.
	if errors.Is(err, tenancy.ErrConsumerGone) {
		return nil
	} else if err != nil {
		return err
	}
	claim := &quotav1alpha1.ResourceClaim{}
	if err := api.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: name}, claim); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: read ResourceClaim: %v", ErrQuotaUnknown, err)
	}
	if !matches(machine, claim) {
		return ErrQuotaInvalid
	}
	uid := claim.UID
	if err := api.Delete(ctx, claim, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: delete ResourceClaim: %v", ErrQuotaUnknown, err)
	}
	// Deletion is observed, not assumed: the next call sees the claim gone.
	return fmt.Errorf("%w: the claim is being deleted", ErrQuotaPending)
}

// client resolves the Machine's consumer incarnation, which must be a Milo
// Project.
func (q Quota) client(ctx context.Context, machine allocation.Machine) (client.Client, error) {
	if machine.Consumer.APIGroup != tenancy.ProjectGroup || machine.Consumer.Kind != tenancy.ProjectKind {
		return nil, fmt.Errorf("%w: Milo quota serves Project consumers only", tenancy.ErrUnsupportedConsumer)
	}
	consumer, err := tenancy.Incarnation(ctx, q.Consumers, machine.Consumer, machine.ConsumerUID)
	if err != nil {
		return nil, err
	}
	return consumer.Client, nil
}
