package controller

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
)

func TestAdmissionRetainsAuthenticatedPrincipal(t *testing.T) {
	s := fleetv1alpha1.MachineManagementSession{Spec: fleetv1alpha1.MachineManagementSessionSpec{MachineRef: fleetv1alpha1.LocalReference{Name: "db-01"}, Protocol: "Redfish", AccessProfile: "Provisioning", ClientPublicKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}
	raw, _ := json.Marshal(s)
	resp := SessionAdmission{}.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Create, Object: runtime.RawExtension{Raw: raw}, UserInfo: authnv1.UserInfo{Username: "alice@example.test"}}})
	if !resp.Allowed || len(resp.Patches) == 0 {
		t.Fatalf("response %+v", resp)
	}
}

func TestAdmissionRejectsSpecMutation(t *testing.T) {
	old := fleetv1alpha1.MachineManagementSession{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{PrincipalAnnotation: "alice"}}, Spec: fleetv1alpha1.MachineManagementSessionSpec{Protocol: "Redfish", AccessProfile: "Provisioning"}}
	newObj := *old.DeepCopy()
	newObj.Spec.AccessProfile = "Other"
	oldRaw, _ := json.Marshal(old)
	newRaw, _ := json.Marshal(newObj)
	resp := SessionAdmission{}.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, Object: runtime.RawExtension{Raw: newRaw}, OldObject: runtime.RawExtension{Raw: oldRaw}}})
	if resp.Allowed {
		t.Fatal("mutable spec accepted")
	}
}
