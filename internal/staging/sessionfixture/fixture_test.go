package sessionfixture

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/simulator"
)

func newFixture(t *testing.T) (Fixture, *controller.LifecycleSessionAuthority, *fleetv1.Machine, *fleetv1.MachineCapacity, *simulator.Store) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, fleetv1.AddToScheme, sessionv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "consumer", Name: "db-01", UID: "claim-1", Generation: 1}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "synthetic"}, MachineProfileRef: fleetv1.LocalReference{Name: "synthetic"}, LocationRef: fleetv1.LocalReference{Name: "location"}, PowerState: "Off"}}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: "provider", Name: "capacity", UID: "capacity-1", Generation: 1}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "device"}, AllocationPolicy: "Enabled"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&sessionv1.MachineManagementSession{}, &sessionv1.MachineManagementSessionBinding{}).WithObjects(claim, capacity, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "consumer", UID: "consumer-1"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "provider", UID: "provider-1"}}).Build()
	store, err := simulator.Open(filepath.Join(t.TempDir(), "executor.json"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := controller.NewCoordinator(kube, "private", StoreExecutor{Store: store}, controller.LocalDigestImageResolver{}, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthority(kube, "private")
	if err != nil {
		t.Fatal(err)
	}
	return Fixture{Client: kube, Coordinator: coordinator, PrivateNamespace: "private"}, authority, claim, capacity, store
}

func allocate(t *testing.T, fixture Fixture, claim *fleetv1.Machine, capacity *fleetv1.MachineCapacity) *controller.AssetRecord {
	t.Helper()
	asset, err := fixture.Allocate(context.Background(), client.ObjectKeyFromObject(claim), client.ObjectKeyFromObject(capacity), "alice", "cell")
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func proposed(claim *fleetv1.Machine) sessionv1.MachineManagementSessionBindingSpec {
	return sessionv1.MachineManagementSessionBindingSpec{MachineNamespace: claim.Namespace, MachineName: claim.Name, MachineUID: claim.UID, RequestingPrincipal: "alice", Protocol: "Redfish", AccessProfile: "Provisioning", SessionNamespace: claim.Namespace, SessionName: "access", SessionUID: "session-1", ClientPublicKey: "key", ConnectBefore: metav1.NewTime(time.Now().Add(time.Minute)), ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)), SessionCorrelationID: "session-correlation", OperationCorrelationID: "operation-correlation"}
}

func TestFixtureWiresSessionReconcilerToRealCustody(t *testing.T) {
	fixture, authority, claim, capacity, _ := newFixture(t)
	asset := allocate(t, fixture, claim, capacity)
	session := &sessionv1.MachineManagementSession{ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: claim.Namespace, UID: "session-1", Annotations: map[string]string{controller.PrincipalAnnotation: "alice"}}, Spec: sessionv1.MachineManagementSessionSpec{MachineRef: sessionv1.LocalReference{Name: claim.Name}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "key", TTL: metav1.Duration{Duration: time.Minute}}}
	ctx := context.Background()
	if err := fixture.Client.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	reconciler := controller.SessionReconciler{Client: fixture.Client, Lifecycle: authority}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}); err != nil {
		t.Fatal(err)
	}
	var bindings sessionv1.MachineManagementSessionBindingList
	if err := fixture.Client.List(ctx, &bindings); err != nil || len(bindings.Items) != 1 {
		t.Fatalf("missing binding: %+v %v", bindings, err)
	}
	binding := &bindings.Items[0]
	if binding.Spec.AllocationUID != types.UID(asset.BindingID) || binding.Spec.DeviceName != capacity.Spec.DeviceRef.Name || !authority.Active(ctx, binding) {
		t.Fatalf("binding did not retain real custody: %+v", binding.Spec)
	}
}

