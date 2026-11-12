package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := fleetv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := sessionv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdmissionPinsMachineAllocationAndCapacity(t *testing.T) {
	scheme := testScheme(t)
	now := time.Unix(1000, 0)
	machine := testMachine("machine-uid")
	allocation := testAllocation(machine.UID)
	capacity := testCapacity()
	session := testSession("session-uid")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session, &sessionv1.MachineManagementSessionBinding{}).WithObjects(machine, allocation, capacity, session).Build()
	r := &SessionReconciler{Client: c, Now: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "access"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var bindings sessionv1.MachineManagementSessionBindingList
	if err := c.List(context.Background(), &bindings, client.InNamespace(SystemNamespace)); err != nil {
		t.Fatal(err)
	}
	if len(bindings.Items) != 1 {
		t.Fatalf("bindings=%d", len(bindings.Items))
	}
	b := bindings.Items[0]
	if b.Spec.MachineUID != machine.UID || b.Spec.AllocationUID != allocation.UID || b.Spec.MachineCapacityUID != capacity.UID || b.Spec.DeviceName != "device-a" || b.Spec.ProviderProject != "provider" || b.Spec.RequestingPrincipal != "alice@example.test" {
		t.Fatalf("binding not pinned: %+v", b.Spec)
	}
}

func TestRecreatedMachineNameCannotInheritAllocation(t *testing.T) {
	scheme := testScheme(t)
	machine := testMachine("new-machine-uid")
	allocation := testAllocation("old-machine-uid")
	session := testSession("session-uid")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(machine, allocation, testCapacity(), session).Build()
	r := &SessionReconciler{Client: c}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "access"}}
	_, _ = r.Reconcile(context.Background(), req)
	_, _ = r.Reconcile(context.Background(), req)
	var got sessionv1.MachineManagementSession
	if err := c.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Conditions) == 0 || got.Status.Conditions[0].Reason != "AllocationChanged" {
		t.Fatalf("conditions %+v", got.Status.Conditions)
	}
}

func testSession(uid types.UID) *sessionv1.MachineManagementSession {
	return &sessionv1.MachineManagementSession{TypeMeta: metav1.TypeMeta{APIVersion: sessionv1.GroupVersion.String(), Kind: "MachineManagementSession"}, ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: "default", UID: uid, Annotations: map[string]string{PrincipalAnnotation: "alice@example.test"}}, Spec: sessionv1.MachineManagementSessionSpec{MachineRef: sessionv1.LocalReference{Name: "db-01"}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", TTL: metav1.Duration{Duration: 15 * time.Minute}}}
}

func testMachine(uid types.UID) *fleetv1.Machine {
	return &fleetv1.Machine{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1.GroupVersion.String(), Kind: "Machine"}, ObjectMeta: metav1.ObjectMeta{Name: "db-01", Namespace: "default", UID: uid}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "class"}, MachineProfileRef: fleetv1.LocalReference{Name: "profile"}, LocationRef: fleetv1.LocalReference{Name: "location"}}}
}

func testCapacity() *fleetv1.MachineCapacity {
	return &fleetv1.MachineCapacity{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1.GroupVersion.String(), Kind: "MachineCapacity"}, ObjectMeta: metav1.ObjectMeta{Name: "capacity-a", Namespace: "provider-ns", UID: "capacity-uid"}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "device-a"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "clean"}, ReclaimPolicy: "Retain"}}
}

func testAllocation(machineUID types.UID) *fleetv1.MachineAllocation {
	return &fleetv1.MachineAllocation{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1.GroupVersion.String(), Kind: "MachineAllocation"}, ObjectMeta: metav1.ObjectMeta{Name: "allocation-a", Namespace: SystemNamespace, UID: "allocation-uid", Annotations: map[string]string{AllocationMachineUIDAnnotation: string(machineUID), MemberClusterAnnotation: "cell-a", LocationNameAnnotation: "location", ProviderProjectAnnotation: "provider"}}, Spec: fleetv1.MachineAllocationSpec{MachineRef: fleetv1.ProjectNamespacedReference{Project: "consumer", Namespace: "default", Name: "db-01"}, MachineCapacityRef: fleetv1.NamespacedReference{Namespace: "provider-ns", Name: "capacity-a"}}, Status: fleetv1.MachineAllocationStatus{Phase: "Active"}}
}
