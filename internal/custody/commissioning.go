package custody

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
	case !distinct(plan.Spec.Creator, approval.Spec.Creator):
		return false, "the plan's author approved it", nil
	}
	return true, "", nil
}

// distinct reports whether two creators are different people: both are
// identified, and neither their UIDs nor their usernames match.
func distinct(author, approver ops.Creator) bool {
	return author.UID != "" && approver.UID != "" && author.Username != "" && approver.Username != "" &&
		author.UID != approver.UID && !strings.EqualFold(author.Username, approver.Username)
}

// CurrentPlan returns the Device's current plan: the one qualified plan whose
// selector matches the Device and that no other qualified plan matching the
// Device supersedes. With no such plan, or more than one, it returns nil and
// the reason. An error means the plans could not be read.
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
		return nil, ReasonPlanAmbiguous, nil
	case len(matching) == 0:
		return nil, ReasonNoCurrentPlan, nil
	case len(matching) == 1:
		return matching[0], "", nil
	}
	return nil, ReasonPlanAmbiguous, nil
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
		Evidence:         StrictEvidence(commissioningEvidenceLimit, validateCommissioningEvidence),
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

// validateCommissioningEvidence accepts only evidence that proves its
// verdict. A Passed result records the identity the BMC reported, matching
// the admission snapshot, hardware that meets the plan, every firmware
// baseline at its version, and a passed burn-in that ran the plan's
// duration. A Failed result names the phase and what failed.
func validateCommissioningEvidence(operation ops.Operation, evidence *ops.CommissioningEvidence) error {
	plan := operation.(*ops.DeviceCommissioning).Spec.Plan
	switch {
	case evidence.Plan != plan.PlanReference:
		return errors.New("the evidence names another plan")
	case len(evidence.Firmware) > 32 || len(evidence.Mismatches) > 32:
		return errors.New("evidence records at most 32 firmware components and 32 mismatches")
	case evidence.Hardware != nil && (len(evidence.Hardware.Disks) > 64 || len(evidence.Hardware.Interfaces) > 64):
		return errors.New("evidence records at most 64 disks and 64 interfaces")
	}
	if err := validateSteps(evidence.Steps); err != nil {
		return err
	}
	switch evidence.Result {
	case ops.CommissioningFailed:
		if evidence.FailedPhase != ops.PhaseInspect && evidence.FailedPhase != ops.PhaseFirmware && evidence.FailedPhase != ops.PhaseBurnIn {
			return errors.New("a failed commissioning names the phase that failed")
		}
		if len(evidence.Mismatches) == 0 {
			return errors.New("a failed commissioning says what failed")
		}
		return nil
	case ops.CommissioningPassed:
	default:
		return errors.New("the evidence records no result")
	}
	if evidence.FailedPhase != "" || len(evidence.Mismatches) != 0 {
		return errors.New("a passed commissioning records no failure")
	}
	if evidence.SerialNumber == "" || evidence.SystemUUID == "" || evidence.BMCPublicKeySHA256 == "" {
		return errors.New("a passed commissioning records the serial number, system UUID and BMC key it read")
	}
	// Delivery waits until status records the admission, so the snapshot is
	// there by the time a result arrives.
	snapshot := operation.OperationStatus().Snapshot
	if snapshot == nil {
		return errors.New("the admission snapshot is not recorded, so the identity cannot be checked")
	}
	if !strings.EqualFold(evidence.SystemUUID, snapshot.SystemUUID) || evidence.BMCPublicKeySHA256 != snapshot.BMCPublicKeySHA256 ||
		!strings.EqualFold(strings.TrimSpace(evidence.SerialNumber), strings.TrimSpace(snapshot.Extra[SnapshotSerialNumber])) {
		return errors.New("the identity the commissioning read differs from the admission snapshot")
	}
	hw, want := evidence.Hardware, plan.Content.Inspection
	if hw == nil {
		return errors.New("a passed commissioning records the inspected hardware")
	}
	if hw.CPU.Architecture != want.CPU.Architecture || hw.CPU.Cores != want.CPU.Cores {
		return errors.New("the inspected CPU does not meet the plan")
	}
	memory, err := resource.ParseQuantity(hw.Memory)
	planned, perr := resource.ParseQuantity(want.Memory.Capacity)
	if err != nil || perr != nil || memory.Cmp(planned) != 0 {
		return errors.New("the inspected memory does not meet the plan")
	}
	minimum, err := resource.ParseQuantity(want.Disks.MinCapacity)
	if err != nil {
		return errors.New("the plan's minimum disk capacity does not parse")
	}
	var disks int64
	for _, disk := range hw.Disks {
		if capacity, err := resource.ParseQuantity(disk.Capacity); err == nil && capacity.Cmp(minimum) >= 0 {
			disks++
		}
	}
	if disks < want.Disks.Count || int64(len(hw.Interfaces)) < want.Interfaces.Count {
		return errors.New("the inspected disks or interfaces do not meet the plan")
	}
	for _, baseline := range plan.Content.Firmware {
		if !slices.Contains(evidence.Firmware, baseline) {
			return fmt.Errorf("firmware %s is not recorded at version %s", baseline.Component, baseline.Version)
		}
	}
	burnIn := evidence.BurnIn
	if burnIn == nil || burnIn.Result != ops.BurnInPassed || burnIn.CompletedAt.Sub(burnIn.StartedAt.Time) < plan.Content.BurnIn.Duration.Duration {
		return errors.New("a passed commissioning records a passed burn-in that ran the plan's duration")
	}
	return nil
}
