package staging

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// TestLegacyCustodyIsReportedThenReleasedOnDeletion reproduces an upgrade over
// synthetic staging: a Machine allocated before CPU and memory became exact,
// whose profile was then deleted and recreated with a new UID. The Machine is
// reported as not ready without a retry loop, and deleting it performs the
// normal verified release so the capacity returns and the finalizer clears.
func TestLegacyCustodyIsReportedThenReleasedOnDeletion(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid"}}
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose", UID: "class-uid"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: "default"}}}}
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "general-purpose-24c-128g", UID: "original-profile-uid", Generation: 1}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 24}, Memory: fleetv1.MemoryRequirement{Capacity: "128Gi"}}}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "central-metal-01", UID: "capacity-uid"}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "central-metal-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-reuse"}, ReclaimPolicy: "Retain"}}
	machine := func(name, uid string) *fleetv1.Machine {
		return &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID(uid), Generation: 1}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: class.Name}, MachineProfileRef: fleetv1.LocalReference{Name: profile.Name}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"}}
	}
	runner := machine("legacy-machine", "legacy-machine-uid")
	public := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.MachineCapacity{}, &fleetv1.Machine{}).WithObjects(namespace, class, profile, capacity, runner).Build()
	private := fake.NewClientBuilder().WithScheme(scheme).Build()
	provider := &executor{results: map[string]simulator.OperationResult{}}
	coordinator, err := controller.NewCoordinator(private, "fleet-system-private", provider, controller.LocalDigestImageResolver{}, nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	project := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: "project-uid"}
	handler := Handler{Public: public, Coordinator: coordinator, Project: project, Location: "us-central-1", Now: time.Now}
	request := func(object *fleetv1.Machine) manager.ProjectRequest {
		return manager.ProjectRequest{AuthorityID: project.AuthorityID, ProjectUID: project.UID, Namespace: object.Namespace, Name: object.Name, UID: object.UID}
	}

	// Before the upgrade: allocate, then rewrite the retained records into the
	// pre-exact shape and recreate the profile under a new UID.
	for i := 0; i < 2; i++ {
		if out := handler.ReconcileEvent(ctx, request(runner)); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
	assertPhase(t, ctx, public, client.ObjectKeyFromObject(runner), "Ready")
	legacyizeStagingRecords(t, ctx, private, runner.UID)
	if err := public.Delete(ctx, &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: profile.Name}}); err != nil {
		t.Fatal(err)
	}
	recreated := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: profile.Name, UID: "recreated-profile-uid", Generation: 1}, Spec: profile.Spec}
	if err := public.Create(ctx, recreated); err != nil {
		t.Fatal(err)
	}

	// After the upgrade: every event settles without a retry error and the
	// Machine reports the legacy custody explicitly instead of readiness.
	owner := manager.NewLifecycleOwner(handler, manager.RequestedHardwareProjector{Client: public, Project: project}, nil)
	for i := 0; i < 3; i++ {
		if out := owner.ReconcileEvent(ctx, request(runner)); out.RetryError != nil {
			t.Fatalf("legacy custody produced a retry loop: %v", out.RetryError)
		}
	}
	var current fleetv1.Machine
	if err := public.Get(ctx, client.ObjectKeyFromObject(runner), &current); err != nil {
		t.Fatal(err)
	}
	for _, conditionType := range []string{"Bound", "Ready"} {
		condition := apiMeta.FindStatusCondition(current.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != controller.ReasonLegacyCustodyRecord {
			t.Fatalf("%s condition = %#v", conditionType, condition)
		}
	}
	if current.Status.Phase != "Pending" || current.Status.AllocatedHardware != nil {
		t.Fatalf("legacy custody status = %#v", current.Status)
	}
	assertAvailable(t, ctx, public, capacity, metav1.ConditionFalse)

	// Deleting the Machine releases through verified cleanup.
	if err := public.Delete(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if out := owner.ReconcileEvent(ctx, request(runner)); out.RetryError != nil {
		t.Fatalf("release with legacy custody: %v", out.RetryError)
	}
	if err := public.Get(ctx, client.ObjectKeyFromObject(runner), &current); !apierrors.IsNotFound(err) {
		t.Fatalf("finalizer was not removed: %v, %#v", err, current.Finalizers)
	}
	cleaned := false
	for _, result := range provider.results {
		cleaned = cleaned || result.Kind == "cleanup"
	}
	if !cleaned {
		t.Fatal("release did not run verified cleanup")
	}
	assertAvailable(t, ctx, public, capacity, metav1.ConditionTrue)
	if state := custodyState(t, ctx, private, runner.UID); state != string(controller.CustodyReleased) {
		t.Fatalf("custody state = %q", state)
	}

	// The returned capacity serves a new exact Machine.
	next := machine("next-runner", "next-runner-uid")
	if err := public.Create(ctx, next); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if out := owner.ReconcileEvent(ctx, request(next)); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
	assertPhase(t, ctx, public, client.ObjectKeyFromObject(next), "Ready")
}

func assertAvailable(t *testing.T, ctx context.Context, kube client.Client, capacity *fleetv1.MachineCapacity, want metav1.ConditionStatus) {
	t.Helper()
	var current fleetv1.MachineCapacity
	if err := kube.Get(ctx, client.ObjectKeyFromObject(capacity), &current); err != nil {
		t.Fatal(err)
	}
	if condition := apiMeta.FindStatusCondition(current.Status.Conditions, "Available"); condition == nil || condition.Status != want {
		t.Fatalf("Available = %#v, want %s", condition, want)
	}
}

func custodyState(t *testing.T, ctx context.Context, private client.Client, claimUID types.UID) string {
	t.Helper()
	var list corev1.SecretList
	if err := private.List(ctx, &list, client.MatchingLabels{"fleet.datumapis.com/private-record": "custody", "fleet.datumapis.com/claim-uid": string(claimUID)}); err != nil || len(list.Items) != 1 {
		t.Fatalf("custody records = %d, %v", len(list.Items), err)
	}
	var record struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(list.Items[0].Data["record.json"], &record); err != nil {
		t.Fatal(err)
	}
	return record.State
}

// legacyizeStagingRecords rewrites the claim's retained custody proposal and
// asset record into the pre-exact requirements shape with deleted profile
// lifetimes. It uses synthetic data only.
func legacyizeStagingRecords(t *testing.T, ctx context.Context, private client.Client, claimUID types.UID) {
	t.Helper()
	var list corev1.SecretList
	if err := private.List(ctx, &list, client.MatchingLabels{"fleet.datumapis.com/claim-uid": string(claimUID)}); err != nil {
		t.Fatal(err)
	}
	legacyize := func(record map[string]any) {
		requirements := record["acceptedRequirements"].(map[string]any)
		cpu := requirements["cpu"].(map[string]any)
		memory := requirements["memory"].(map[string]any)
		cpu["cores"] = map[string]any{"min": cpu["cores"]}
		memory["capacity"] = map[string]any{"min": memory["capacity"]}
		record["profileUID"], record["localProfileUID"] = "original-profile-uid-deleted", "original-profile-uid-deleted"
	}
	rewritten := 0
	for i := range list.Items {
		secret := &list.Items[i]
		var record map[string]any
		if err := json.Unmarshal(secret.Data["record.json"], &record); err != nil {
			t.Fatal(err)
		}
		switch secret.Labels["fleet.datumapis.com/private-record"] {
		case "asset":
			legacyize(record)
		case "custody":
			proposal, ok := record["proposedAsset"].(map[string]any)
			if !ok {
				t.Fatal("bound custody has no retained proposal")
			}
			legacyize(proposal)
		default:
			continue
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		secret.Data["record.json"] = data
		if err := private.Update(ctx, secret); err != nil {
			t.Fatal(err)
		}
		rewritten++
	}
	if rewritten != 2 {
		t.Fatalf("rewrote %d records, want custody and asset", rewritten)
	}
}
