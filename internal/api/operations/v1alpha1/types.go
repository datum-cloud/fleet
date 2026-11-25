// Package v1alpha1 holds Device custody and the Device operation kinds in
// fleet.datumapis.com, as proposed in datum-cloud/fleet#174 at d4d17f9.
//
// DeviceCustody is a coordination record, like a Lease: its fields live in
// spec and the coordinator is their only writer. Every operation kind shares
// one envelope (OperationEnvelope) and one status (OperationStatus). The
// coordinator is the only writer of operation status. Requesters write an
// operation's spec once, at create; it is immutable afterwards.
//
// Nothing here sends anything to a BMC. These kinds live only in the provider
// project and are never served to consumer projects.
package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "fleet.datumapis.com", Version: "v1alpha1"}

// CustodyFinalizer keeps custody until its holder is Retired.
const CustodyFinalizer = "fleet.datumapis.com/custody"

// DeviceCustody names who holds one Device, the one operation running on it,
// and its fence. It has the same name and namespace as its Device.
type DeviceCustody struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceCustodySpec `json:"spec"`
}

type CustodyState string

const (
	CustodyVerified   CustodyState = "Verified"
	CustodyUnverified CustodyState = "Unverified"
)

type HolderType string

const (
	HolderNone       HolderType = "None"
	HolderAllocation HolderType = "Allocation"
	HolderRelease    HolderType = "Release"
	HolderService    HolderType = "Service"
	HolderRetired    HolderType = "Retired"
)

// ReleaseGate is one machine lifecycle release obligation. The set is closed;
// a gate the coordinator does not know stays open.
type ReleaseGate string

const (
	GateOperations ReleaseGate = "Operations"
	GateAccess     ReleaseGate = "Access"
	GateNetwork    ReleaseGate = "Network"
	GateAddress    ReleaseGate = "Address"
	GateCleaning   ReleaseGate = "Cleaning"
	GateQuota      ReleaseGate = "Quota"
)

// ReleaseGates is every gate, in the order the machine lifecycle settles them.
var ReleaseGates = []ReleaseGate{GateOperations, GateAccess, GateNetwork, GateAddress, GateCleaning, GateQuota}

type DeviceCustodySpec struct {
	DeviceRef DeviceReference `json:"deviceRef"`
	State     CustodyState    `json:"state"`
	Holder    Holder          `json:"holder"`
	// Fence rises when an operation is admitted and when custody is rebuilt.
	Fence           int64            `json:"fence"`
	ActiveOperation *ActiveOperation `json:"activeOperation,omitempty"`
	LastResult      *OperationResult `json:"lastResult,omitempty"`
	RecoveryHold    *RecoveryHold    `json:"recoveryHold,omitempty"`
	// Conflicts says why Unverified custody cannot be verified yet.
	Conflicts []string `json:"conflicts,omitempty"`
	// RejectedResults counts site results refused for a stale fence.
	RejectedResults *RejectedResults `json:"rejectedResults,omitempty"`
}

type DeviceReference struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type LocalReference struct {
	Name string `json:"name"`
}

// Holder is who holds the Device. AllocationRef is set only for Allocation
// and Release, and OpenGates only for Release.
type Holder struct {
	Type          HolderType      `json:"type"`
	AllocationRef *LocalReference `json:"allocationRef,omitempty"`
	OpenGates     []ReleaseGate   `json:"openGates,omitempty"`
}

// OperationReference names one operation incarnation.
type OperationReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// ActiveOperation is the one admitted operation. Custody retains its exact
// input digest and Device snapshot before anything is delivered, so delivery
// can be replayed from custody after a crash.
type ActiveOperation struct {
	OperationReference `json:",inline"`
	Fence              int64          `json:"fence"`
	Phase              Phase          `json:"phase"`
	AdmittedAt         metav1.Time    `json:"admittedAt"`
	Deadline           metav1.Time    `json:"deadline"`
	InputDigest        string         `json:"inputDigest"`
	Snapshot           DeviceSnapshot `json:"snapshot"`
	Creator            Creator        `json:"creator"`
	// Settles names the recovery hold this operation was admitted to settle.
	Settles *OperationReference `json:"settles,omitempty"`
}

