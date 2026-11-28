package delivery

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// TestOnlyTheAdmittedOperationIsDelivered never builds an assignment from
// an operation that does not carry the fence custody admitted it at, so a
// same-name operation created later is never delivered as the admitted one.
func TestOnlyTheAdmittedOperationIsDelivered(t *testing.T) {
	state := &ops.DeviceCustody{ObjectMeta: metav1.ObjectMeta{Name: "server-example-001", Namespace: "default"}, Spec: ops.DeviceCustodySpec{
		ActiveOperation: &ops.ActiveOperation{OperationReference: ops.OperationReference{Kind: "DeviceWipe", Name: "wipe-example"}, Fence: 2}}}
	for _, tt := range []struct {
		name      string
		admission *ops.OperationAdmission
		delivered bool
	}{
		{"recreated, with no admission record", nil, false},
		{"admitted at another fence", &ops.OperationAdmission{Fence: 1}, false},
		{"admitted at the active fence", &ops.OperationAdmission{Fence: 2}, true},
	} {
		operation := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: "wipe-example", Namespace: "default"}, Status: ops.OperationStatus{Admission: tt.admission}}
		assignment, err := Assignment(state, operation)
		if delivered := err == nil; delivered != tt.delivered {
			t.Fatalf("%s: delivered = %v, %v", tt.name, delivered, err)
		}
		if tt.delivered && assignment.Fence != 2 {
			t.Fatalf("%s: fence = %d", tt.name, assignment.Fence)
		}
	}
}
