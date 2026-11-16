package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coordinationv1 "go.datum.net/fleet/internal/api/coordination/v1alpha1"
	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
)

func TestReleasedAllocationTerminallyRevokesProjection(t *testing.T) {
	scheme := testScheme(t)
	now := time.Unix(1000, 0).UTC()
	machine := testMachine("machine-uid")
	allocation := testAllocation(machine.UID)
	allocation.Status.Phase = "Released"
	capacity := testCapacity()
	binding := &sessionv1.MachineManagementSessionBinding{ObjectMeta: metav1.ObjectMeta{Name: "mms-internal", Namespace: SystemNamespace}, Spec: sessionv1.MachineManagementSessionBindingSpec{
		SessionUID: "session-uid", ConsumerProject: "consumer", ProviderProject: "provider", MachineNamespace: "default", MachineName: "db-01", MachineUID: machine.UID,
		AllocationName: allocation.Name, AllocationUID: allocation.UID, MachineCapacityNamespace: capacity.Namespace, MachineCapacityName: capacity.Name, MachineCapacityUID: capacity.UID,
		DeviceName: "device-a", LocationName: "location", MemberCluster: "cell-a", Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "key", ConnectBefore: metav1.NewTime(now.Add(time.Minute)), ExpiresAt: metav1.NewTime(now.Add(time.Hour)),
	}}
	projection := &coordinationv1.ProjectedMachineManagementSession{ObjectMeta: metav1.ObjectMeta{Name: binding.Name, Namespace: LocationNamespace, Labels: map[string]string{MemberClusterLabel: "cell-a"}}, Spec: coordinationv1.ProjectedMachineManagementSessionSpec{DesiredState: sessionv1.ProjectionActive, MachineCapacityRef: sessionv1.LocalReference{Name: capacity.Name}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "key", ConnectBefore: binding.Spec.ConnectBefore, ExpiresAt: binding.Spec.ExpiresAt, AuthorityFreshUntil: metav1.NewTime(now.Add(time.Minute))}}
	primary := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(binding).WithObjects(binding, machine, allocation, capacity).Build()
	federation := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(projection).WithObjects(projection).Build()
	r := &ProjectionReconciler{Client: primary, FederationClient: federation, Now: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: SystemNamespace, Name: binding.Name}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var got sessionv1.MachineManagementSessionBinding
	if err := primary.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[RevokedAtAnnotation] == "" {
		t.Fatal("released allocation did not permanently revoke admitted binding")
	}
	var revoked coordinationv1.ProjectedMachineManagementSession
	key := types.NamespacedName{Namespace: LocationNamespace, Name: binding.Name}
	if err := federation.Get(context.Background(), key, &revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked.Spec.AuthorityFreshUntil.Equal(&projection.Spec.AuthorityFreshUntil) {
		t.Fatal("revocation moved the monotonic freshness deadline")
	}
	if revoked.Spec.DesiredState != sessionv1.ProjectionRevoked {
		t.Fatal("allocation release was not propagated as revocation")
	}
	apimeta.SetStatusCondition(&revoked.Status.Conditions, metav1.Condition{Type: "CleanupConfirmed", Status: metav1.ConditionTrue, Reason: "GatewayStopped", Message: "stopped", ObservedGeneration: revoked.Generation})
	revoked.Status.TeardownSpecDigest = coordinationv1.TeardownDigest(&revoked)
	if err := federation.Status().Update(context.Background(), &revoked); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing lifecycle authority must block completion: %v", err)
	}
	if err := federation.Get(context.Background(), key, &revoked); err != nil {
		t.Fatal("projection evidence removed without lifecycle settlement")
	}
}

func TestProjectionFreshnessCannotExceedExpiry(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	r := &ProjectionReconciler{Now: func() time.Time { return now }, Freshness: 2 * time.Minute}
	b := &sessionv1.MachineManagementSessionBinding{Spec: sessionv1.MachineManagementSessionBindingSpec{ExpiresAt: metav1.NewTime(now.Add(time.Second))}}
	p := r.build(b)
	if !p.Spec.AuthorityFreshUntil.Equal(&b.Spec.ExpiresAt) {
		t.Fatal("projection extends authority past expiry")
	}
	if got := p.APIVersion; got != "coordination.fleet.datumapis.com/v1alpha1" {
		t.Fatalf("projection API = %s", got)
	}
}
