package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DevicePowerOperation asks for one power action on one Device in the same
// namespace. Staff create it; the whole spec is fixed at creation, and a
// changed request is a new operation. The site dispatcher alone writes its
// status, and sends at most one command for it through a worker. See
// docs/operations/device-power.md and the proposal in
// https://github.com/datum-cloud/fleet/pull/173.
type DevicePowerOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DevicePowerOperationSpec   `json:"spec"`
	Status            DevicePowerOperationStatus `json:"status,omitempty"`
}

type DevicePowerOperationSpec struct {
	// DeviceRef names the Device to act on, in the same namespace.
	DeviceRef LocalReference `json:"deviceRef"`
	// Action is one of On, ForceOff, ForceRestart or PowerCycle.
	Action PowerAction `json:"action"`
	// ExpectedSystemUUID is the server staff expect behind the BMC, in
	// lowercase canonical form. The operation fails if the Device record or
	// the BMC reports another.
	ExpectedSystemUUID string `json:"expectedSystemUUID"`
	// Requester is the user who created the operation. Admission requires it
	// to equal the authenticated username of the create request.
	Requester string `json:"requester"`
	// Reason is free text for the audit record.
	Reason string `json:"reason"`
}

// PowerAction is one ComputerSystem.Reset type Fleet sends for staff.
// Graceful actions are excluded.
type PowerAction string

const (
	PowerOn           PowerAction = "On"
	PowerForceOff     PowerAction = "ForceOff"
	PowerForceRestart PowerAction = "ForceRestart"
	PowerCycle        PowerAction = "PowerCycle"
)

// PowerResult is set once and never changed.
type PowerResult string

const (
	// PowerSucceeded means Fleet observed the requested state, or observed
	// the reset.
	PowerSucceeded PowerResult = "Succeeded"
	// PowerFailed means Fleet knows nothing was sent, or the BMC refused the
	// command and the power did not change.
	PowerFailed PowerResult = "Failed"
	// PowerUnknown means a command may have been sent and Fleet cannot prove
	// the outcome. Nothing retries it.
	PowerUnknown PowerResult = "Unknown"
)

// DevicePowerOperationStatus is written only by the site dispatcher. It never
// carries a credential or any BMC response content.
type DevicePowerOperationStatus struct {
	// Conditions are Accepted, Executing and Finished.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Result is Succeeded, Failed or Unknown, set once.
	Result PowerResult `json:"result,omitempty"`
	// RequestSentAt is when the one command was sent. It is absent when none
	// was sent.
	RequestSentAt *metav1.Time `json:"requestSentAt,omitempty"`
	// Power is the power state seen before and after the command.
	Power *PowerObservations `json:"power,omitempty"`
	// FinishedAt is when the result was set.
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

type PowerObservations struct {
	Before *PowerObservation `json:"before,omitempty"`
	After  *PowerObservation `json:"after,omitempty"`
}

// PowerObservation is one read of a System's PowerState.
type PowerObservation struct {
	State      string      `json:"state"`
	ObservedAt metav1.Time `json:"observedAt"`
}

// DevicePowerOperation condition types.
const (
	ConditionPowerAccepted  = "Accepted"
	ConditionPowerExecuting = "Executing"
	ConditionPowerFinished  = "Finished"
)

// Reasons the dispatcher refuses an operation at acceptance. Each ends the
// operation as Failed with nothing sent.
const (
	ReasonCapacityBacked        = "CapacityBacked"
	ReasonOperationInProgress   = "OperationInProgress"
	ReasonDeviceNotFound        = "DeviceNotFound"
	ReasonDeviceRetired         = "DeviceRetired"
	ReasonSystemUUIDNotRecorded = "SystemUUIDNotRecorded"
	ReasonSystemUUIDMismatch    = "SystemUUIDMismatch"
	ReasonDiscoveryNotCurrent   = "DiscoveryNotCurrent"
	ReasonSystemNotObserved     = "SystemNotObserved"
	ReasonSharedPublicKey       = "SharedPublicKey"
	ReasonResetTypeNotOffered   = "ResetTypeNotOffered"
	ReasonExpired               = "Expired"
)

type DevicePowerOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DevicePowerOperation `json:"items"`
}

func (in *DevicePowerOperation) DeepCopyObject() runtime.Object {
	out := new(DevicePowerOperation)
	deepCopy(in, out)
	return out
}

func (in *DevicePowerOperationList) DeepCopyObject() runtime.Object {
	out := new(DevicePowerOperationList)
	deepCopy(in, out)
	return out
}