// DeviceSnapshot is what admission captured about the Device. The site
// checks the BMC against it before it sends anything.
type DeviceSnapshot struct {
	BMCEndpoint         string `json:"bmcEndpoint"`
	SystemUUID          string `json:"systemUUID"`
	BootMACAddress      string `json:"bootMACAddress,omitempty"`
	BMCPublicKeySHA256  string `json:"bmcPublicKeySHA256"`
	DeviceGeneration    int64  `json:"deviceGeneration"`
	DiscoveryObservedAt string `json:"discoveryObservedAt"`
	// Extra holds bounded fields a kind adds at admission, such as the reset
	// types discovery offered. The worker and recovery see what admission
	// saw.
	Extra map[string]string `json:"extra,omitempty"`
}

// OperationResult is how one operation ended, with its typed evidence.
type OperationResult struct {
	OperationReference `json:",inline"`
	Fence              int64                 `json:"fence"`
	Phase              Phase                 `json:"phase"`
	Reason             string                `json:"reason,omitempty"`
	Message            string                `json:"message,omitempty"`
	FinishedAt         metav1.Time           `json:"finishedAt"`
	Evidence           *runtime.RawExtension `json:"evidence,omitempty"`
}

// RecoveryHold is an operation whose outcome is unknown. Only the settling
// operations its kind allows are admitted while it exists.
type RecoveryHold struct {
	OperationReference `json:",inline"`
	Fence              int64 `json:"fence"`
	// Since is when the held operation ended with an unknown outcome.
	Since  metav1.Time `json:"since"`
	Reason string      `json:"reason"`
	// Deadline is the held operation's deadline, when it was known.
	Deadline *metav1.Time `json:"deadline,omitempty"`
	// Snapshot is the Device snapshot the held operation was admitted with.
	Snapshot *DeviceSnapshot `json:"snapshot,omitempty"`
	// RetryAuthorizedBy names the DeviceRecovery with Retry that lets the
	// held operation's requester create one settling operation.
	RetryAuthorizedBy *OperationReference `json:"retryAuthorizedBy,omitempty"`
	// Retries counts the DeviceRecovery Retry decisions for the held
	// operation. An operation created after a Retry is named RetryName (the
	// held operation's name when empty) plus "-retry-" and this count.
	Retries int32 `json:"retries,omitempty"`
	// RetryName is the name retries are named from. It is set when a retried
	// operation itself ends unknown, so its retries keep the original
	// operation's name and count on.
	RetryName string `json:"retryName,omitempty"`
}

// RejectedResults records site results the coordinator refused because their
// fence did not match the active operation. Monitoring reads it.
type RejectedResults struct {
	Count int64          `json:"count"`
	Last  RejectedResult `json:"last"`
}

type RejectedResult struct {
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	Fence      int64       `json:"fence"`
	Site       string      `json:"site"`
	RejectedAt metav1.Time `json:"rejectedAt"`
}

// Phase is an operation phase.
type Phase string

const (
	PhasePending        Phase = "Pending"
	PhaseAdmitted       Phase = "Admitted"
	PhaseDispatched     Phase = "Dispatched"
	PhaseVerifying      Phase = "Verifying"
	PhaseSucceeded      Phase = "Succeeded"
	PhaseFailed         Phase = "Failed"
	PhaseCancelled      Phase = "Cancelled"
	PhaseOutcomeUnknown Phase = "OutcomeUnknown"
)

// Ended reports whether an operation in this phase has finished.
// OutcomeUnknown ends the operation; its hold lives on in custody.
func (p Phase) Ended() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseCancelled || p == PhaseOutcomeUnknown
}

// Running reports whether custody can name an operation in this phase as
// active.
func (p Phase) Running() bool {
	return p == PhaseAdmitted || p == PhaseDispatched || p == PhaseVerifying
}

// Creator is the authenticated caller that created an operation. A
// validating webhook rejects any other value and any change.
type Creator struct {
	Username string `json:"username"`
	UID      string `json:"uid"`
}

// Authority is why an operation may act. Exactly one field is set.
type Authority struct {
	AllocationRef  *LocalReference `json:"allocationRef,omitempty"`
	ServiceRequest *ServiceRequest `json:"serviceRequest,omitempty"`
}

