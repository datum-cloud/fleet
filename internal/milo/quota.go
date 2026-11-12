package milo

import (
	"context"
	"fmt"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

type ResourceClaimManager struct{}

func QuotaIntentFor(project controller.ProjectIdentity, namespace, claimName string, claimUID types.UID) QuotaIntent {
	return QuotaIntent{ConsumerProject: project, Namespace: namespace, ClaimName: claimName, ClaimUID: claimUID, ResourceType: MachineQuotaResource, Amount: 1, Name: "fleet-machine-" + shortDigest(string(project.UID)+"/"+string(claimUID))}
}

func (ResourceClaimManager) Acquire(ctx context.Context, kube client.Client, intent QuotaIntent) (QuotaHold, bool, error) {
	if kube == nil || !intent.ConsumerProject.Valid() || intent.Namespace == "" || intent.ClaimName == "" || intent.ClaimUID == "" || intent.ResourceType != MachineQuotaResource || intent.Amount != 1 || intent.Name != QuotaIntentFor(intent.ConsumerProject, intent.Namespace, intent.ClaimName, intent.ClaimUID).Name {
		return QuotaHold{}, false, ErrAuthorityInvalid
	}
	key := client.ObjectKey{Namespace: intent.Namespace, Name: intent.Name}
	claim := &quotav1alpha1.ResourceClaim{}
	if err := kube.Get(ctx, key, claim); apierrors.IsNotFound(err) {
		claim = desiredResourceClaim(intent)
		if err := kube.Create(ctx, claim); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ResourceClaimManager{}.Acquire(ctx, kube, intent)
			}
			return QuotaHold{}, false, fmt.Errorf("%w: create ResourceClaim: %v", ErrQuotaUnknown, err)
		}
		return QuotaHold{}, false, ErrQuotaPending
	} else if err != nil {
		return QuotaHold{}, false, fmt.Errorf("%w: read ResourceClaim: %v", ErrQuotaUnknown, err)
	}
	if err := validateResourceClaim(intent, claim); err != nil {
		return QuotaHold{}, false, err
	}
	hold := QuotaHold{Intent: intent, UID: claim.UID, Generation: claim.Generation, ObservedGeneration: claim.Status.ObservedGeneration}
	granted := apiMeta.FindStatusCondition(claim.Status.Conditions, quotav1alpha1.ResourceClaimGranted)
	if claim.DeletionTimestamp != nil {
		return hold, false, ErrQuotaUnknown
	}
	if granted == nil || claim.Status.ObservedGeneration != claim.Generation {
		return hold, false, ErrQuotaPending
	}
	if granted.Status == metav1.ConditionUnknown {
		return hold, false, ErrQuotaUnknown
	}
	if granted.Status != metav1.ConditionTrue {
		return hold, false, ErrQuotaPending
	}
	return hold, true, nil
}

func desiredResourceClaim(intent QuotaIntent) *quotav1alpha1.ResourceClaim {
	return &quotav1alpha1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: intent.Name, Namespace: intent.Namespace, Labels: map[string]string{"fleet.datumapis.com/quota-claim": "machine", "fleet.datumapis.com/machine-uid": string(intent.ClaimUID), "fleet.datumapis.com/consumer-project-uid": string(intent.ConsumerProject.UID)}}, Spec: quotav1alpha1.ResourceClaimSpec{ConsumerRef: quotav1alpha1.ConsumerRef{APIGroup: "resourcemanager.miloapis.com", Kind: "Project", Name: intent.ConsumerProject.RouteName}, Requests: []quotav1alpha1.ResourceRequest{{ResourceType: intent.ResourceType, Amount: intent.Amount}}, ResourceRef: &quotav1alpha1.UnversionedObjectReference{APIGroup: fleetv1.GroupVersion.Group, Kind: "Machine", Namespace: intent.Namespace, Name: intent.ClaimName}}}
}

func validateResourceClaim(intent QuotaIntent, claim *quotav1alpha1.ResourceClaim) error {
	if claim == nil || claim.UID == "" || claim.Name != intent.Name || claim.Namespace != intent.Namespace || claim.Labels["fleet.datumapis.com/machine-uid"] != string(intent.ClaimUID) || claim.Labels["fleet.datumapis.com/consumer-project-uid"] != string(intent.ConsumerProject.UID) || claim.Spec.ConsumerRef.APIGroup != "resourcemanager.miloapis.com" || claim.Spec.ConsumerRef.Kind != "Project" || claim.Spec.ConsumerRef.Name != intent.ConsumerProject.RouteName || claim.Spec.ResourceRef == nil || claim.Spec.ResourceRef.APIGroup != fleetv1.GroupVersion.Group || claim.Spec.ResourceRef.Kind != "Machine" || claim.Spec.ResourceRef.Namespace != intent.Namespace || claim.Spec.ResourceRef.Name != intent.ClaimName || len(claim.Spec.Requests) != 1 || claim.Spec.Requests[0].ResourceType != intent.ResourceType || claim.Spec.Requests[0].Amount != intent.Amount {
		return ErrAuthorityInvalid
	}
	return nil
}
