package custody

import (
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewApproval returns the approval that qualifies the plan as read, for the
// approver to create.
func NewApproval(plan *ops.DeviceCommissioningPlan, approver ops.Creator) (*ops.DeviceCommissioningPlanApproval, error) {
	digest, err := PlanDigest(plan)
	if err != nil {
		return nil, err
	}
	return &ops.DeviceCommissioningPlanApproval{
		ObjectMeta: metav1.ObjectMeta{Name: plan.Name, Namespace: plan.Namespace},
		Spec:       ops.DeviceCommissioningPlanApprovalSpec{Creator: approver, PlanRef: ops.PlanReference{Name: plan.Name, UID: string(plan.UID), Digest: digest}},
	}, nil
}

// NewCommissioning returns a commissioning of the Device under the plan as
// read, with an exact copy of its content, for the operator to create.
func NewCommissioning(device *inventory.Device, plan *ops.DeviceCommissioningPlan, operator ops.Creator, reason string) (*ops.DeviceCommissioning, error) {
	digest, err := PlanDigest(plan)
	if err != nil {
		return nil, err
	}
	return &ops.DeviceCommissioning{
		ObjectMeta: metav1.ObjectMeta{GenerateName: device.Name + "-commission-", Namespace: device.Namespace},
		Spec: ops.DeviceCommissioningSpec{
			OperationEnvelope: ops.OperationEnvelope{Creator: operator, DeviceRef: ops.DeviceReference{Name: device.Name, UID: string(device.UID)},
				Authority: ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: reason}}},
			Plan: ops.CommissioningPlanInput{PlanReference: ops.PlanReference{Name: plan.Name, UID: string(plan.UID), Digest: digest}, Content: plan.Spec.Content},
		},
	}, nil
}
