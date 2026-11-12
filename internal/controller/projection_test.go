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

func TestReleasedAllocationTerminallyRevokesProjection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0).UTC()
	binding := &fleetv1alpha1.MachineManagementSessionBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "session-uid", Namespace: SystemNamespace},
		Spec: fleetv1alpha1.MachineManagementSessionBindingSpec{
			SessionUID: "session-uid", ClaimUID: "claim-uid", MachineUID: "machine-uid",
			BindingID: "binding", BindingEpoch: 1, LocationUID: "location", LocationRevision: "r1",
			AdapterRoute: "sushy-libvirt-v1", ExpiresAt: metav1.NewTime(now.Add(time.Hour)),
		},
	}
	allocation := &fleetv1alpha1.MachineAllocationBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: SystemNamespace, Labels: map[string]string{ClaimUIDLabel: "claim-uid"}},
		Spec:       fleetv1alpha1.MachineAllocationBindingSpec{ClaimUID: "claim-uid", MachineUID: "machine-uid", BindingID: "binding", BindingEpoch: 1, LocationUID: "location", LocationRevision: "r1", AdapterRoute: "sushy-libvirt-v1", Active: false},
	}
	projection := &fleetv1alpha1.ProjectedMachineManagementSession{ObjectMeta: metav1.ObjectMeta{Name: binding.Name, Namespace: LocationNamespace}}
	primary := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(binding).WithObjects(binding, allocation).Build()
	location := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(projection).WithObjects(projection).Build()
	r := &ProjectionReconciler{Client: primary, LocationClient: location, Now: func() time.Time { return now }}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: SystemNamespace, Name: binding.Name}})
	if err != nil {
		t.Fatal(err)
	}
	var got fleetv1alpha1.MachineManagementSessionBinding
	if err := primary.Get(context.Background(), types.NamespacedName{Namespace: SystemNamespace, Name: binding.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[RevokedAtAnnotation] == "" {
		t.Fatal("released allocation did not permanently revoke admitted binding")
	}
	var removed fleetv1alpha1.ProjectedMachineManagementSession
	if err := location.Get(context.Background(), types.NamespacedName{Namespace: LocationNamespace, Name: binding.Name}, &removed); err == nil {
		t.Fatal("location projection survived allocation release")
	}
}
