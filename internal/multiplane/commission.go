package multiplane

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/registration"
)

const (
	// HardwareModelLabel and HardwareModel select the lab Device for the
	// lab plan.
	HardwareModelLabel = "fleet.datumapis.com/hardware-model"
	HardwareModel      = "fleet-lab"
	// PoolLabel and Pool are the capacity pool the local class selects.
	PoolLabel = registration.PoolLabel
	Pool      = "local"
	// PlanName is the lab commissioning plan.
	PlanName = "fleet-lab-v1"
	// CleaningPolicy is the fixture cleaning policy the capacity names.
	CleaningPolicy = "local-cleaning"
)

// labPlan is the lab target's hardware as the lab BMC reports it, the lab
// firmware, and a one-minute burn-in of the site's boot image.
func labPlan(author ops.Creator, image string) (*ops.DeviceCommissioningPlan, error) {
	_, digest, ok := strings.Cut(image, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") {
		return nil, fmt.Errorf("the site image %q is not pinned by digest", image)
	}
	return &ops.DeviceCommissioningPlan{ObjectMeta: metav1.ObjectMeta{Name: PlanName, Namespace: DeviceNamespace}, Spec: ops.DeviceCommissioningPlanSpec{
		Creator:        author,
		DeviceSelector: metav1.LabelSelector{MatchLabels: map[string]string{HardwareModelLabel: HardwareModel}},
		Content: ops.CommissioningPlanContent{
			Inspection: ops.CommissioningInspection{CPU: ops.InspectionCPU{Architecture: "amd64", Cores: 2}, Memory: ops.InspectionValue{Capacity: "2Gi"},
				Disks: ops.InspectionDisks{Count: 1, MinCapacity: "1Gi"}, Interfaces: ops.InspectionCount{Count: 1}},
			Firmware: []ops.FirmwareBaseline{{Component: "BMC", Version: "fleet-lab-1.0"}},
			BurnIn:   ops.CommissioningBurnIn{Image: ops.ImageInput{Name: "fleet-local-linux", Digest: digest}, Duration: metav1.Duration{Duration: time.Minute}},
		},
		EvidenceLifetime: metav1.Duration{Duration: 24 * time.Hour},
	}}, nil
}

// Commission takes the registered server from Disabled to Available as four
// people: a plan author writes the lab plan, a second person approves it, an
// operator commissions the Device, and a capacity operator enables its
// capacity. It
// converges: each call does what is left and reports what it waits for.
func (r *Runner) Commission(ctx context.Context) error {
	if err := r.verified(ctx); err != nil {
		return err
	}
	people := map[string]client.Client{}
	creators := map[string]ops.Creator{}
	for _, who := range []string{"plan-author", "plan-approver", "operator", "capacity-operator"} {
		path := filepath.Join(r.Config.State, "provider", who+"-project.json")
		api, err := API(path)
		if err != nil {
			return err
		}
		creator, err := identity(ctx, path)
		if err != nil {
			return err
		}
		people[who], creators[who] = api, creator
	}
	plan, err := labPlan(creators["plan-author"], r.Config.Image)
	if err != nil {
		return err
	}
	if err := people["plan-author"].Create(ctx, plan); apierrors.IsAlreadyExists(err) {
		if err := people["plan-author"].Get(ctx, client.ObjectKeyFromObject(plan), plan); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("write the plan: %w", err)
	}
	approval, err := custody.NewApproval(plan, creators["plan-approver"])
	if err != nil {
		return err
	}
	if err := people["plan-approver"].Create(ctx, approval); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("approve the plan: %w", err)
	}
	operator := people["operator"]
	device := &inventory.Device{}
	if err := operator.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, device); err != nil {
		return err
	}
	capacity := registered(device)
	if err := operator.Get(ctx, client.ObjectKeyFromObject(capacity), capacity); err != nil {
		return fmt.Errorf("the registered capacity: %w", err)
	}
	if err := r.commissioned(ctx, operator, device, plan, creators["operator"]); err != nil {
		return err
	}
	prepared, healthy := apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionPrepared), apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionHealthy)
	if !current(prepared, capacity.Generation) || !current(healthy, capacity.Generation) {
		return fmt.Errorf("%w: the projector has not made the capacity Prepared and Healthy: %s", errPending, describe(capacity))
	}
	if capacity.Spec.AllocationPolicy != "Enabled" {
		capacity.Spec.AllocationPolicy, capacity.Spec.CleaningPolicyRef.Name = "Enabled", CleaningPolicy
		if capacity.Labels == nil {
			capacity.Labels = map[string]string{}
		}
		capacity.Labels[PoolLabel] = Pool
		if err := people["capacity-operator"].Update(ctx, capacity); err != nil {
			return fmt.Errorf("enable the capacity: %w", err)
		}
		return fmt.Errorf("%w: enabled the capacity; waiting for the projector", errPending)
	}
	if available := apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionAvailable); !current(available, capacity.Generation) {
		return fmt.Errorf("%w: the capacity is enabled but not Available: %s", errPending, describe(capacity))
	}
	return nil
}

// commissioned creates one commissioning of the Device under the plan and
// waits for it to pass. A failed or unknown commissioning ends the phase.
func (r *Runner) commissioned(ctx context.Context, operator client.Client, device *inventory.Device, plan *ops.DeviceCommissioningPlan, creator ops.Creator) error {
	var list ops.DeviceCommissioningList
	if err := operator.List(ctx, &list, client.InNamespace(DeviceNamespace)); err != nil {
		return err
	}
	for _, operation := range list.Items {
		if operation.Spec.DeviceRef.UID != string(device.UID) {
			continue
		}
		switch status := operation.Status; {
		case status.Phase == ops.PhaseSucceeded:
			if result := evidenceResult(status); result != string(ops.CommissioningPassed) {
				return fmt.Errorf("%w: commissioning %s proved the server failed: %s", errTerminal, operation.Name, result)
			}
			return nil
		case status.Phase.Ended():
			return fmt.Errorf("%w: commissioning %s ended %s (%s): %s", errTerminal, operation.Name, status.Phase, status.Reason, status.Message)
		default:
			return fmt.Errorf("%w: commissioning %s is %s", errPending, operation.Name, status.Phase)
		}
	}
	operation, err := custody.NewCommissioning(device, plan, creator, "lab intake")
	if err != nil {
		return err
	}
	if err := operator.Create(ctx, operation); err != nil {
		return fmt.Errorf("commission the Device: %w", err)
	}
	return fmt.Errorf("%w: created commissioning %s", errPending, operation.Name)
}

func evidenceResult(status ops.OperationStatus) string {
	if status.Evidence == nil {
		return "no evidence"
	}
	var evidence ops.CommissioningEvidence
	if err := json.Unmarshal(status.Evidence.Raw, &evidence); err != nil {
		return "unreadable evidence"
	}
	if evidence.Result != ops.CommissioningPassed {
		return fmt.Sprintf("%s in %s: %v", evidence.Result, evidence.FailedPhase, evidence.Mismatches)
	}
	return string(ops.CommissioningPassed)
}

func current(condition *metav1.Condition, generation int64) bool {
	return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == generation
}

func describe(capacity *fleet.MachineCapacity) string {
	var parts []string
	for _, condition := range capacity.Status.Conditions {
		parts = append(parts, condition.Type+"="+string(condition.Status)+"/"+condition.Reason)
	}
	if len(parts) == 0 {
		return "no conditions yet"
	}
	return strings.Join(parts, ", ")
}
