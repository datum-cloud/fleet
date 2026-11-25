package custody

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CommissioningOverhead is the time a commissioning gets for inspection and
// boots on top of its plan's burn-in duration.
const CommissioningOverhead = time.Hour

// commissioningEvidenceLimit bounds commissioning evidence. It is larger than
// the other kinds' limit because it records inspected hardware and firmware.
const commissioningEvidenceLimit = 16 << 10

// Commissioning refusal and result reasons.
const (
	ReasonPlanNotQualified = "PlanNotQualified"
	ReasonPlanNotCurrent   = "PlanNotCurrent"
	ReasonPlanMismatch     = "PlanMismatch"
	ReasonDeviceNotActive  = "DeviceNotActive"
)

// Reasons a Device has no current plan.
const (
	ReasonNoCurrentPlan = "NoCurrentPlan"
	ReasonPlanAmbiguous = "PlanAmbiguous"
)

// PlanDigest is the digest an approval must name: the digest of the plan's
// whole spec.
func PlanDigest(plan *ops.DeviceCommissioningPlan) (string, error) {
	return SpecDigest(plan.Spec)
}

// Qualified reports whether the plan is qualified: an approval with its name
// names its UID and digest, and a different person created it.
func Qualified(ctx context.Context, reader client.Reader, plan *ops.DeviceCommissioningPlan) (bool, string, error) {
	approval := &ops.DeviceCommissioningPlanApproval{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(plan), approval); apierrors.IsNotFound(err) {
		return false, "the plan has no approval", nil
	} else if err != nil {
		return false, "", err
	}
	return qualifies(plan, approval)
}

func qualifies(plan *ops.DeviceCommissioningPlan, approval *ops.DeviceCommissioningPlanApproval) (bool, string, error) {
	digest, err := PlanDigest(plan)
	if err != nil {
		return false, "", err
	}
	reference := approval.Spec.PlanRef
	switch {
	case !approval.DeletionTimestamp.IsZero() || !plan.DeletionTimestamp.IsZero():
		return false, "the plan or its approval is being deleted", nil
	case reference.Name != plan.Name || reference.UID != string(plan.UID):
		return false, "the approval is for another plan", nil
	case reference.Digest != digest:
		return false, "the approval is for different plan content", nil
	case approval.Spec.Creator.Username == "" || approval.Spec.Creator.Username == plan.Spec.Creator.Username:
		return false, "the plan's author approved it", nil
	}
	return true, "", nil
}

// CurrentPlan returns the Device's current plan: the one qualified plan whose
// selector matches the Device and that no other qualified plan matching the
// Device supersedes. With no such plan, or more than one, it returns nil and
// the reason.
func CurrentPlan(ctx context.Context, reader client.Reader, device *inventory.Device) (*ops.DeviceCommissioningPlan, string, error) {
	var plans ops.DeviceCommissioningPlanList
	if err := reader.List(ctx, &plans, client.InNamespace(device.Namespace)); err != nil {
		return nil, "", err
	}
	var approvals ops.DeviceCommissioningPlanApprovalList
	if err := reader.List(ctx, &approvals, client.InNamespace(device.Namespace)); err != nil {
		return nil, "", err
	}
	return currentPlan(plans.Items, approvals.Items, device)
}

