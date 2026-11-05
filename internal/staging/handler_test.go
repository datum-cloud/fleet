package staging

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/simulator"
)

type executor struct {
	results map[string]simulator.OperationResult
}

func (e *executor) Execute(_ context.Context, request simulator.Request) (simulator.OperationResult, error) {
	now := time.Now().UTC()
	result := simulator.OperationResult{ID: request.ID, Kind: request.Kind, Phase: "Succeeded", Result: "clean", CreatedAt: now, CompletedAt: &now}
	e.results[request.ID] = result
	return result, nil
}

func (e *executor) Lookup(_ context.Context, id string) (simulator.OperationResult, bool, error) {
	result, found := e.results[id]
	return result, found, nil
}

func TestOneMachineAllocatesExclusivelyThenReusesAfterRelease(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid"}}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose", UID: "class-uid"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{Machines: fleetv1.MachineSelector{Namespace: "default", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}}}}}
	cores := int64(24)
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose-24c-128g", UID: "profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: fleetv1.Constraint{Min: &cores}}, Memory: fleetv1.MemoryRequirement{Capacity: fleetv1.QuantityConstraint{Min: "128Gi"}}}}
	machine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "central-metal-01", UID: "machine-uid", Labels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}, Spec: fleetv1.MachineSpec{ProviderRef: fleetv1.TypedLocalReference{APIGroup: "synthetic.fleet.datumapis.com", Kind: providerKind, Name: "central-metal-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-reuse"}, ReclaimPolicy: "Retain"}}
	claim := func(name, uid string) *fleetv1.MachineClaim {
		return &fleetv1.MachineClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID(uid)}, Spec: fleetv1.MachineClaimSpec{MachineClassRef: fleetv1.LocalReference{Name: class.Name}, MachineProfileRef: fleetv1.LocalReference{Name: profile.Name}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"}}
	}
	first, second := claim("first", "first-uid"), claim("second", "second-uid")
	public := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.Machine{}, &fleetv1.MachineClaim{}).WithObjects(namespace, class, profile, machine, first, second).Build()
	private := fake.NewClientBuilder().WithScheme(scheme).Build()
	provider := &executor{results: map[string]simulator.OperationResult{}}
	coordinator, err := controller.NewCoordinator(private, "fleet-system-private", provider, controller.LocalDigestImageResolver{}, nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	project := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "project-uid"}
	handler := Handler{Public: public, Coordinator: coordinator, Project: project, Location: "us-central-1", Now: time.Now}
	reconcile := func(object *fleetv1.MachineClaim) manager.Outcome {
		return handler.ReconcileEvent(ctx, manager.ProjectRequest{AuthorityID: project.AuthorityID, ProjectUID: project.UID, Namespace: object.Namespace, Name: object.Name, UID: object.UID})
	}

	for i := 0; i < 2; i++ {
		if out := reconcile(first); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
	assertPhase(t, ctx, public, client.ObjectKeyFromObject(first), "Ready")
	for i := 0; i < 2; i++ {
		if out := reconcile(second); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
	assertPhase(t, ctx, public, client.ObjectKeyFromObject(second), "Pending")

	var deleting fleetv1.MachineClaim
	if err := public.Get(ctx, client.ObjectKeyFromObject(first), &deleting); err != nil {
		t.Fatal(err)
	}
	if err := public.Delete(ctx, &deleting); err != nil {
		t.Fatal(err)
	}
	if err := public.Get(ctx, client.ObjectKeyFromObject(first), &deleting); err != nil {
		t.Fatal(err)
	}
	if out := reconcile(&deleting); out.RetryError != nil {
		t.Fatal(out.RetryError)
	}
	if out := reconcile(second); out.RetryError != nil {
		t.Fatal(out.RetryError)
	}
	assertPhase(t, ctx, public, client.ObjectKeyFromObject(second), "Ready")
}

func assertPhase(t *testing.T, ctx context.Context, kube client.Client, key client.ObjectKey, phase string) {
	t.Helper()
	var claim fleetv1.MachineClaim
	if err := kube.Get(ctx, key, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Status.Phase != phase {
		t.Fatalf("phase = %q, want %q", claim.Status.Phase, phase)
	}
}