// ServiceRequest is a staff request. Reason is required.
type ServiceRequest struct {
	Reason string `json:"reason"`
}

// OperationEnvelope is the spec every operation kind starts with.
type OperationEnvelope struct {
	Creator   Creator         `json:"creator"`
	DeviceRef DeviceReference `json:"deviceRef"`
	Authority Authority       `json:"authority"`
}

// OperationStatus is the status every operation kind shares. The coordinator
// copies it from custody.
type OperationStatus struct {
	Phase       Phase           `json:"phase,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	Message     string          `json:"message,omitempty"`
	Fence       int64           `json:"fence,omitempty"`
	AdmittedAt  *metav1.Time    `json:"admittedAt,omitempty"`
	Deadline    *metav1.Time    `json:"deadline,omitempty"`
	FinishedAt  *metav1.Time    `json:"finishedAt,omitempty"`
	InputDigest string          `json:"inputDigest,omitempty"`
	Snapshot    *DeviceSnapshot `json:"snapshot,omitempty"`
	// Evidence is the kind's typed result evidence, as recorded in custody.
	Evidence *runtime.RawExtension `json:"evidence,omitempty"`
}

// Operation is implemented by every operation kind.
type Operation interface {
	runtime.Object
	metav1.Object
	Envelope() *OperationEnvelope
	OperationStatus() *OperationStatus
}

// DeviceInstallation installs an image on an allocated Device. The Fleet
// provider for Metal creates it from an Installation.
type DeviceInstallation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceInstallationSpec `json:"spec"`
	Status            OperationStatus        `json:"status,omitempty"`
}

type DeviceInstallationSpec struct {
	OperationEnvelope `json:",inline"`
	// RequestUID is the UID of the Installation this fulfills. The name is
	// derived from it, so a retried create collides.
	RequestUID    string       `json:"requestUID"`
	Image         ImageInput   `json:"image"`
	Bootstrap     *SealedInput `json:"bootstrap,omitempty"`
	BootInterface string       `json:"bootInterface"`
	TargetDisks   []string     `json:"targetDisks"`
}

type ImageInput struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// SealedInput is an opaque reference into the sealed store and the digest of
// what it holds. Operations never carry the payload.
type SealedInput struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// InstallationEvidence is the result evidence of a DeviceInstallation.
type InstallationEvidence struct {
	Steps []StepEvidence `json:"steps,omitempty"`
}

// StepEvidence records one journaled step. It never carries a BMC response.
type StepEvidence struct {
	Name        string      `json:"name"`
	CompletedAt metav1.Time `json:"completedAt"`
}

// DeviceWipe cleans an allocation's disks during release. The release
// controller creates it.
type DeviceWipe struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceWipeSpec  `json:"spec"`
	Status            OperationStatus `json:"status,omitempty"`
}

type DeviceWipeSpec struct {
	OperationEnvelope `json:",inline"`
	// Step numbers the wipes of one release, from 1. The name is derived
	// from it.
	Step int32 `json:"step"`
	// CleaningPolicyDigest is the digest of the cleaning policy record
	// pinned at allocation.
	CleaningPolicyDigest string   `json:"cleaningPolicyDigest"`
	TargetDisks          []string `json:"targetDisks"`
}

// WipeEvidence is the result evidence of a DeviceWipe.
type WipeEvidence struct {
	Steps        []StepEvidence `json:"steps,omitempty"`
	WipedDisks   []string       `json:"wipedDisks,omitempty"`
	PolicyDigest string         `json:"policyDigest,omitempty"`
}

// DeviceRecovery is a staff decision about a recovery hold or about
// Unverified custody. It sends nothing to the server.
type DeviceRecovery struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceRecoverySpec `json:"spec"`
	Status            OperationStatus    `json:"status,omitempty"`
}

type RecoveryDecision string

const (
	DecisionSettle  RecoveryDecision = "Settle"
	DecisionRetry   RecoveryDecision = "Retry"
	DecisionRetire  RecoveryDecision = "Retire"
	DecisionResolve RecoveryDecision = "Resolve"
)

type DeviceRecoverySpec struct {
	OperationEnvelope `json:",inline"`
	Decision          RecoveryDecision `json:"decision"`
	// Hold names the held operation the decision is about. Required for
	// Settle, Retry and Retire under a hold.
	Hold *HeldOperationReference `json:"hold,omitempty"`
	// Evidence is what staff observed. The held kind validates it for Settle.
	Evidence RecoveryEvidence `json:"evidence"`
	// DiskDisposal is required to Retire from Release.
	DiskDisposal *DiskDisposal `json:"diskDisposal,omitempty"`
	// Resolve names the holder of Unverified custody.
	Resolve *ResolveInput `json:"resolve,omitempty"`
}

type HeldOperationReference struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Fence int64  `json:"fence"`
}

// RecoveryEvidence is bounded, typed staff evidence. Data holds the held
// kind's own evidence fields; the kind validates them.
type RecoveryEvidence struct {
	ObservedAt metav1.Time       `json:"observedAt"`
	Summary    string            `json:"summary"`
	Data       map[string]string `json:"data,omitempty"`
}

type DiskDisposalMethod string

const (
	DisposalDestroyed DiskDisposalMethod = "Destroyed"
	DisposalRemoved   DiskDisposalMethod = "Removed"
)

type DiskDisposal struct {
	Method    DiskDisposalMethod `json:"method"`
	Reference string             `json:"reference"`
}

type ResolveInput struct {
	Holder        HolderType      `json:"holder"`
	AllocationRef *LocalReference `json:"allocationRef,omitempty"`
}

// RecoveryResultEvidence is the result evidence of a DeviceRecovery: the
// decision applied and the site fence that confirmed the held worker stopped.
type RecoveryResultEvidence struct {
	Decision       RecoveryDecision `json:"decision"`
	ConfirmedFence int64            `json:"confirmedFence"`
}

func (in *DeviceInstallation) Envelope() *OperationEnvelope      { return &in.Spec.OperationEnvelope }
func (in *DeviceInstallation) OperationStatus() *OperationStatus { return &in.Status }
func (in *DeviceWipe) Envelope() *OperationEnvelope              { return &in.Spec.OperationEnvelope }
func (in *DeviceWipe) OperationStatus() *OperationStatus         { return &in.Status }
func (in *DeviceRecovery) Envelope() *OperationEnvelope          { return &in.Spec.OperationEnvelope }
func (in *DeviceRecovery) OperationStatus() *OperationStatus     { return &in.Status }

type DeviceCustodyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceCustody `json:"items"`
}

type DeviceInstallationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceInstallation `json:"items"`
}

type DeviceWipeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceWipe `json:"items"`
}

type DeviceRecoveryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceRecovery `json:"items"`
}

func addKnown(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &DeviceCustody{}, &DeviceCustodyList{}, &DeviceInstallation{}, &DeviceInstallationList{}, &DeviceWipe{}, &DeviceWipeList{}, &DeviceRecovery{}, &DeviceRecoveryList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return addCommissioning(s)
}

var AddToScheme = addKnown

func (in *DeviceCustody) DeepCopyObject() runtime.Object          { return deepCopy(in) }
func (in *DeviceCustodyList) DeepCopyObject() runtime.Object      { return deepCopy(in) }
func (in *DeviceInstallation) DeepCopyObject() runtime.Object     { return deepCopy(in) }
func (in *DeviceInstallationList) DeepCopyObject() runtime.Object { return deepCopy(in) }
func (in *DeviceWipe) DeepCopyObject() runtime.Object             { return deepCopy(in) }
func (in *DeviceWipeList) DeepCopyObject() runtime.Object         { return deepCopy(in) }
func (in *DeviceRecovery) DeepCopyObject() runtime.Object         { return deepCopy(in) }
func (in *DeviceRecoveryList) DeepCopyObject() runtime.Object     { return deepCopy(in) }

// DeepCopy returns a copy of custody.
func (in *DeviceCustody) DeepCopy() *DeviceCustody { return deepCopy(in) }

func deepCopy[T any](in *T) *T {
	out := new(T)
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, out)
	return out
}
