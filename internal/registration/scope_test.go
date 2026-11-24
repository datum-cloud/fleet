package registration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	metal "go.datum.net/fleet/internal/api/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func capacityRaw(t *testing.T, capacity *metal.MachineCapacity) runtime.RawExtension {
	t.Helper()
	raw, err := json.Marshal(capacity)
	if err != nil {
		t.Fatal(err)
	}
	return runtime.RawExtension{Raw: raw}
}

func clone(capacity *metal.MachineCapacity) *metal.MachineCapacity {
	return capacity.DeepCopyObject().(*metal.MachineCapacity)
}

// Capacity without the registration marker is not inventory-backed. The
// handler allows it without touching Devices or the provider project, even
// when both are unavailable.
func TestUnmarkedCapacityIsNotGoverned(t *testing.T) {
	handler := &Handler{Projects: []string{"provider"}, Resolve: func(context.Context, string) (Project, error) {
		t.Error("unmarked capacity resolved the provider project")
		return Project{}, errors.New("unavailable")
	}, Authorize: func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error {
		t.Error("unmarked capacity was authorized against Devices")
		return errors.New("unavailable")
	}}
	synthetic := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "synthetic-01", Namespace: "fleet-provider", Labels: map[string]string{"fleet.datumapis.com/capacity-pool": "synthetic"}, Annotations: map[string]string{"example.com/note": "synthetic"}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "synthetic-device"}, AllocationPolicy: "Enabled", ReclaimPolicy: "Retain", CleaningPolicyRef: metal.LocalReference{Name: "synthetic-tenant-reuse-v1"}}}
	changed := clone(synthetic)
	changed.Spec.AllocationPolicy = "Disabled"
	resource := metav1.GroupVersionResource{Group: "metal.datumapis.com", Version: "v1alpha1", Resource: "machinecapacities"}
	requests := map[string]admissionv1.AdmissionRequest{
		"create":        {Operation: admissionv1.Create, Object: capacityRaw(t, synthetic)},
		"spec update":   {Operation: admissionv1.Update, Object: capacityRaw(t, changed), OldObject: capacityRaw(t, synthetic)},
		"status update": {Operation: admissionv1.Update, SubResource: "status", Object: capacityRaw(t, changed), OldObject: capacityRaw(t, synthetic)},
		"delete":        {Operation: admissionv1.Delete, OldObject: capacityRaw(t, synthetic)},
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			request.Name, request.Namespace, request.Resource = synthetic.Name, synthetic.Namespace, resource
			if response := handler.Handle(context.Background(), admission.Request{AdmissionRequest: request}); !response.Allowed {
				t.Fatalf("unmarked capacity denied: %s", response.Result.Message)
			}
		})
	}
}

// Marked capacity stays fenced, and the marker cannot be added, changed or
// removed to move capacity into or out of the gate.
func TestMarkerTransitionsAreDenied(t *testing.T) {
	handler, device := fixture(t)
	registration := registrationRequest(t, device)
	var registered metal.MachineCapacity
	if err := json.Unmarshal(registration.Object.Raw, &registered); err != nil {
		t.Fatal(err)
	}
	unmarked := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "hand-made", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	adopted := clone(unmarked)
	adopted.Annotations = map[string]string{DeviceUIDAnnotation: string(device.UID)}
	enabledAdoption := clone(adopted)
	enabledAdoption.Spec.AllocationPolicy = "Enabled"
	stripped := clone(&registered)
	stripped.Annotations = nil
	changed := clone(&registered)
	changed.Annotations[DeviceUIDAnnotation] = "other"
	partial := clone(&registered)
	delete(partial.Annotations, DeviceUIDAnnotation)
	squatted := clone(unmarked)
	squatted.Name = CapacityName("squatted")
	cases := map[string]struct {
		operation   admissionv1.Operation
		subresource string
		object, old *metal.MachineCapacity
	}{
		"marker added":                {operation: admissionv1.Update, object: adopted, old: unmarked},
		"marker added while enabling": {operation: admissionv1.Update, object: enabledAdoption, old: unmarked},
		"marker removed":              {operation: admissionv1.Update, object: stripped, old: &registered},
		"marker changed":              {operation: admissionv1.Update, object: changed, old: &registered},
		"one marker removed":          {operation: admissionv1.Update, object: partial, old: &registered},
		"marked update":               {operation: admissionv1.Update, object: &registered, old: &registered},
		"marked delete":               {operation: admissionv1.Delete, old: &registered},
		"marked status when routed":   {operation: admissionv1.Update, subresource: "status", object: &registered, old: &registered},
		"derived name without marker": {operation: admissionv1.Create, object: squatted},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			request := registration
			request.Operation, request.SubResource = test.operation, test.subresource
			request.Object, request.OldObject = runtime.RawExtension{}, runtime.RawExtension{}
			if test.object != nil {
				request.Object, request.Name = capacityRaw(t, test.object), test.object.Name
			}
			if test.old != nil {
				request.OldObject, request.Name = capacityRaw(t, test.old), test.old.Name
			}
			if response := handler.Handle(context.Background(), request); response.Allowed {
				t.Fatal("inventory-backed capacity escaped the gate")
			}
		})
	}
}
