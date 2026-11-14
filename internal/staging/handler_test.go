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
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose", UID: "class-uid"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}}}}}
	cores := int64(24)
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose-24c-128g", UID: "profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: fleetv1.Constraint{Min: &cores}}, Memory: fleetv1.MemoryRequirement{Capacity: fleetv1.QuantityConstraint{Min: "128Gi"}}}}
	machine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "central-metal-01", UID: "machine-uid", Labels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "central-metal-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-reuse"}, ReclaimPolicy: "Retain"}}
	claim := func(name, uid string) *fleetv1.Machine {
		return &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID(uid)}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: class.Name}, MachineProfileRef: fleetv1.LocalReference{Name: profile.Name}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"}}
	}
	first, second := claim("first", "first-uid"), claim("second", "second-uid")
	public := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineCapacity{}, &fleetv1.Machine{}).WithObjects(namespace, class, profile, machine, first, second).Build()
	private := fake.NewClientBuilder().WithScheme(scheme).Build()
	provider := &executor{results: map[string]simulator.OperationResult{}}
	coordinator, err := controller.NewCoordinator(private, "fleet-system-private", provider, controller.LocalDigestImageResolver{}, nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	project := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "project-uid"}
	handler := Handler{Public: public, Coordinator: coordinator, Project: project, Location: "us-central-1", Now: time.Now}
	reconcile := func(object *fleetv1.Machine) manager.Outcome {
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

	var deleting fleetv1.Machine
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

func TestPublishedConsumerClaimAllocatesProviderMachine(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	consumerProject := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "project-zdhcz", UID: "consumer-project-uid"}
	providerProject := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "provider-project-uid"}
	cores := int64(24)
	sourceProfile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose-24c-128g", UID: "source-profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: fleetv1.Constraint{Min: &cores}}, Memory: fleetv1.MemoryRequirement{Capacity: fleetv1.QuantityConstraint{Min: "128Gi"}}}}
	sourceClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "central-metal", UID: "source-class-uid", Generation: 1}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: sourceProfile.Name}}}, Access: &fleetv1.AccessPolicy{}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}}}}}
	machine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "central-metal-01", UID: "machine-uid", Labels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "central-metal-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-reuse"}, ReclaimPolicy: "Retain"}}
	revision, err := publicationRevision(sourceClass, providerProject)
	if err != nil {
		t.Fatal(err)
	}
	localClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: sourceClass.Name, UID: "local-class-uid", Generation: 1, Annotations: map[string]string{publicationManagedByAnnotation: publicationManager, publicationProviderUIDAnnotation: string(providerProject.UID), publicationSourceUIDAnnotation: string(sourceClass.UID), publicationSourceNameAnnotation: sourceClass.Name}}, Spec: fleetv1.MachineClassSpec{Source: &fleetv1.SourceReference{Project: providerProject.RouteName, Name: sourceClass.Name}}, Status: fleetv1.MachineClassStatus{EvaluatedRevision: revision, ResolvedClass: &fleetv1.ResolvedClass{ProfilePolicy: *sourceClass.Spec.ProfilePolicy}, Conditions: []metav1.Condition{publicationCondition("SourceResolved", metav1.ConditionTrue, 1, "AuthorizedSource", "resolved")}}}
	localProfile := sourceProfile.DeepCopyObject().(*fleetv1.MachineProfile)
	localProfile.UID = "local-profile-uid"
	localProfile.Annotations = map[string]string{publicationManagedByAnnotation: publicationManager, publicationProviderUIDAnnotation: string(providerProject.UID), publicationSourceUIDAnnotation: string(sourceProfile.UID), publicationSourceNameAnnotation: sourceProfile.Name}
	localProfile.Status.Conditions = []metav1.Condition{publicationCondition("Published", metav1.ConditionTrue, localProfile.Generation, "SourceCopied", "copied")}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid"}}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cross-project", UID: "claim-uid"}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: localClass.Name}, MachineProfileRef: fleetv1.LocalReference{Name: localProfile.Name}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"}}
	consumer := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.Machine{}).WithObjects(namespace, localClass, localProfile, claim).Build()
	providerKube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineCapacity{}).WithObjects(sourceClass, sourceProfile, machine).Build()
	private := fake.NewClientBuilder().WithScheme(scheme).Build()
	providerExecutor := &executor{results: map[string]simulator.OperationResult{}}
	coordinator, err := controller.NewCoordinator(private, "fleet-system-private", providerExecutor, controller.LocalDigestImageResolver{}, nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{Public: consumer, Provider: providerKube, Coordinator: coordinator, Project: consumerProject, ProviderProject: providerProject, Location: "us-central-1", Now: time.Now}
	request := manager.ProjectRequest{AuthorityID: consumerProject.AuthorityID, ProjectUID: consumerProject.UID, Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}
	for i := 0; i < 2; i++ {
		if out := h.ReconcileEvent(ctx, request); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
	assertPhase(t, ctx, consumer, client.ObjectKeyFromObject(claim), "Ready")
	var allocated fleetv1.MachineCapacity
	if err := providerKube.Get(ctx, client.ObjectKeyFromObject(machine), &allocated); err != nil {
		t.Fatal(err)
	}
	available := false
	for _, condition := range allocated.Status.Conditions {
		if condition.Type == "Available" && condition.Status == metav1.ConditionFalse {
			available = true
		}
	}
	if !available {
		t.Fatal("provider Machine was not marked allocated")
	}
	var deleting fleetv1.Machine
	if err := consumer.Get(ctx, client.ObjectKeyFromObject(claim), &deleting); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Delete(ctx, &deleting); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Get(ctx, client.ObjectKeyFromObject(claim), &deleting); err != nil {
		t.Fatal(err)
	}
	if out := h.ReconcileEvent(ctx, request); out.RetryError != nil {
		t.Fatal(out.RetryError)
	}
	if err := providerKube.Get(ctx, client.ObjectKeyFromObject(machine), &allocated); err != nil {
		t.Fatal(err)
	}
	reusable := false
	for _, condition := range allocated.Status.Conditions {
		if condition.Type == "Available" && condition.Status == metav1.ConditionTrue {
			reusable = true
		}
	}
	if !reusable {
		t.Fatal("provider Machine did not become reusable after cross-project release")
	}
}

func assertPhase(t *testing.T, ctx context.Context, kube client.Client, key client.ObjectKey, phase string) {
	t.Helper()
	var claim fleetv1.Machine
	if err := kube.Get(ctx, key, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Status.Phase != phase {
		t.Fatalf("phase = %q, want %q", claim.Status.Phase, phase)
	}
}