func currentPlan(plans []ops.DeviceCommissioningPlan, approvals []ops.DeviceCommissioningPlanApproval, device *inventory.Device) (*ops.DeviceCommissioningPlan, string, error) {
	approvalFor := map[string]*ops.DeviceCommissioningPlanApproval{}
	for index := range approvals {
		approvalFor[approvals[index].Name] = &approvals[index]
	}
	var qualified []*ops.DeviceCommissioningPlan
	for index := range plans {
		plan := &plans[index]
		approval := approvalFor[plan.Name]
		if approval == nil {
			continue
		}
		ok, _, err := qualifies(plan, approval)
		if err != nil {
			return nil, "", err
		}
		if ok {
			qualified = append(qualified, plan)
		}
	}
	// A supersession applies only to the Devices the superseding plan
	// selects, so only plans that match the Device count.
	var selecting []*ops.DeviceCommissioningPlan
	for _, plan := range qualified {
		selector, err := metav1.LabelSelectorAsSelector(&plan.Spec.DeviceSelector)
		if err != nil || selector.Empty() {
			continue
		}
		if selector.Matches(labels.Set(device.Labels)) {
			selecting = append(selecting, plan)
		}
	}
	superseded := map[string]bool{}
	for _, plan := range selecting {
		for _, name := range plan.Spec.Supersedes {
			if name != plan.Name {
				superseded[name] = true
			}
		}
	}
	var matching []*ops.DeviceCommissioningPlan
	for _, plan := range selecting {
		if !superseded[plan.Name] {
			matching = append(matching, plan)
		}
	}
	switch {
	case len(matching) == 0 && len(selecting) > 0:
		// Every plan that applies is superseded by another that applies:
		// plans that supersede each other.
		return nil, ReasonPlanAmbiguous, errors.New("the plans that apply supersede each other")
	case len(matching) == 0:
		return nil, ReasonNoCurrentPlan, nil
	case len(matching) == 1:
		return matching[0], "", nil
	}
	names := make([]string, 0, len(matching))
	for _, plan := range matching {
		names = append(names, plan.Name)
	}
	sort.Strings(names)
	return nil, ReasonPlanAmbiguous, fmt.Errorf("plans %v all apply", names)
}

// DeviceCommissioningKind is the registry entry for DeviceCommissioning.
func DeviceCommissioningKind() Kind {
	return Kind{
		GVK:     gv.WithKind("DeviceCommissioning"),
		New:     func() ops.Operation { return &ops.DeviceCommissioning{} },
		NewList: func() client.ObjectList { return &ops.DeviceCommissioningList{} },
		Items: func(list client.ObjectList) []ops.Operation {
			return items(list.(*ops.DeviceCommissioningList).Items)
		},
		Digest: func(operation ops.Operation) (string, error) {
			return SpecDigest(operation.(*ops.DeviceCommissioning).Spec)
		},
		Naming:   GeneratedName,
		Deadline: CommissioningOverhead,
		DeadlineFor: func(operation ops.Operation) time.Duration {
			return CommissioningOverhead + operation.(*ops.DeviceCommissioning).Spec.Plan.Content.BurnIn.Duration.Duration
		},
		Effectful:     true,
		BootInterface: func(ops.Operation) string { return CommissioningBootInterface },
		Check:         checkCommissioning,
		SnapshotExtra: func(admission *Admission) map[string]string {
			return map[string]string{SnapshotSerialNumber: admission.Device.Spec.Identity.Serial}
		},
		// A held commissioning is settled by a later one that succeeds. No
		// Retry is needed: staff create the settling operation directly.
		Settles:     []SettleRule{{Held: "DeviceCommissioning"}},
		KeepsLatest: true,
		Reasons: []Reason{
			{Name: ReasonPlanNotQualified, RaisedBy: RaisedByCoordinator},
			{Name: ReasonPlanNotCurrent, RaisedBy: RaisedByCoordinator},
			{Name: ReasonPlanMismatch, RaisedBy: RaisedByBoth},
			{Name: ReasonDeviceNotActive, RaisedBy: RaisedByCoordinator},
			{Name: ReasonBootInterfaceUnknown, RaisedBy: RaisedByCoordinator},
			{Name: ReasonTargetUnsupported, RaisedBy: RaisedByWorker},
			{Name: ReasonInputsUnreadable, RaisedBy: RaisedByWorker},
		},
		Evidence: StrictEvidence(commissioningEvidenceLimit, func(operation ops.Operation, evidence *ops.CommissioningEvidence) error {
			plan := operation.(*ops.DeviceCommissioning).Spec.Plan
			switch {
			case evidence.Plan != plan.PlanReference:
				return errors.New("the evidence names another plan")
			case evidence.Result == ops.CommissioningPassed && (evidence.FailedPhase != "" || len(evidence.Mismatches) != 0 || evidence.Hardware == nil || evidence.BurnIn == nil || evidence.BurnIn.Result != ops.BurnInPassed):
				return errors.New("a passed commissioning records inspected hardware and a passed burn-in, and no failure")
			case evidence.Result == ops.CommissioningFailed && evidence.FailedPhase != ops.PhaseInspect && evidence.FailedPhase != ops.PhaseFirmware && evidence.FailedPhase != ops.PhaseBurnIn:
				return errors.New("a failed commissioning names the phase that failed")
			case evidence.Result != ops.CommissioningPassed && evidence.Result != ops.CommissioningFailed:
				return errors.New("the evidence records no result")
			case len(evidence.Firmware) > 32 || len(evidence.Mismatches) > 32:
				return errors.New("evidence records at most 32 firmware components and 32 mismatches")
			case evidence.Hardware != nil && (len(evidence.Hardware.Disks) > 64 || len(evidence.Hardware.Interfaces) > 64):
				return errors.New("evidence records at most 64 disks and 64 interfaces")
			}
			return validateSteps(evidence.Steps)
		}),
		EvidenceRequired: true,
	}
}

