package milo

import (
	"context"
	"errors"
	"testing"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.datum.net/fleet/internal/controller"
)

func quotaScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := quotav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestResourceClaimExactnessAndLostCreateRecovery(t *testing.T) {
	ctx := context.Background()
	project := controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "project-uid"}
	intent := QuotaIntentFor(project, "default", "allocation", "claim-uid")
	kube := fake.NewClientBuilder().WithScheme(quotaScheme(t)).Build()
	manager := ResourceClaimManager{}
	if _, granted, err := manager.Acquire(ctx, kube, intent); !errors.Is(err, ErrQuotaPending) || granted {
		t.Fatalf("initial acquire = %v, %v", granted, err)
	}
	var created quotav1alpha1.ResourceClaim
	if err := kube.Get(ctx, client.ObjectKey{Namespace: intent.Namespace, Name: intent.Name}, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.OwnerReferences) != 0 {
		t.Fatalf("quota claim has owner references: %#v", created.OwnerReferences)
	}
	created.UID, created.Generation = "quota-uid", 2
	created.Status.ObservedGeneration = 2
	created.Status.Conditions = []metav1.Condition{{Type: quotav1alpha1.ResourceClaimGranted, Status: metav1.ConditionTrue}}
	if err := kube.Update(ctx, &created); err != nil {
		t.Fatal(err)
	}
	hold, granted, err := manager.Acquire(ctx, kube, intent)
	if err != nil || !granted || hold.UID != "quota-uid" {
		t.Fatalf("recovered hold = %#v, %v, %v", hold, granted, err)
	}
	created.Spec.Requests[0].Amount = 2
	if err := kube.Update(ctx, &created); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Acquire(ctx, kube, intent); !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("forged claim error = %v", err)
	}
}

func TestResourceClaimMissingIsPendingNotSettlement(t *testing.T) {
	project := controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "project-uid"}
	intent := QuotaIntentFor(project, "default", "allocation", "claim-uid")
	kube := fake.NewClientBuilder().WithScheme(quotaScheme(t)).Build()
	_, _, err := (ResourceClaimManager{}).Acquire(context.Background(), kube, intent)
	if !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("missing ResourceClaim = %v, want pending", err)
	}
	// ResourceClaimManager deliberately has no Settle method. Only an injected
	// QuotaCustodian can produce the acknowledgment consumed by release.
}
