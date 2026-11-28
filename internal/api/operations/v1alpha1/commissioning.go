package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DeviceCommissioningPlan says what commissioning must prove for the Devices
// its selector matches. Its spec is immutable; a new plan replaces it. A plan
// is qualified only while a DeviceCommissioningPlanApproval with its approval
// name, generation and digest exists and was created by someone other than
// the plan's creator.
type DeviceCommissioningPlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceCommissioningPlanSpec `json:"spec"`
}

type DeviceCommissioningPlanSpec struct {
	// Creator is the authenticated caller that wrote the plan.
	Creator Creator `json:"creator"`
	// DeviceSelector selects Devices by label. An empty selector matches
	// nothing.
	DeviceSelector metav1.LabelSelector `json:"deviceSelector"`
	// Supersedes names plans that stop being current once this plan is
	// qualified.
	Supersedes []string `json:"supersedes,omitempty"`
	// Content is what the site runs. A DeviceCommissioning carries an exact
	// copy of it.
	Content CommissioningPlanContent `json:"content"`
	// EvidenceLifetime is how long a succeeded commissioning stays current.
	EvidenceLifetime metav1.Duration `json:"evidenceLifetime"`
}

// CommissioningPlanContent is the part of a plan the site runs.
type CommissioningPlanContent struct {
	Inspection CommissioningInspection `json:"inspection"`
	Firmware   []FirmwareBaseline      `json:"firmware,omitempty"`
	BurnIn     CommissioningBurnIn     `json:"burnIn"`
}

// CommissioningInspection is the hardware the server must report. CPU and
// memory must match exactly; disks and interfaces are minimums. Every
// reported component must be healthy.
type CommissioningInspection struct {
	CPU        InspectionCPU   `json:"cpu"`
	Memory     InspectionValue `json:"memory"`
	Disks      InspectionDisks `json:"disks"`
	Interfaces InspectionCount `json:"interfaces"`
}

type InspectionCPU struct {
	Architecture string `json:"architecture"`
	Cores        int64  `json:"cores"`
}

type InspectionValue struct {
	// Capacity is a Kubernetes quantity, such as 256Gi.
	Capacity string `json:"capacity"`
}

type InspectionDisks struct {
	Count       int64  `json:"count"`
	MinCapacity string `json:"minCapacity"`
}

type InspectionCount struct {
	Count int64 `json:"count"`
}

// FirmwareBaseline is the exact version one firmware component must report.
type FirmwareBaseline struct {
	Component string `json:"component"`
	Version   string `json:"version"`
}

// CommissioningBurnIn is the burn-in image, pinned by digest, and how long it
// runs.
type CommissioningBurnIn struct {
	Image    ImageInput      `json:"image"`
	Duration metav1.Duration `json:"duration"`
}

// DeviceCommissioningPlanApproval qualifies the plan with the same name. Its
// spec is immutable; deleting it revokes the qualification.
type DeviceCommissioningPlanApproval struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceCommissioningPlanApprovalSpec `json:"spec"`
}

type DeviceCommissioningPlanApprovalSpec struct {
	// Creator is the authenticated approver. It must differ from the plan's
	// creator.
	Creator Creator       `json:"creator"`
	PlanRef PlanReference `json:"planRef"`
}

// PlanReference names a plan and pins its content with its generation and
// the digest of its spec.
type PlanReference struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
	Digest     string `json:"digest"`
}

// ApprovedPlanReference is a plan reference and the approval that qualified
// it. The approval's name is derived from the plan's UID, so it pins the plan
// incarnation: a plan recreated under the same name has another approval
// name.
type ApprovedPlanReference struct {
	PlanReference `json:",inline"`
	Approval      string `json:"approval"`
}

// DeviceCommissioning runs a qualified plan on a Device that no customer
// holds. Staff create it with a serviceRequest.
type DeviceCommissioning struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceCommissioningSpec `json:"spec"`
	Status            OperationStatus         `json:"status,omitempty"`
}

type DeviceCommissioningSpec struct {
	OperationEnvelope `json:",inline"`
	Plan              CommissioningPlanInput `json:"plan"`
}

// CommissioningPlanInput names the plan and carries an exact copy of its
// content, so the site runs exactly what admission checked.
type CommissioningPlanInput struct {
	ApprovedPlanReference `json:",inline"`
	Content               CommissioningPlanContent `json:"content"`
}

