package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1alpha1 "go.datum.net/fleet/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := fleetv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdmissionPinsProtectedAllocation(t *testing.T) {
	scheme := testScheme(t)
	now := time.Unix(1000, 0)
	claim := &fleetv1alpha1.MachineClaim{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "MachineClaim"}, ObjectMeta: metav1.ObjectMeta{Name: "db-01", Namespace: "default", UID: "claim-uid"}}
	allocation := testAllocation(claim.UID)
	session := testSession("session-uid")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session, &fleetv1alpha1.MachineManagementSessionBinding{}).WithObjects(claim, allocation, session).Build()
	r := &SessionReconciler{Client: c, Now: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "access"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	} // finalizer
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	} // binding
	var b fleetv1alpha1.MachineManagementSessionBinding
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: SystemNamespace, Name: "session-uid"}, &b); err != nil {
		t.Fatal(err)
	}
	if b.Spec.ClaimUID != "claim-uid" || b.Spec.MachineUID != "machine-uid" || b.Spec.ProviderProjectUID != "provider-project" || b.Spec.RequestingPrincipal != "alice@example.test" {
		t.Fatalf("binding not pinned: %+v", b.Spec)
	}
	if b.Spec.AdapterRoute == "" || b.Spec.BMCSystemID == "" {
		t.Fatal("private adapter identity missing")
	}
}

func TestRecreatedClaimNameCannotInheritAllocation(t *testing.T) {
	scheme := testScheme(t)
	claim := &fleetv1alpha1.MachineClaim{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "MachineClaim"}, ObjectMeta: metav1.ObjectMeta{Name: "db-01", Namespace: "default", UID: "new-claim-uid"}}
	allocation := testAllocation("old-claim-uid")
	allocation.Labels[ClaimUIDLabel] = "new-claim-uid" // even a forged lookup label cannot change retained UID
	session := testSession("session-uid")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(claim, allocation, session).Build()
	r := &SessionReconciler{Client: c}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "access"}}
	_, _ = r.Reconcile(context.Background(), req)
	_, _ = r.Reconcile(context.Background(), req)
	var got fleetv1alpha1.MachineManagementSession
	if err := c.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Conditions) == 0 || got.Status.Conditions[0].Reason != "AllocationChanged" {
		t.Fatalf("conditions %+v", got.Status.Conditions)
	}
}

func testSession(uid types.UID) *fleetv1alpha1.MachineManagementSession {
	return &fleetv1alpha1.MachineManagementSession{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "MachineManagementSession"}, ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: "default", UID: uid, Annotations: map[string]string{PrincipalAnnotation: "alice@example.test"}}, Spec: fleetv1alpha1.MachineManagementSessionSpec{MachineClaimRef: fleetv1alpha1.LocalObjectReference{Name: "db-01"}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", TTL: metav1.Duration{Duration: 15 * time.Minute}}}
}

func testAllocation(claimUID types.UID) *fleetv1alpha1.MachineAllocationBinding {
	return &fleetv1alpha1.MachineAllocationBinding{TypeMeta: metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "MachineAllocationBinding"}, ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: SystemNamespace, Labels: map[string]string{ClaimUIDLabel: string(claimUID)}}, Spec: fleetv1alpha1.MachineAllocationBindingSpec{ConsumerProjectUID: "consumer-project", ProviderProjectUID: "provider-project", ClaimNamespace: "default", ClaimName: "db-01", ClaimUID: claimUID, MachineName: "machine-a", MachineUID: "machine-uid", BindingID: "binding-a", BindingEpoch: 7, LocationUID: "location-uid", LocationRevision: "location-r1", MemberCluster: "cell-a", BareMetalMachineUID: "bmm-uid", AccessProfileRevision: "provisioning-r1", AdapterRoute: "sushy-libvirt", BMCSystemID: "vm-allocated", Active: true}}
}
