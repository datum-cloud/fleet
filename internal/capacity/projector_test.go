package capacity

import (
	"context"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestADuplicateInAnotherNamespaceCounts records the same serial number in
// two namespaces of one provider project.
func TestADuplicateInAnotherNamespaceCounts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := inventory.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default", UID: "one"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "SERIAL-1", SystemUUID: "00000000-0000-4000-8000-000000000001"}}}
	other := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "elsewhere", UID: "two"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: " serial-1", SystemUUID: "00000000-0000-4000-8000-000000000002"}}}
	alone := fake.NewClientBuilder().WithScheme(scheme).WithObjects(device).Build()
	if duplicate, err := Duplicate(context.Background(), alone, device); err != nil || duplicate {
		t.Fatalf("a lone Device has a duplicate: %v", err)
	}
	both := fake.NewClientBuilder().WithScheme(scheme).WithObjects(device, other).Build()
	if duplicate, err := Duplicate(context.Background(), both, device); err != nil || !duplicate {
		t.Fatalf("a duplicate in another namespace was missed: %v", err)
	}
}
