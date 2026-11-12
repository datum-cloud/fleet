package gateway

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

func TestBindingInputsCannotChangeAfterGatewayAcceptance(t *testing.T) {
	a := fleetv1alpha1.ProjectedMachineManagementSessionSpec{
		MachineCapacityRef: fleetv1alpha1.LocalReference{Name: "capacity"}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "key",
		ConnectBefore: metav1.NewTime(time.Unix(100, 0)), ExpiresAt: metav1.NewTime(time.Unix(200, 0)),
		AuthorityFreshUntil: metav1.NewTime(time.Unix(110, 0)),
	}
	b := a
	b.AuthorityFreshUntil = metav1.NewTime(time.Unix(120, 0))
	if !sameBinding(a, b) {
		t.Fatal("authority freshness was not refreshable")
	}
	b.MachineCapacityRef.Name = "changed"
	if sameBinding(a, b) {
		t.Fatal("changed capacity was accepted")
	}
}

func TestEnrollmentResolutionUsesCapacityDeviceNotCustomerTarget(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "allocated", Namespace: DefaultEnrollmentNamespace}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "device-a"}}}
	enrollment := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "device-a", Namespace: DefaultEnrollmentNamespace, Labels: map[string]string{EnrollmentDeviceLabel: "device-a"}}, Data: map[string][]byte{"adapter": []byte("sushy-libvirt-v1"), "endpoint": []byte("https://private.invalid"), "system-id": []byte("physical-a"), "username": []byte("user"), "password": []byte("pass")}}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "device-b", Namespace: DefaultEnrollmentNamespace, Labels: map[string]string{EnrollmentDeviceLabel: "device-b"}}, Data: map[string][]byte{"adapter": []byte("sushy-libvirt-v1"), "endpoint": []byte("https://foreign.invalid"), "system-id": []byte("physical-b"), "username": []byte("user"), "password": []byte("pass")}}
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(capacity, enrollment, foreign).Build(), Config: Config{SupportedAdapter: "sushy-libvirt-v1"}}
	target, err := r.resolveTarget(context.Background(), fleetv1alpha1.ProjectedMachineManagementSessionSpec{MachineCapacityRef: fleetv1alpha1.LocalReference{Name: "allocated"}})
	if err != nil {
		t.Fatal(err)
	}
	if target.DeviceName != "device-a" || target.SystemID != "physical-a" || target.BaseURL != "https://private.invalid" {
		t.Fatalf("wrong enrollment resolved: %+v", target)
	}
}
