// Package v1alpha1 holds the six observational physical inventory kinds in
// fleet.datumapis.com. Legacy slot claims retain their historical bindings;
// new typed claims describe individual installations without accepting them.
//
// All are operator-written records. None has a controller. Registering a
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

// DeviceClaim retains legacy slot plans and typed observational installations.
type DeviceClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceClaimSpec   `json:"spec"`
	Status            DeviceClaimStatus `json:"status,omitempty"`
}

type DeviceClaimSpec struct {
	DisplayName      string           `json:"displayName,omitempty"`
	Lifecycle        string           `json:"lifecycle,omitempty"`
	CableRefs        []LocalReference `json:"cableRefs,omitempty"`
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

// Placement retains legacy text placement or an explicit typed scope.
type Placement struct {
	Scope       string          `json:"scope,omitempty"`
	FacilityRef *LocalReference `json:"facilityRef,omitempty"`
	RackRef     *LocalReference `json:"rackRef,omitempty"`
	Mount       string          `json:"mount,omitempty"`
	Facility    string          `json:"facility,omitempty"`
	Rack        string          `json:"rack,omitempty"`
	Position    int32           `json:"position,omitempty"`
	Height      int32           `json:"height,omitempty"`
	Face        string          `json:"face,omitempty"`
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
	ReportedPlacement   *ReportedPlacement   `json:"reportedPlacement,omitempty"`
	DisplayName         string               `json:"displayName,omitempty"`
	ModelShape          *ModelShape          `json:"modelShape,omitempty"`
	AssetTag            string               `json:"assetTag,omitempty"`
	ProviderHostRef     *ExternalReference   `json:"providerHostRef,omitempty"`
	Components          []Component          `json:"components,omitempty"`
	InternalMappings    []InternalMapping    `json:"internalMappings,omitempty"`
	ServiceAssociations []ServiceAssociation `json:"serviceAssociations,omitempty"`
	TopologyAuthority   string               `json:"topologyAuthority,omitempty"`
	ClaimRef            LocalReference       `json:"claimRef"`
	Identity            DeviceIdentity       `json:"identity"`
	BMC                 *BMC                 `json:"bmc,omitempty"`
	Interfaces          []Interface          `json:"interfaces,omitempty"`
	// UnmappedMACAddresses are MACs detected on the asset that the registrar
	// could not attribute to a named interface.
	UnmappedMACAddresses []string `json:"unmappedMACAddresses,omitempty"`
}

type ReportedPlacement struct {
	Revision   int32       `json:"revision"`
	ObservedAt metav1.Time `json:"observedAt"`
	// Reason and Reference explain and evidence the report. Both are required.
	Reason      string          `json:"reason"`
	Reference   string          `json:"reference"`
	FacilityRef *LocalReference `json:"facilityRef,omitempty"`
	RackRef     *LocalReference `json:"rackRef,omitempty"`
	Position    *float64        `json:"position,omitempty"`
	Height      *float64        `json:"height,omitempty"`
	Face        string          `json:"face,omitempty"`
	FullDepth   *bool           `json:"fullDepth,omitempty"`
	Mount       string          `json:"mount,omitempty"`
}

type LocalReference struct {
	Name string `json:"name"`
}

// DeviceIdentity is the detected chassis identity. Serial and SystemUUID are
// matching evidence, not authenticators. A Device carries at least one of
// them, or sets Unidentified with neither.
type DeviceIdentity struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model"`
	Serial       string `json:"serial,omitempty"`
	// SystemUUID is the SMBIOS system UUID in lowercase canonical form.
	SystemUUID string `json:"systemUUID,omitempty"`
	// Unidentified records deliberately that the equipment has no serial or
	// system UUID on record, such as a patch panel. It may only be true, needs
	// UnidentifiedReason, and excludes Serial and SystemUUID. Like the rest of
	// identity it is immutable: recording a serial later is a new Device. An
	// unidentified Device cannot back MachineCapacity.
	Unidentified       bool   `json:"unidentified,omitempty"`
	UnidentifiedReason string `json:"unidentifiedReason,omitempty"`
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
	s.AddKnownTypes(GroupVersion, &Site{}, &SiteList{}, &DeviceClaim{}, &DeviceClaimList{}, &Device{}, &DeviceList{}, &Facility{}, &FacilityList{}, &Rack{}, &RackList{}, &Cable{}, &CableList{}, &PowerFeed{}, &PowerFeedList{})
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
