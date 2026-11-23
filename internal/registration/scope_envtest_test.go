package registration

import (
	"context"
	"testing"

	metal "go.datum.net/fleet/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// syntheticCapacity is a non-inventory capacity as a GitOps controller
// applies it with server-side apply.
func syntheticCapacity(policy, cleaning string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metal.datumapis.com/v1alpha1",
		"kind":       "MachineCapacity",
		"metadata": map[string]any{
			"name":      "synthetic-01",
			"namespace": "synthetic-provider",
			"labels":    map[string]any{"fleet.datumapis.com/capacity-pool": "synthetic"},
		},
		"spec": map[string]any{
			"deviceRef":         map[string]any{"name": "synthetic-device-01"},
			"allocationPolicy":  policy,
			"cleaningPolicyRef": map[string]any{"name": cleaning},
			"reclaimPolicy":     "Retain",
		},
	}}
}

// TestWebhookGovernsOnlyInventoryBackedCapacity runs the packaged webhook
// configuration against a real API server. Capacity without the registration
// marker is never routed, so its writes succeed even with the backend gone.
// Marked capacity stays fenced, and the marker cannot be added, changed or
// removed.
func TestWebhookGovernsOnlyInventoryBackedCapacity(t *testing.T) {
	api := startAPI(t)
	ctx := context.Background()
	if err := api.admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "synthetic-provider"}}); err != nil {
		t.Fatal(err)
	}
	device := api.device("scoped", "default", "scoped-serial", "77777777-7777-7777-7777-777777777771")
	webhook := api.installWebhook()
	operator := api.actor("operator", "system:masters")

	registered := api.registration(device)
	if err := operator.Create(ctx, registered); err != nil {
		t.Fatalf("eligible registration denied: %v", err)
	}
	current := func() *metal.MachineCapacity {
		t.Helper()
		capacity := &metal.MachineCapacity{}
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(registered), capacity); err != nil {
			t.Fatal(err)
		}
		return capacity
	}

	// Hand-made capacity in the inventory namespace, created while the
	// webhook is serving.
	handMade := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "hand-made", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	if err := operator.Create(ctx, handMade); err != nil {
		t.Fatalf("hand-made capacity denied: %v", err)
	}

	t.Run("marked capacity cannot be updated or deleted", func(t *testing.T) {
		capacity := current()
		capacity.Labels = map[string]string{"edited": "true"}
		requireDenied(t, operator.Update(ctx, capacity), "mutation and deletion are disabled")
		requireDenied(t, operator.Delete(ctx, current()), "mutation and deletion are disabled")
		requireDenied(t, api.admin.Delete(ctx, current()), "mutation and deletion are disabled")
	})
	t.Run("the marker cannot be removed or changed", func(t *testing.T) {
		stripped := current()
		stripped.Annotations = nil
		requireDenied(t, operator.Update(ctx, stripped), "mutation and deletion are disabled")
		partial := current()
		delete(partial.Annotations, DeviceUIDAnnotation)
		requireDenied(t, operator.Update(ctx, partial), "mutation and deletion are disabled")
		changed := current()
		changed.Annotations[DeviceUIDAnnotation] = "another-device"
		requireDenied(t, operator.Update(ctx, changed), "mutation and deletion are disabled")
		patch := client.RawPatch("application/merge-patch+json", []byte(`{"metadata":{"annotations":{"`+DeviceUIDAnnotation+`":null,"`+DeviceVersionAnnotation+`":null,"`+OperationAnnotation+`":null}}}`))
		requireDenied(t, operator.Patch(ctx, current(), patch), "mutation and deletion are disabled")
		if annotations := current().Annotations; annotations[DeviceUIDAnnotation] != string(device.UID) || len(annotations) != 3 {
			t.Fatalf("marker changed: %v", annotations)
		}
	})
	t.Run("the marker cannot be added to unmarked capacity", func(t *testing.T) {
		adopted := &metal.MachineCapacity{}
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(handMade), adopted); err != nil {
			t.Fatal(err)
		}
		adopted.Annotations = map[string]string{DeviceUIDAnnotation: string(device.UID)}
		requireDenied(t, operator.Update(ctx, adopted), "mutation and deletion are disabled")
		patch := client.RawPatch("application/merge-patch+json", []byte(`{"metadata":{"annotations":{"`+OperationAnnotation+`":"adopt"}}}`))
		requireDenied(t, operator.Patch(ctx, handMade, patch), "mutation and deletion are disabled")
	})
	t.Run("an unmarked create under the derived name is gated", func(t *testing.T) {
		squatter := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: CapacityName("77777777-0000-0000-0000-000000000000"), Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "squatted"}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
		requireDenied(t, operator.Create(ctx, squatter), "exact Device UID/resourceVersion")
	})

	// From here on the backend is unreachable. Only routed requests fail.
	webhook.Close()

	t.Run("unmarked capacity is not routed", func(t *testing.T) {
		owner := []client.ApplyOption{client.FieldOwner("synthetic-gitops"), client.ForceOwnership}
		if err := api.admin.Apply(ctx, client.ApplyConfigurationFromUnstructured(syntheticCapacity("Enabled", "synthetic-tenant-reuse-v1")), owner...); err != nil {
			t.Fatalf("synthetic create by apply: %v", err)
		}
		if err := api.admin.Apply(ctx, client.ApplyConfigurationFromUnstructured(syntheticCapacity("Disabled", "synthetic-tenant-reuse-v2")), owner...); err != nil {
			t.Fatalf("synthetic spec update by apply: %v", err)
		}
		synthetic := &metal.MachineCapacity{}
		if err := api.admin.Get(ctx, client.ObjectKey{Namespace: "synthetic-provider", Name: "synthetic-01"}, synthetic); err != nil {
			t.Fatal(err)
		}
		if synthetic.Spec.AllocationPolicy != "Disabled" || synthetic.Spec.CleaningPolicyRef.Name != "synthetic-tenant-reuse-v2" {
			t.Fatalf("synthetic apply did not persist: %+v", synthetic.Spec)
		}
		synthetic.Status.Conditions = []metav1.Condition{{Type: "Available", Status: metav1.ConditionTrue, Reason: "SyntheticProvider", LastTransitionTime: metav1.Now()}}
		if err := api.admin.Status().Update(ctx, synthetic); err != nil {
			t.Fatalf("synthetic provider status write: %v", err)
		}
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(handMade), handMade); err != nil {
			t.Fatal(err)
		}
		handMade.Labels = map[string]string{"edited": "true"}
		if err := operator.Update(ctx, handMade); err != nil {
			t.Fatalf("hand-made capacity update: %v", err)
		}
		for _, capacity := range []client.Object{synthetic, handMade} {
			if err := api.admin.Delete(ctx, capacity); err != nil {
				t.Fatalf("unmarked capacity delete: %v", err)
			}
			if api.exists(capacity) {
				t.Fatal("unmarked capacity survived deletion")
			}
		}
	})
	t.Run("marked capacity status writes are not routed", func(t *testing.T) {
		// Status is governed by RBAC alone. The status subresource cannot
		// change metadata, so it cannot strip the marker or touch spec.
		capacity := current()
		capacity.Annotations = nil
		capacity.Spec.AllocationPolicy = "Enabled"
		capacity.Spec.CleaningPolicyRef.Name = "invented-policy"
		capacity.Status.Conditions = []metav1.Condition{{Type: "Observed", Status: metav1.ConditionUnknown, Reason: "Synthetic", LastTransitionTime: metav1.Now()}}
		if err := api.admin.Status().Update(ctx, capacity); err != nil {
			t.Fatalf("status write on marked capacity: %v", err)
		}
		after := current()
		if len(after.Annotations) != 3 || after.Annotations[DeviceUIDAnnotation] != string(device.UID) || after.Spec.AllocationPolicy != "Disabled" || after.Spec.CleaningPolicyRef.Name != "" || len(after.Status.Conditions) != 1 {
			t.Fatalf("status write changed more than status: %+v %+v", after.Annotations, after.Spec)
		}
	})
	t.Run("marked capacity writes fail closed without the backend", func(t *testing.T) {
		capacity := current()
		capacity.Labels = map[string]string{"edited": "true"}
		if err := operator.Update(ctx, capacity); err == nil {
			t.Fatal("marked capacity updated without the webhook")
		}
		if err := api.admin.Delete(ctx, current()); err == nil {
			t.Fatal("marked capacity deleted without the webhook")
		}
		unmarked := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "late-hand-made", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "late"}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
		if err := api.admin.Create(ctx, unmarked); err != nil {
			t.Fatalf("unmarked create without the webhook: %v", err)
		}
		unmarked.Annotations = map[string]string{DeviceUIDAnnotation: "adopted"}
		if err := api.admin.Update(ctx, unmarked); err == nil || apierrors.IsConflict(err) {
			t.Fatalf("marker added without the webhook: %v", err)
		}
	})
}
