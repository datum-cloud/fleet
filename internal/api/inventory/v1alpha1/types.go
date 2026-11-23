// Package v1alpha1 holds the six observational physical inventory kinds in
// fleet.datumapis.com. A DeviceClaim describes one planned installation by
// typed placement without accepting it.
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

// DeviceClaim records one typed observational installation.
type DeviceClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceClaimSpec   `json:"spec"`
	Status            DeviceClaimStatus `json:"status,omitempty"`
}

type DeviceClaimSpec struct {
	DisplayName string `json:"displayName,omitempty"`
	// Lifecycle is required and is always ClaimInstallation.
	Lifecycle        string           `json:"lifecycle"`
	CableRefs        []LocalReference `json:"cableRefs,omitempty"`
	ExpectedIdentity ExpectedIdentity `json:"expectedIdentity"`
	Placement        Placement        `json:"placement"`
}

// ClaimInstallation is the only DeviceClaim lifecycle.
const ClaimInstallation = "Installation"

// ExpectedIdentity is the planned equipment. Serial is usually unknown at plan
// time and is optional.
type ExpectedIdentity struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model"`
	Serial       string `json:"serial,omitempty"`
}

// Placement is the typed installation scope. Facility scope names a
// FacilityRef; the rack scopes name a RackRef. RackUnits also needs Position,
// Height and Face, and NonRackUnitMount needs Mount. Placement is immutable.
type Placement struct {
	Scope       string          `json:"scope"`
	FacilityRef *LocalReference `json:"facilityRef,omitempty"`
	RackRef     *LocalReference `json:"rackRef,omitempty"`
	Mount       string          `json:"mount,omitempty"`
	Position    int32           `json:"position,omitempty"`
	Height      int32           `json:"height,omitempty"`
	// Face is the mounted rack face: the rack face the chassis front points
	// toward. See ReportedPlacement.Face.
	Face string `json:"face,omitempty"`
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
	DeviceTypeRef       *LocalReference      `json:"deviceTypeRef,omitempty"`
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
	// Lifecycle is the asset's service state. The CRD defaults it to Active and
	// allows Active and Decommissioning to move between each other and
	// Decommissioning to move to Retired, which is terminal. Only an Active
	// Device can back new capacity.
	Lifecycle DeviceLifecycle `json:"lifecycle,omitempty"`
}

// DeviceLifecycle is the service state of a Device.
type DeviceLifecycle string

const (
	DeviceActive          DeviceLifecycle = "Active"
	DeviceDecommissioning DeviceLifecycle = "Decommissioning"
	DeviceRetired         DeviceLifecycle = "Retired"
)

// LifecycleOf returns the Device's lifecycle, treating an unset value as
// Active, which is the CRD default.
func LifecycleOf(device *Device) DeviceLifecycle {
	if device.Spec.Lifecycle == "" {
		return DeviceActive
	}
	return device.Spec.Lifecycle
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
	// Face is the mounted rack face, front or rear: the rack face the
	// chassis front points toward, as NetBox records it. A half-depth device
	// sits on that face's rails and occupies only that face; a full-depth
	// device occupies both faces. A switch mounted reversed, with its ports
	// toward the rack rear, has face rear when its ports are on the chassis
	// front. A chassis-relative component face maps to a rack face through
	// this value: Front lands on Face, Rear on the opposite face.
	Face string `json:"face,omitempty"`
	// FullDepth is the reported depth for this installation. When unset,
	// readers fall back to the Device's and then the DeviceType's model
	// shape. A depth still unknown is counted as full depth for occupancy
	// and conflicts only, and is labelled unknown; it is never defaulted.
	FullDepth *bool  `json:"fullDepth,omitempty"`
	Mount     string `json:"mount,omitempty"`
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

// Interface names a detected network interface and its MAC address. Cable
// records carry what it connects to.
type Interface struct {
	Name       string `json:"name"`
	MACAddress string `json:"macAddress,omitempty"`
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
	s.AddKnownTypes(GroupVersion, &Manufacturer{}, &ManufacturerList{}, &DeviceType{}, &DeviceTypeList{})
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