func TestReleaseBlocksUntilVerifiedTeardownThenReusesNextEpoch(t *testing.T) {
	fixture, authority, claim, capacity, store := newFixture(t)
	asset := allocate(t, fixture, claim, capacity)
	ctx := context.Background()
	retained, err := authority.Acquire(ctx, proposed(claim))
	if err != nil {
		t.Fatal(err)
	}
	binding := &sessionv1.MachineManagementSessionBinding{ObjectMeta: metav1.ObjectMeta{Generation: 1}, Spec: retained}
	if err := authority.BeginProjection(ctx, binding); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := fixture.Release(ctx, client.ObjectKeyFromObject(claim), claim.UID)
		if !errors.Is(err, controller.ErrManagementActive) || result.SafeToRemoveFinalizer || authority.Active(ctx, binding) {
			t.Fatalf("release crossed management barrier: %+v %v", result, err)
		}
	}
	if _, found := store.Get("cleanup-" + asset.BindingID); found {
		t.Fatal("cleanup executed before verified session teardown")
	}
	other := claim.DeepCopyObject().(*fleetv1.Machine)
	other.Name, other.UID, other.ResourceVersion = "db-02", "claim-2", ""
	if err := fixture.Client.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Allocate(ctx, client.ObjectKeyFromObject(other), client.ObjectKeyFromObject(capacity), "alice", "cell"); !errors.Is(err, controller.ErrInsufficientCapacity) {
		t.Fatalf("reuse before teardown: %v", err)
	}
	binding.Annotations = map[string]string{controller.RevokedAtAnnotation: "revoked"}
	binding.Status.Conditions = []metav1.Condition{{Type: "CleanupConfirmed", Status: metav1.ConditionUnknown, ObservedGeneration: 1}}
	if err := authority.Complete(ctx, binding); !errors.Is(err, controller.ErrManagementActive) {
		t.Fatalf("unknown teardown cleared custody: %v", err)
	}
	binding.Status.Conditions[0].Status = metav1.ConditionTrue
	if err := authority.Complete(ctx, binding); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.Release(ctx, client.ObjectKeyFromObject(claim), claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("verified release: %+v %v", result, err)
	}
	next := allocate(t, fixture, other, capacity)
	if next.Epoch != asset.Epoch+1 || next.BindingID == asset.BindingID {
		t.Fatalf("reuse did not advance allocation: %+v", next)
	}
}

func TestSyntheticGrantCannotAuthorizeMissingCustodyOrChangedLifetimes(t *testing.T) {
	for _, change := range []string{"principal", "machine", "capacity", "namespace", "provider", "device", "grant-only"} {
		t.Run(change, func(t *testing.T) {
			fixture, authority, claim, capacity, _ := newFixture(t)
			allocate(t, fixture, claim, capacity)
			binding := proposed(claim)
			ctx := context.Background()
			switch change {
			case "principal":
				binding.RequestingPrincipal = "mallory"
			case "machine", "capacity", "namespace", "provider", "device":
				var object client.Object = claim.DeepCopyObject().(*fleetv1.Machine)
				if change == "capacity" || change == "device" {
					object = capacity.DeepCopyObject().(*fleetv1.MachineCapacity)
				} else if change == "namespace" {
					object = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "consumer"}}
				} else if change == "provider" {
					object = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "provider"}}
				}
				if err := fixture.Client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatal(err)
				}
				if change == "device" {
					object.(*fleetv1.MachineCapacity).Spec.DeviceRef.Name = "different"
				} else {
					object.SetUID("replacement")
				}
				if err := fixture.Client.Update(ctx, object); err != nil {
					t.Fatal(err)
				}
			case "grant-only":
				var secrets corev1.SecretList
				if err := fixture.Client.List(ctx, &secrets); err != nil {
					t.Fatal(err)
				}
				for _, secret := range secrets.Items {
					if secret.Name != grantName(claim.UID) {
						if err := fixture.Client.Delete(ctx, &secret); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if _, err := authority.Acquire(ctx, binding); err == nil {
				t.Fatal("changed authority or missing custody was accepted")
			}
		})
	}
}