// SnapshotSerialNumber is the snapshot field that carries the Device's
// recorded serial number, which the worker compares with the BMC's.
const SnapshotSerialNumber = "serialNumber"

// CommissioningBootInterface is the Device interface a commissioning boots
// from. Burn-in boots over the network, so the snapshot records its MAC.
const CommissioningBootInterface = "primary"

func checkCommissioning(ctx context.Context, admission *Admission) *Refusal {
	commissioning := admission.Operation.(*ops.DeviceCommissioning)
	if inventory.LifecycleOf(admission.Device) != inventory.DeviceActive {
		return refuse(ReasonDeviceNotActive, "the Device is %s", inventory.LifecycleOf(admission.Device))
	}
	if !hasBootInterface(admission.Device) {
		return refuse(ReasonBootInterfaceUnknown, "the Device records no interface %q with a MAC address", CommissioningBootInterface)
	}
	if admission.Reader == nil {
		return refuse(ReasonPlanNotQualified, "the plan could not be read")
	}
	input := commissioning.Spec.Plan
	plan := &ops.DeviceCommissioningPlan{}
	if err := admission.Reader.Get(ctx, client.ObjectKey{Namespace: commissioning.Namespace, Name: input.Name}, plan); err != nil {
		return refuse(ReasonPlanNotQualified, "plan %s could not be read", input.Name)
	}
	if string(plan.UID) != input.UID {
		return refuse(ReasonPlanNotQualified, "plan %s has another UID", input.Name)
	}
	ok, why, err := Qualified(ctx, admission.Reader, plan)
	if err != nil {
		return refuse(ReasonPlanNotQualified, "the plan's approval could not be read")
	}
	if !ok {
		return refuse(ReasonPlanNotQualified, "%s", why)
	}
	digest, err := PlanDigest(plan)
	if err != nil || digest != input.Digest || !reflect.DeepEqual(plan.Spec.Content, input.Content) {
		return refuse(ReasonPlanMismatch, "the operation's plan is not the approved plan")
	}
	current, reason, err := CurrentPlan(ctx, admission.Reader, admission.Device)
	switch {
	case current == nil && reason == "":
		return refuse(ReasonPlanNotCurrent, "the Device's current plan could not be read: %v", err)
	case current == nil:
		return refuse(ReasonPlanNotCurrent, "the Device has no current plan (%s)", reason)
	case current.UID != plan.UID:
		return refuse(ReasonPlanNotCurrent, "the Device's current plan is %s", current.Name)
	}
	return nil
}

// CommissioningRules are the DeviceCommissioning rows of the operations
// table: staff, from holder None, or from Service only to settle a
// commissioning hold.
func CommissioningRules() []Rule {
	return []Rule{
		{Kind: "DeviceCommissioning", Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderNone}},
		{Kind: "DeviceCommissioning", Creator: CreatorStaff, Authority: AuthorityService, Holders: []ops.HolderType{ops.HolderService}, Hold: HoldSettlesRequired},
	}
}

func hasBootInterface(device *inventory.Device) bool {
	for _, iface := range device.Spec.Interfaces {
		if iface.Name == CommissioningBootInterface && iface.MACAddress != "" {
			return true
		}
	}
	return false
}
