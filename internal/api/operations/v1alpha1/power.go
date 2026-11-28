package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DevicePowerOperation powers a Device on or off, or resets it, with one
// command, as proposed in datum-cloud/fleet#173. Staff create it with a
// serviceRequest; the Fleet provider for Metal creates it with an
// allocationRef from Machine power intent.
type DevicePowerOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DevicePowerOperationSpec `json:"spec"`
	Status            OperationStatus          `json:"status,omitempty"`
}

// PowerAction is the one command a power operation sends. Each value is the
// Redfish ComputerSystem.Reset type of the same name.
type PowerAction string

const (
	PowerOn           PowerAction = "On"
	PowerForceOff     PowerAction = "ForceOff"
	PowerForceRestart PowerAction = "ForceRestart"
	PowerCycle        PowerAction = "PowerCycle"
)

// PowerActions is every action, in the order the staff menu lists them.
var PowerActions = []PowerAction{PowerOn, PowerForceOff, PowerForceRestart, PowerCycle}

// Resets reports whether the action resets a running server.
func (a PowerAction) Resets() bool { return a == PowerForceRestart || a == PowerCycle }

type DevicePowerOperationSpec struct {
	OperationEnvelope `json:",inline"`
	Action            PowerAction `json:"action"`
	// ExpectedSystemUUID is the server staff expect behind the BMC. A staff
	// operation requires it, with serviceRequest.reason; a provider
	// operation omits it.
	ExpectedSystemUUID string `json:"expectedSystemUUID,omitempty"`
	// MachineGeneration is the Machine generation whose power intent a
	// provider operation carries out. The name is derived from it, so a
	// refused operation is not created again until the generation changes.
	// A staff operation omits it.
	MachineGeneration int64 `json:"machineGeneration,omitempty"`
}

// ObservedPowerState is a Redfish ComputerSystem PowerState.
type ObservedPowerState string

const (
	PowerStateOn          ObservedPowerState = "On"
	PowerStateOff         ObservedPowerState = "Off"
	PowerStatePoweringOn  ObservedPowerState = "PoweringOn"
	PowerStatePoweringOff ObservedPowerState = "PoweringOff"
	PowerStatePaused      ObservedPowerState = "Paused"
)

// Settled reports whether the state is one a power result or a recovery can
// rest on: On or Off.
func (s ObservedPowerState) Settled() bool { return s == PowerStateOn || s == PowerStateOff }

// ResetEvidence is how the worker proved a restart or power cycle happened.
type ResetEvidence string

const (
	// ResetLastResetTime means the BMC reported a reset after the request.
	ResetLastResetTime ResetEvidence = "LastResetTime"
	// ResetObservedOff means the worker saw the server off.
	ResetObservedOff ResetEvidence = "ObservedOff"
)

// PowerEvidence is the result evidence of a DevicePowerOperation. It never
// carries a credential or BMC response content.
type PowerEvidence struct {
	// RequestSentAt is when the one command was sent. Absent if none was.
	RequestSentAt *metav1.Time `json:"requestSentAt,omitempty"`
	// Power is the state the worker read before the command and at the end.
	Power *PowerObservations `json:"power,omitempty"`
	// ResetEvidence is set for a restart or power cycle the worker proved.
	ResetEvidence ResetEvidence `json:"resetEvidence,omitempty"`
}

type PowerObservations struct {
	Before *PowerObservation `json:"before,omitempty"`
	After  *PowerObservation `json:"after,omitempty"`
}

// PowerObservation is one read of the server's power state.
type PowerObservation struct {
	State      ObservedPowerState `json:"state"`
	ObservedAt metav1.Time        `json:"observedAt"`
}

type DevicePowerOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DevicePowerOperation `json:"items"`
}

func (in *DevicePowerOperation) Envelope() *OperationEnvelope      { return &in.Spec.OperationEnvelope }
func (in *DevicePowerOperation) OperationStatus() *OperationStatus { return &in.Status }
func (in *DevicePowerOperation) DeepCopyObject() runtime.Object    { return deepCopy(in) }
func (in *DevicePowerOperationList) DeepCopyObject() runtime.Object {
	return deepCopy(in)
}

func addPower(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &DevicePowerOperation{}, &DevicePowerOperationList{})
	return nil
}
