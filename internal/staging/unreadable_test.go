package staging

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
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

// An asset record in the older CPU and memory shape is reported on its own
// Machine only. Other Machines still allocate and release, and the affected
// Machine keeps its finalizer until an operator removes the record.
func TestUnreadablePrivateRecordIsReportedOnlyOnItsMachine(t *testing.T) {
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
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose-24c-128g", UID: "profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 24}, Memory: fleetv1.MemoryRequirement{Capacity: "128Gi"}}}
	capacity := func(name string) *fleetv1.MachineCapacity {
		return &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{"fleet.datumapis.com/capacity-pool": "central"}}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: name}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-reuse"}, ReclaimPolicy: "Retain"}}
	}
	claim := func(name string) *fleetv1.Machine {
		return &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID(name + "-uid")}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: class.Name}, MachineProfileRef: fleetv1.LocalReference{Name: profile.Name}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"}}
	}
	stale, other := claim("stale"), claim("other")
	public := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineCapacity{}, &fleetv1.Machine{}).WithObjects(namespace, class, profile, capacity("central-metal-01"), capacity("central-metal-02"), stale, other).Build()
	private := fake.NewClientBuilder().WithScheme(scheme).Build()
	coordinator, err := controller.NewCoordinator(private, "fleet-system-private", &executor{results: map[string]simulator.OperationResult{}}, controller.LocalDigestImageResolver{}, nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	project := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "project-uid"}
	handler := Handler{Public: public, Coordinator: coordinator, Project: project, Location: "us-central-1", Now: time.Now}
	reconcile := func(key client.ObjectKey) manager.Outcome {
		var current fleetv1.Machine
		if err := public.Get(ctx, key, &current); err != nil {
			t.Fatal(err)
		}
		return handler.ReconcileEvent(ctx, manager.ProjectRequest{AuthorityID: project.AuthorityID, ProjectUID: project.UID, Namespace: current.Namespace, Name: current.Name, UID: current.UID})
	}
	staleKey, otherKey := client.ObjectKeyFromObject(stale), client.ObjectKeyFromObject(other)

	for i := 0; i < 2; i++ {
		if out := reconcile(staleKey); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
	assertPhase(t, ctx, public, staleKey, "Ready")
	writeOlderAssetShape(t, ctx, private, stale.UID)

	assertUnreadable := func() {
		t.Helper()
		if out := reconcile(staleKey); out.RetryError != nil {
			t.Fatalf("unreadable record was requeued: %v", out.RetryError)
		}
		var current fleetv1.Machine
		if err := public.Get(ctx, staleKey, &current); err != nil {
			t.Fatal(err)
		}
		ready := apiMeta.FindStatusCondition(current.Status.Conditions, "Ready")
		if current.Status.Phase != "Pending" || ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != controller.ReasonUnreadablePrivateRecord {
			t.Fatalf("stale status = %#v", current.Status)
		}
	}
	assertUnreadable()

	for i := 0; i < 2; i++ {
		if out := reconcile(otherKey); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
	assertPhase(t, ctx, public, otherKey, "Ready")

	for _, key := range []client.ObjectKey{staleKey, otherKey} {
		var current fleetv1.Machine
		if err := public.Get(ctx, key, &current); err != nil {
			t.Fatal(err)
		}
		if err := public.Delete(ctx, &current); err != nil {
			t.Fatal(err)
		}
	}
	if out := reconcile(otherKey); out.RetryError != nil {
		t.Fatal(out.RetryError)
	}
	var released fleetv1.Machine
	if err := public.Get(ctx, otherKey, &released); err == nil {
		t.Fatalf("other Machine kept its finalizer: %#v", released.Finalizers)
	}
	assertUnreadable()
	var kept fleetv1.Machine
	if err := public.Get(ctx, staleKey, &kept); err != nil || !contains(kept.Finalizers, controller.MachineFinalizer) {
		t.Fatalf("stale Machine lost its finalizer: %#v, %v", kept.Finalizers, err)
	}
}

func writeOlderAssetShape(t *testing.T, ctx context.Context, private client.Client, claimUID types.UID) {
	t.Helper()
	var secrets corev1.SecretList
	if err := private.List(ctx, &secrets, client.MatchingLabels{"fleet.datumapis.com/private-record": "asset", "fleet.datumapis.com/claim-uid": string(claimUID)}); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("asset records for %s = %d, want 1", claimUID, len(secrets.Items))
	}
	secret := &secrets.Items[0]
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(secret.Data["record.json"], &fields); err != nil {
		t.Fatal(err)
	}
	fields["acceptedRequirements"] = json.RawMessage(`{"cpu":{"architecture":"amd64","cores":{"min":4}},"memory":{"capacity":{"min":"16Gi"}}}`)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["record.json"] = data
	if err := private.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
}
