// Package v1alpha1 holds a minimal subset of the proposed physical inventory
// API in fleet.datumapis.com: DeviceClaim, the installation plan for one slot,
// and Device, the actual asset bound to a claim.
//
// Both are operator-written records. Neither has a controller. Registering a
// Device authorizes no hardware effect and creates or changes no
// MachineCapacity. Status is reserved for future collectors and verifiers and
// has no writer.
package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "fleet.datumapis.com", Version: "v1alpha1"}

// ProvenanceManual marks a record an operator entered by hand.
const ProvenanceManual = "Manual"

// Provenance says how, by whom, and when a record's spec was written. Every
// change to a spec must advance RecordedAt.
type Provenance struct {
	Method     string      `json:"method"`
	RecordedBy string      `json:"recordedBy"`
	RecordedAt metav1.Time `json:"recordedAt"`
	// Reference is an optional, opaque pointer to where the facts came from.
	// Fleet does not interpret it.
	Reference string `json:"reference,omitempty"`
}

// DeviceClaim is the plan for one physical slot, written by the installation
// planner before hardware arrives. The slot outlives any one Device.
type DeviceClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceClaimSpec   `json:"spec"`
	Status            DeviceClaimStatus `json:"status,omitempty"`
}

type DeviceClaimSpec struct {
	Provenance       Provenance       `json:"provenance"`
	ExpectedIdentity ExpectedIdentity `json:"expectedIdentity"`
	Placement        Placement        `json:"placement"`
	Ports            []ExpectedPort   `json:"ports,omitempty"`
}

// ExpectedIdentity is the planned equipment. Serial is usually unknown at plan
// time and is optional.
type ExpectedIdentity struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model"`
	Serial       string `json:"serial,omitempty"`
}

// Placement is the planned mount: the lowest occupied rack unit, the number of
// units, and the face. Facility and Rack are names until those kinds exist.
type Placement struct {
	Facility string `json:"facility"`
	Rack     string `json:"rack"`
	Position int32  `json:"position"`
	Height   int32  `json:"height,omitempty"`
	Face     string `json:"face"`
}

// ExpectedPort is a planned physical port and the cable peer it should reach.
// Logical LAGs and VLANs are not ports.
type ExpectedPort struct {
	Name         string    `json:"name"`
	Type         string    `json:"type,omitempty"`
	SpeedMbps    int64     `json:"speedMbps,omitempty"`
	ExpectedPeer *PortPeer `json:"expectedPeer,omitempty"`
}

// PortPeer names the far end of a cable by device name and port.
type PortPeer struct {
	Device string `json:"device"`
	Port   string `json:"port"`
}

// DeviceClaimStatus is reserved for verifiers. Nothing writes it yet.
type DeviceClaimStatus struct{}

// Device is one actual asset, created by the registrar when detected hardware
// is bound to a claim. Its identity and claim binding are immutable: a hardware
// swap is a new Device.
type Device struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceSpec   `json:"spec"`
	Status            DeviceStatus `json:"status,omitempty"`
}

type DeviceSpec struct {
	ClaimRef   LocalReference `json:"claimRef"`
	Provenance Provenance     `json:"provenance"`
	Identity   DeviceIdentity `json:"identity"`
	BMC        *BMC           `json:"bmc,omitempty"`
	Interfaces []Interface    `json:"interfaces,omitempty"`
	// UnmappedMACAddresses are MACs detected on the asset that the registrar
	// could not attribute to a named interface.
	UnmappedMACAddresses []string `json:"unmappedMACAddresses,omitempty"`
}

type LocalReference struct {
	Name string `json:"name"`
}

// DeviceIdentity is the detected chassis identity. Serial and SystemUUID are
// matching evidence, not authenticators. At least one is required.
type DeviceIdentity struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model"`
	Serial       string `json:"serial,omitempty"`
	// SystemUUID is the SMBIOS system UUID in lowercase canonical form.
	SystemUUID string `json:"systemUUID,omitempty"`
}

// BMC records where the management controller was reached. Credentials never
// belong here.
type BMC struct {
	Endpoint string `json:"endpoint"`
}

type Interface struct {
	Name       string    `json:"name"`
	MACAddress string    `json:"macAddress,omitempty"`
	Peer       *PortPeer `json:"peer,omitempty"`
}

// DeviceStatus is reserved for collectors and verifiers to report drift and
// mismatch. It never carries identity. Nothing writes it yet.
type DeviceStatus struct{}

type DeviceClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceClaim `json:"items"`
}

type DeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Device `json:"items"`
}

func addKnown(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &DeviceClaim{}, &DeviceClaimList{}, &Device{}, &DeviceList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

var AddToScheme = addKnown

func (in *DeviceClaim) DeepCopyObject() runtime.Object {
	out := new(DeviceClaim)
	deepCopy(in, out)
	return out
}
func (in *DeviceClaimList) DeepCopyObject() runtime.Object {
	out := new(DeviceClaimList)
	deepCopy(in, out)
	return out
}
func (in *Device) DeepCopyObject() runtime.Object {
	out := new(Device)
	deepCopy(in, out)
	return out
}
func (in *DeviceList) DeepCopyObject() runtime.Object {
	out := new(DeviceList)
	deepCopy(in, out)
	return out
}

func deepCopy[T any](in *T, out *T) { b, _ := json.Marshal(in); _ = json.Unmarshal(b, out) }
