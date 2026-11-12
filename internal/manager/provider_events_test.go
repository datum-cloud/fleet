package manager

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

func TestProviderMachineEventWakesEveryConsumerClaimWithoutPolling(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	first := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "first", UID: "first-uid"}}
	second := &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "second", UID: "second-uid"}}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(first, second).Build()
	consumer := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "project-zdhcz", UID: "consumer-uid"}
	provider := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "provider-uid"}
	seen := map[types.UID]bool{}
	verifier := ProjectVerifierFunc(func(_ context.Context, _ controller.ProjectIdentity) error { return nil })
	reconciler := ProviderEventReconciler{Consumer: consumer, Provider: provider, ConsumerVerifier: verifier, ProviderVerifier: verifier, Reader: reader, Handler: HandlerFunc(func(_ context.Context, request ProjectRequest) Outcome {
		seen[request.UID] = true
		return Outcome{}
	})}
	request := ProviderEventRequest{AuthorityID: provider.AuthorityID, ProjectUID: provider.UID, Namespace: "default", Name: "machine", UID: "machine-uid"}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !seen[first.UID] || !seen[second.UID] || len(seen) != 2 {
		t.Fatalf("woken claims = %#v", seen)
	}
}
