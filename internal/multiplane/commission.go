package multiplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
)

// HardwareModelLabel is the label the local plan selects Devices by.
const HardwareModelLabel = "fleet.datumapis.com/hardware-model"

// LocalPlanName is the local commissioning plan.
const LocalPlanName = "local-plan-v1"

// LabFirmware is the firmware inventory the Redfish lab adapter serves
// (hack/redfish-lab/firmware.json). The Redfish run's plan requires it, so the
// worker's firmware check runs against the emulator.
var LabFirmware = []ops.FirmwareBaseline{{Component: "BMC", Version: "1.2.2-fleet-lab.1"}, {Component: "BIOS", Version: "lab-bios-1.0.0"}}

// LocalPlan is the local commissioning plan: the hardware of the local guest
// and of lab target A, which the fixture profile requests, the given firmware
// baseline, and a one-minute burn-in of the site image. The local VM reports
// no firmware, so its plan has no baseline; the Redfish run's plan requires
// LabFirmware.
func LocalPlan(author ops.Creator, burnIn ops.ImageInput, firmware []ops.FirmwareBaseline) *ops.DeviceCommissioningPlan {
	return &ops.DeviceCommissioningPlan{ObjectMeta: metav1.ObjectMeta{Name: LocalPlanName, Namespace: DeviceNamespace}, Spec: ops.DeviceCommissioningPlanSpec{
		Creator:        author,
		DeviceSelector: metav1.LabelSelector{MatchLabels: map[string]string{HardwareModelLabel: "synthetic"}},
		Content: ops.CommissioningPlanContent{
			Inspection: ops.CommissioningInspection{CPU: ops.InspectionCPU{Architecture: "amd64", Cores: 2}, Memory: ops.InspectionValue{Capacity: "2Gi"},
				Disks: ops.InspectionDisks{Count: 1, MinCapacity: "1Gi"}, Interfaces: ops.InspectionCount{Count: 1}},
			Firmware: firmware,
			BurnIn:   ops.CommissioningBurnIn{Image: burnIn, Duration: metav1.Duration{Duration: time.Minute}},
		},
		EvidenceLifetime: metav1.Duration{Duration: 24 * time.Hour},
	}}
}

// Commission commissions the Device as three people: the plan author writes
// the local plan, another person approves its exact content, and an operator
// commissions the Device under it. It then waits until the capacity
// projector offers the capacity. It repeats safely: the plan and approval
// are found again by name, and the Device's commissioning by its Device.
func (r *Runner) Commission(ctx context.Context) error {
	if err := r.verified(ctx); err != nil {
		return err
	}
	staff := map[string]client.Client{}
	creators := map[string]ops.Creator{}
	for _, who := range []string{"plan-author", "plan-approver", "operator"} {
		path := filepath.Join(r.Config.State, "provider", who+"-project.json")
		api, err := API(path)
		if err != nil {
			return err
		}
		if creators[who], err = identity(ctx, path); err != nil {
			return err
		}
		staff[who] = api
	}
	image, err := allocation.ParseImage(r.Config.Image)
	if err != nil {
		return fmt.Errorf("%w: the site image: %v", errTerminal, err)
	}
	plan := LocalPlan(creators["plan-author"], image, r.PlanFirmware)
	if err := staff["plan-author"].Create(ctx, plan); apierrors.IsAlreadyExists(err) {
		if err := staff["plan-author"].Get(ctx, client.ObjectKeyFromObject(plan), plan); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("write the plan: %w", err)
	}
	approval, err := custody.NewApproval(plan, creators["plan-approver"])
	if err != nil {
		return err
	}
	if err := staff["plan-approver"].Create(ctx, approval); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("approve the plan: %w", err)
	}
	operator := staff["operator"]
	device := &inventory.Device{}
	if err := operator.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, device); err != nil {
		return err
	}
	operation, err := r.commissioning(ctx, operator, device)
	if err != nil {
		return err
	}
	if operation == nil {
		if operation, err = custody.NewCommissioning(device, plan, creators["operator"], "local intake"); err != nil {
			return err
		}
		if err := operator.Create(ctx, operation); err != nil {
			return fmt.Errorf("commission the Device: %w", err)
		}
	}
	switch phase := operation.Status.Phase; {
	case phase == ops.PhaseSucceeded:
	case phase.Ended():
		return fmt.Errorf("%w: the commissioning ended %s: %s %s", errTerminal, phase, operation.Status.Reason, operation.Status.Message)
	default:
		return fmt.Errorf("%w: the commissioning is %s", errPending, phase)
	}
	var evidence ops.CommissioningEvidence
	if operation.Status.Evidence == nil || json.Unmarshal(operation.Status.Evidence.Raw, &evidence) != nil {
		return fmt.Errorf("%w: the commissioning succeeded without evidence", errTerminal)
	}
	if evidence.Result != ops.CommissioningPassed {
		return fmt.Errorf("%w: the commissioning proved %s in %s: %v", errTerminal, evidence.Result, evidence.FailedPhase, evidence.Mismatches)
	}
	capacity := &fleet.MachineCapacity{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: "local-capacity"}, capacity); err != nil {
		return err
	}
	available := apiMeta.FindStatusCondition(capacity.Status.Conditions, custody.ConditionAvailable)
	if available == nil || available.Status != metav1.ConditionTrue || available.ObservedGeneration != capacity.Generation {
		reason := "no Available condition"
		if available != nil {
			reason = available.Reason
		}
		return fmt.Errorf("%w: the projector does not offer the capacity yet (%s)", errPending, reason)
	}
	record := map[string]any{"plan": plan.Name, "approval": approval.Name, "firmwareBaseline": plan.Spec.Content.Firmware, "firmware": evidence.Firmware, "author": creators["plan-author"].Username, "approver": creators["plan-approver"].Username,
		"operator": creators["operator"].Username, "operation": operation.Name, "fence": operation.Status.Fence, "result": evidence.Result,
		"burnIn": evidence.BurnIn, "steps": evidence.Steps, "hardware": capacity.Status.Hardware, "capacityAvailable": available.Reason}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.Config.State, "commissioning.json"), data, 0o600); err != nil {
		return err
	}
	fmt.Printf("commission: verified: %s wrote the plan, %s approved it, %s commissioned the Device; burn-in %s at fence %d; the projector offers the capacity\n",
		creators["plan-author"].Username, creators["plan-approver"].Username, creators["operator"].Username, evidence.BurnIn.Result, operation.Status.Fence)
	return nil
}

// commissioning returns the Device's one commissioning, or nil before the
// first. More than one means a repeat created a second, which proves nothing.
func (r *Runner) commissioning(ctx context.Context, api client.Client, device *inventory.Device) (*ops.DeviceCommissioning, error) {
	var list ops.DeviceCommissioningList
	if err := api.List(ctx, &list, client.InNamespace(DeviceNamespace)); err != nil {
		return nil, err
	}
	var mine []ops.DeviceCommissioning
	for _, item := range list.Items {
		if item.Spec.DeviceRef.Name == device.Name && item.Labels[ops.LabelDeviceUID] == string(device.UID) {
			mine = append(mine, item)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })
	switch len(mine) {
	case 0:
		return nil, nil
	case 1:
		return &mine[0], nil
	}
	return nil, fmt.Errorf("%w: the Device has %d commissionings", errTerminal, len(mine))
}
