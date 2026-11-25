package custody

import (
	"strings"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MachineCapacity conditions the capacity projector derives. It is their only
// writer, and the allocator evaluates the same projection from live inputs
// instead of reading them.
const (
	ConditionPrepared  = "Prepared"
	ConditionHealthy   = "Healthy"
	ConditionAvailable = "Available"
)

// CleaningState is what the cleaning policy owner said about a capacity's
// cleaning policy reference.
type CleaningState string

const (
	CleaningResolved   CleaningState = "Resolved"
	CleaningUnresolved CleaningState = "Unresolved"
	// CleaningUnknown means the owner could not be reached. Available is
	// then Unknown, not False.
	CleaningUnknown CleaningState = "Unknown"
)

// LatestCommissioning is a Device's latest succeeded commissioning.
type LatestCommissioning struct {
	DeviceUID  string
	FinishedAt time.Time
	Evidence   ops.CommissioningEvidence
}

// CurrentPlanInput is the Device's current plan, or why it has none.
type CurrentPlanInput struct {
	// Plan is nil when the Device has no current plan.
	Plan *ops.DeviceCommissioningPlan
	// Digest is the plan's digest.
	Digest string
	// Reason is NoCurrentPlan or PlanAmbiguous when Plan is nil.
	Reason string
}

// DiscoveryInput is what BMC discovery last proved about the Device.
type DiscoveryInput struct {
	// Current is true when discovery matched the Device's identity for its
	// current generation, read its inventory within twice the interval, and
	// recorded the BMC key at its current endpoint.
	Current bool
	// KeyDigest is the BMC key digest discovery recorded.
	KeyDigest string
}

// CapacityInputs is everything the projector reads, all read before any
// write.
type CapacityInputs struct {
	Generation int64
	Now        time.Time
	// DeviceUID is the UID of the capacity's Device; empty when it is
	// missing.
	DeviceUID string
	// DeviceSerial and DeviceSystemUUID are the Device's recorded identity.
	DeviceSerial, DeviceSystemUUID string
	DeviceActive                   bool
	// Commissioning is nil when no commissioning has succeeded.
	Commissioning *LatestCommissioning
	CurrentPlan   CurrentPlanInput
	Discovery     DiscoveryInput
	// Custody is nil when the Device has no custody.
	Custody                 *ops.DeviceCustodySpec
	AllocationPolicyEnabled bool
	Cleaning                CleaningState
	// Duplicate is true when another Device in the provider scope shares
	// one of the Device's identity aliases.
	Duplicate bool
}

// CapacityProjection is the derived status: the three conditions and the
// inspected hardware, which is nil unless the latest commissioning passed.
type CapacityProjection struct {
	Conditions []metav1.Condition
	Hardware   *ops.InspectedHardware
}

// Available reports whether the projection offers the capacity.
func (p CapacityProjection) Available() bool {
	for _, condition := range p.Conditions {
		if condition.Type == ConditionAvailable {
			return condition.Status == metav1.ConditionTrue
		}
	}
	return false
}

// ProjectCapacity derives Prepared, Healthy and Available. Each false
// condition names the first check that failed. Available is advisory: the
// allocator decides with a conditional custody update.
func ProjectCapacity(in CapacityInputs) CapacityProjection {
	condition := func(kind string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: in.Generation}
	}
	no := func(kind, reason, message string) metav1.Condition {
		return condition(kind, metav1.ConditionFalse, reason, message)
	}
	var out CapacityProjection
	latest := in.Commissioning
	if latest != nil && (in.DeviceUID == "" || latest.DeviceUID != in.DeviceUID) {
		// Evidence for another incarnation of the Device proves nothing.
		latest = nil
	}
	var prepared metav1.Condition
	plan := in.CurrentPlan
	switch {
	case latest == nil:
		prepared = no(ConditionPrepared, "NotCommissioned", "no commissioning has succeeded")
	case latest.Evidence.Result != ops.CommissioningPassed:
		prepared = no(ConditionPrepared, "CommissioningFailed", "the latest commissioning failed in its "+string(latest.Evidence.FailedPhase)+" phase")
	case plan.Plan == nil && plan.Reason == ReasonPlanAmbiguous:
		prepared = no(ConditionPrepared, ReasonPlanAmbiguous, "more than one plan applies to the Device")
	case plan.Plan == nil:
		prepared = no(ConditionPrepared, ReasonNoCurrentPlan, "no qualified plan applies to the Device")
	case latest.Evidence.Plan.UID != string(plan.Plan.UID) || latest.Evidence.Plan.Digest != plan.Digest:
		prepared = no(ConditionPrepared, "PlanChanged", "the latest commissioning used another plan than the current one")
	default:
		prepared = condition(ConditionPrepared, metav1.ConditionTrue, "Commissioned", "the server passed commissioning under the current plan")
		out.Hardware = latest.Evidence.Hardware
	}
	var healthy metav1.Condition
	switch {
	case prepared.Status != metav1.ConditionTrue:
		healthy = no(ConditionHealthy, "NotPrepared", "the server is not prepared")
	case in.Now.After(latest.FinishedAt.Add(plan.Plan.Spec.EvidenceLifetime.Duration)):
		healthy = no(ConditionHealthy, "EvidenceExpired", "the commissioning is older than the plan's evidence lifetime")
	case !in.Discovery.Current:
		healthy = no(ConditionHealthy, "DiscoveryNotCurrent", "discovery evidence is missing or old")
	case !strings.EqualFold(strings.TrimSpace(in.DeviceSerial), latest.Evidence.SerialNumber) || !strings.EqualFold(in.DeviceSystemUUID, latest.Evidence.SystemUUID) || in.Discovery.KeyDigest != latest.Evidence.BMCPublicKeySHA256:
		healthy = no(ConditionHealthy, "IdentityChanged", "the Device's identity or BMC key differs from what commissioning recorded")
	default:
		healthy = condition(ConditionHealthy, metav1.ConditionTrue, "EvidenceCurrent", "commissioning and discovery evidence are current")
	}
	var available metav1.Condition
	custody := in.Custody
	switch {
	case healthy.Status != metav1.ConditionTrue:
		available = no(ConditionAvailable, "NotHealthy", "the server is not healthy")
	case custody == nil:
		available = no(ConditionAvailable, "CustodyMissing", "the Device has no custody")
	case custody.State != ops.CustodyVerified:
		available = no(ConditionAvailable, "CustodyUnverified", "custody is Unverified")
	case custody.RecoveryHold != nil:
		available = no(ConditionAvailable, "RecoveryHold", "an operation outcome is unknown")
	case custody.Holder.Type != ops.HolderNone || custody.ActiveOperation != nil:
		available = no(ConditionAvailable, "Held", "the holder is "+string(custody.Holder.Type))
	case !in.AllocationPolicyEnabled:
		available = no(ConditionAvailable, "AllocationDisabled", "the allocation policy is not enabled")
	case !in.DeviceActive:
		available = no(ConditionAvailable, "DeviceNotActive", "the Device is not Active")
	case in.Duplicate:
		available = no(ConditionAvailable, "IdentityConflict", "another Device shares an identity alias")
	case in.Cleaning == CleaningUnknown:
		available = condition(ConditionAvailable, metav1.ConditionUnknown, "CleaningPolicyUnknown", "the cleaning policy owner could not be reached")
	case in.Cleaning != CleaningResolved:
		available = no(ConditionAvailable, "CleaningPolicyUnresolved", "the cleaning policy reference does not resolve")
	default:
		available = condition(ConditionAvailable, metav1.ConditionTrue, "Available", "the server can be allocated")
	}
	out.Conditions = []metav1.Condition{prepared, healthy, available}
	return out
}
