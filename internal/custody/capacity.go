package custody

import (
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MachineCapacity conditions the capacity projector derives. The projector
// is their only writer once it is wired; in Increment A it is a pure function
// and nothing calls it outside tests.
const (
	ConditionPrepared  = "Prepared"
	ConditionHealthy   = "Healthy"
	ConditionAvailable = "Available"
)

// CommissioningEvidence is the latest succeeded commissioning of a Device.
type CommissioningEvidence struct {
	// PlanDigest is the commissioning plan the run used.
	PlanDigest string
	// HealthCurrent says the run's health evidence is still current.
	HealthCurrent bool
}

// CapacityInputs is everything the projector reads.
type CapacityInputs struct {
	Generation int64
	// Commissioning is nil when no commissioning has succeeded.
	Commissioning *CommissioningEvidence
	// CurrentPlanDigest is the plan the Device must be commissioned under.
	CurrentPlanDigest string
	// DiscoveryCurrent says BMC discovery evidence is current.
	DiscoveryCurrent bool
	// Custody is nil when the Device has no custody.
	Custody                 *ops.DeviceCustodySpec
	AllocationPolicyEnabled bool
}

// ProjectCapacity derives Prepared, Healthy and Available. Available is
// advisory: the allocator decides with a conditional custody update.
func ProjectCapacity(in CapacityInputs) []metav1.Condition {
	condition := func(kind string, ok bool, reason, message string) metav1.Condition {
		status := metav1.ConditionFalse
		if ok {
			status = metav1.ConditionTrue
		}
		return metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: in.Generation}
	}
	var prepared metav1.Condition
	switch {
	case in.Commissioning == nil:
		prepared = condition(ConditionPrepared, false, "NotCommissioned", "no commissioning has succeeded")
	case in.CurrentPlanDigest == "" || in.Commissioning.PlanDigest != in.CurrentPlanDigest:
		prepared = condition(ConditionPrepared, false, "PlanChanged", "the last commissioning used a different plan")
	default:
		prepared = condition(ConditionPrepared, true, "Commissioned", "commissioning succeeded under the current plan")
	}
	var healthy metav1.Condition
	switch {
	case in.Commissioning == nil || !in.Commissioning.HealthCurrent:
		healthy = condition(ConditionHealthy, false, "CommissioningEvidenceNotCurrent", "commissioning health evidence is missing or old")
	case !in.DiscoveryCurrent:
		healthy = condition(ConditionHealthy, false, "DiscoveryNotCurrent", "discovery evidence is missing or old")
	default:
		healthy = condition(ConditionHealthy, true, "EvidenceCurrent", "commissioning and discovery evidence are current")
	}
	var available metav1.Condition
	custody := in.Custody
	switch {
	case prepared.Status != metav1.ConditionTrue:
		available = condition(ConditionAvailable, false, "NotPrepared", "the Device is not prepared")
	case healthy.Status != metav1.ConditionTrue:
		available = condition(ConditionAvailable, false, "NotHealthy", "the Device is not healthy")
	case custody == nil:
		available = condition(ConditionAvailable, false, "CustodyMissing", "the Device has no custody")
	case custody.State != ops.CustodyVerified:
		available = condition(ConditionAvailable, false, "CustodyUnverified", "custody is Unverified")
	case custody.RecoveryHold != nil:
		available = condition(ConditionAvailable, false, "RecoveryHold", "an operation outcome is unknown")
	case custody.Holder.Type != ops.HolderNone || custody.ActiveOperation != nil:
		available = condition(ConditionAvailable, false, "Held", "the holder is "+string(custody.Holder.Type))
	case !in.AllocationPolicyEnabled:
		available = condition(ConditionAvailable, false, "AllocationDisabled", "the allocation policy is not enabled")
	default:
		available = condition(ConditionAvailable, true, "Available", "the Device can be allocated")
	}
	return []metav1.Condition{prepared, healthy, available}
}