// CommissioningEvidence is the result evidence of a DeviceCommissioning. It
// never carries a credential or BMC response content.
type CommissioningEvidence struct {
	Plan ApprovedPlanReference `json:"plan"`
	// Result is the proven verdict. A commissioning whose verdict is proven
	// ends Succeeded whether the server passed or failed.
	Result CommissioningResult `json:"result"`
	// FailedPhase is the phase that failed, for a Failed result.
	FailedPhase CommissioningPhase `json:"failedPhase,omitempty"`
	// SerialNumber and SystemUUID are what the BMC reported.
	SerialNumber       string             `json:"serialNumber,omitempty"`
	SystemUUID         string             `json:"systemUUID,omitempty"`
	BMCPublicKeySHA256 string             `json:"bmcPublicKeySHA256,omitempty"`
	Hardware           *InspectedHardware `json:"hardware,omitempty"`
	Firmware           []FirmwareBaseline `json:"firmware,omitempty"`
	BurnIn             *BurnInEvidence    `json:"burnIn,omitempty"`
	Steps              []StepEvidence     `json:"steps,omitempty"`
	// Mismatches lists what failed, for a Failed result.
	Mismatches []string `json:"mismatches,omitempty"`
}

// InspectedHardware is what inspection read, in the shape of MachineCapacity
// hardware.
type InspectedHardware struct {
	CPU        InspectedCPU         `json:"cpu"`
	Memory     string               `json:"memory"`
	Disks      []InspectedDisk      `json:"disks,omitempty"`
	Interfaces []InspectedInterface `json:"interfaces,omitempty"`
}

type InspectedCPU struct {
	Architecture string `json:"architecture,omitempty"`
	Model        string `json:"model,omitempty"`
	Sockets      int64  `json:"sockets,omitempty"`
	Cores        int64  `json:"cores,omitempty"`
}

type InspectedDisk struct {
	Name     string `json:"name"`
	Capacity string `json:"capacity"`
}

type InspectedInterface struct {
	Name       string `json:"name"`
	MACAddress string `json:"macAddress,omitempty"`
	Speed      string `json:"speed,omitempty"`
}

// CommissioningResult is a commissioning's proven verdict.
type CommissioningResult string

const (
	CommissioningPassed CommissioningResult = "Passed"
	CommissioningFailed CommissioningResult = "Failed"
)

// CommissioningPhase is one phase of a commissioning.
type CommissioningPhase string

const (
	PhaseInspect  CommissioningPhase = "Inspect"
	PhaseFirmware CommissioningPhase = "Firmware"
	PhaseBurnIn   CommissioningPhase = "BurnIn"
)

// BurnInResult is what the burn-in image reported.
type BurnInResult string

const (
	BurnInPassed BurnInResult = "Passed"
	BurnInFailed BurnInResult = "Failed"
)

type BurnInEvidence struct {
	Result      BurnInResult `json:"result"`
	StartedAt   metav1.Time  `json:"startedAt"`
	CompletedAt metav1.Time  `json:"completedAt"`
}

type DeviceCommissioningPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceCommissioningPlan `json:"items"`
}

type DeviceCommissioningPlanApprovalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceCommissioningPlanApproval `json:"items"`
}

type DeviceCommissioningList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceCommissioning `json:"items"`
}

func (in *DeviceCommissioning) Envelope() *OperationEnvelope      { return &in.Spec.OperationEnvelope }
func (in *DeviceCommissioning) OperationStatus() *OperationStatus { return &in.Status }

func (in *DeviceCommissioningPlan) DeepCopyObject() runtime.Object { return deepCopy(in) }
func (in *DeviceCommissioningPlanList) DeepCopyObject() runtime.Object {
	return deepCopy(in)
}
func (in *DeviceCommissioningPlanApproval) DeepCopyObject() runtime.Object { return deepCopy(in) }
func (in *DeviceCommissioningPlanApprovalList) DeepCopyObject() runtime.Object {
	return deepCopy(in)
}
func (in *DeviceCommissioning) DeepCopyObject() runtime.Object     { return deepCopy(in) }
func (in *DeviceCommissioningList) DeepCopyObject() runtime.Object { return deepCopy(in) }

func addCommissioning(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &DeviceCommissioningPlan{}, &DeviceCommissioningPlanList{}, &DeviceCommissioningPlanApproval{}, &DeviceCommissioningPlanApprovalList{}, &DeviceCommissioning{}, &DeviceCommissioningList{})
	return nil
}
