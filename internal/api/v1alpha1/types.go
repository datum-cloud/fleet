package v1alpha1

import (
	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "fleet.datumapis.com", Version: "v1alpha1"}

type ConditionedStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type MachineClassSpec struct {
	Source        *SourceReference `json:"source,omitempty"`
	ProfilePolicy *ProfilePolicy   `json:"profilePolicy,omitempty"`
	Access        *AccessPolicy    `json:"access,omitempty"`
	Capacity      *CapacityPolicy  `json:"capacity,omitempty"`
}
type AccessPolicy struct {
	ConsoleModes []string `json:"consoleModes,omitempty"`
	PowerControl bool     `json:"powerControl,omitempty"`
}
type SourceReference struct {
	Project string `json:"project"`
	Name    string `json:"name"`
}
type ProfilePolicy struct {
	AllowedProfiles []LocalReference    `json:"allowedProfiles,omitempty"`
	CustomProfiles  CustomProfilePolicy `json:"customProfiles,omitempty"`
}
type CustomProfilePolicy struct {
	Enabled bool `json:"enabled,omitempty"`
}
type CapacityPolicy struct {
	Machines         MachineSelector           `json:"machines"`
	ProviderClassRef *TypedNamespacedReference `json:"providerClassRef,omitempty"`
}
type MachineSelector struct {
	Namespace string               `json:"namespace"`
	Selector  metav1.LabelSelector `json:"selector,omitempty"`
}
type LocalReference struct {
	Name string `json:"name"`
}

type TypedLocalReference struct {
	APIGroup string `json:"apiGroup"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}
type TypedNamespacedReference struct {
	APIGroup  string `json:"apiGroup"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type MachineClassStatus struct {
	EvaluatedRevision string             `json:"evaluatedRevision,omitempty"`
	ResolvedClass     *ResolvedClass     `json:"resolvedClass,omitempty"`
	Conditions        []metav1.Condition `json:"conditions,omitempty"`
}
type ResolvedClass struct {
	ProfilePolicy ProfilePolicy `json:"profilePolicy,omitempty"`
	Access        AccessPolicy  `json:"access,omitempty"`
}
type MachineClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineClassSpec   `json:"spec,omitempty"`
	Status            MachineClassStatus `json:"status,omitempty"`
}
type MachineClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineClass `json:"items"`
}

type Constraint struct {
	Min   *int64 `json:"min,omitempty"`
	Max   *int64 `json:"max,omitempty"`
	Exact *int64 `json:"exact,omitempty"`
}
type QuantityConstraint struct {
	Min   string `json:"min,omitempty"`
	Max   string `json:"max,omitempty"`
	Exact string `json:"exact,omitempty"`
}
type CPURequirement struct {
	Architecture string     `json:"architecture"`
	Cores        Constraint `json:"cores,omitempty"`
}
type MemoryRequirement struct {
	Capacity QuantityConstraint `json:"capacity"`
}
type MachineProfileSpec struct {
	CPU        CPURequirement         `json:"cpu"`
	Memory     MemoryRequirement      `json:"memory"`
	GPUs       *GPURequirement        `json:"gpus,omitempty"`
	Disks      []DiskRequirement      `json:"disks,omitempty"`
	Interfaces []InterfaceRequirement `json:"interfaces,omitempty"`
}
type GPURequirement struct {
	Count           Constraint         `json:"count"`
	Vendor          string             `json:"vendor,omitempty"`
	Models          []string           `json:"models,omitempty"`
	MemoryPerDevice QuantityConstraint `json:"memoryPerDevice,omitempty"`
}
type DiskRequirement struct {
	Name      string             `json:"name"`
	Count     Constraint         `json:"count"`
	Capacity  QuantityConstraint `json:"capacity"`
	Media     string             `json:"media,omitempty"`
	Interface string             `json:"interface,omitempty"`
}
type InterfaceRequirement struct {
	Name  string             `json:"name"`
	Type  string             `json:"type,omitempty"`
	Speed QuantityConstraint `json:"speed,omitempty"`
}
type MachineProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineProfileSpec `json:"spec"`
	Status            ConditionedStatus  `json:"status,omitempty"`
}
type MachineProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineProfile `json:"items"`
}

type Hardware struct {
	CPU        HardwareCPU         `json:"cpu,omitempty"`
	Memory     HardwareMemory      `json:"memory,omitempty"`
	GPUs       []HardwareGPU       `json:"gpus,omitempty"`
	Disks      []HardwareDisk      `json:"disks,omitempty"`
	Interfaces []HardwareInterface `json:"interfaces,omitempty"`
}
type HardwareCPU struct {
	Architecture string `json:"architecture,omitempty"`
	Model        string `json:"model,omitempty"`
	Sockets      int64  `json:"sockets,omitempty"`
	Cores        int64  `json:"cores,omitempty"`
	Threads      int64  `json:"threads,omitempty"`
}
type HardwareMemory struct {
	Capacity string `json:"capacity,omitempty"`
}
type HardwareGPU struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor,omitempty"`
	Model  string `json:"model,omitempty"`
	Memory string `json:"memory,omitempty"`
}
type HardwareDisk struct {
	Name      string `json:"name"`
	Capacity  string `json:"capacity,omitempty"`
	Media     string `json:"media,omitempty"`
	Interface string `json:"interface,omitempty"`
}
type HardwareInterface struct {
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Speed string `json:"speed,omitempty"`
}
type MachineSpec struct {
	ProviderRef       TypedLocalReference `json:"providerRef"`
	DeviceRef         *LocalReference     `json:"deviceRef,omitempty"`
	AllocationPolicy  string              `json:"allocationPolicy"`
	CleaningPolicyRef LocalReference      `json:"cleaningPolicyRef"`
	ReclaimPolicy     string              `json:"reclaimPolicy"`
}
type MachineStatus struct {
	Hardware   Hardware           `json:"hardware,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
type Machine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineSpec   `json:"spec"`
	Status            MachineStatus `json:"status,omitempty"`
}
type MachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Machine `json:"items"`
}

type MachineClaimSpec struct {
	MachineClassRef        LocalReference            `json:"machineClassRef"`
	MachineProfileRef      LocalReference            `json:"machineProfileRef"`
	LocationRef            LocalReference            `json:"locationRef"`
	InitialInstallationRef *LocalReference           `json:"initialInstallationRef,omitempty"`
	NetworkInterfaces      []NetworkInterfaceRequest `json:"networkInterfaces,omitempty"`
	PowerState             string                    `json:"powerState,omitempty"`
}
type NamespacedReference struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}
type NetworkInterfaceRequest struct {
	Name          string              `json:"name"`
	Network       NamespacedReference `json:"network"`
	IPFamilies    []string            `json:"ipFamilies,omitempty"`
	Addresses     []AddressRequest    `json:"addresses,omitempty"`
	ReclaimPolicy string              `json:"reclaimPolicy,omitempty"`
}
type AddressRequest struct {
	ClassRef LocalReference `json:"classRef"`
}
type NetworkInterfaceStatus struct {
	Name              string                      `json:"name"`
	Network           NetworkReference            `json:"network"`
	MACAddress        string                      `json:"macAddress,omitempty"`
	MTU               int32                       `json:"mtu,omitempty"`
	Addresses         []AddressAssignment         `json:"addresses,omitempty"`
	ExternalAddresses []ExternalAddressAssignment `json:"externalAddresses,omitempty"`
	Routes            []NetworkRoute              `json:"routes,omitempty"`
	DNS               *NetworkDNS                 `json:"dns,omitempty"`
	Conditions        []metav1.Condition          `json:"conditions,omitempty"`
}
type NetworkReference struct {
	APIGroup  string `json:"apiGroup"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}
type NetworkRoute struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
}
type NetworkDNS struct {
	Servers       []string `json:"servers,omitempty"`
	SearchDomains []string `json:"searchDomains,omitempty"`
}
type AddressAssignment struct {
	Address string `json:"address"`
	Family  string `json:"family"`
	Gateway string `json:"gateway,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}
type ExternalAddressAssignment struct {
	Address  string         `json:"address"`
	Family   string         `json:"family"`
	ClassRef LocalReference `json:"classRef"`
}
type Placement struct {
	LocationRef LocalReference `json:"locationRef"`
}
type AllocatedHardwareStatus struct {
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
	Hardware   Hardware     `json:"hardware"`
}
type NetworkStatus struct {
	Revision   string                   `json:"revision,omitempty"`
	Interfaces []NetworkInterfaceStatus `json:"interfaces,omitempty"`
}
type PowerStatus struct {
	State string `json:"state"`
}
type MachineClaimStatus struct {
	Phase                  string                   `json:"phase,omitempty"`
	Placement              *Placement               `json:"placement,omitempty"`
	AcceptedProfileRef     *LocalReference          `json:"acceptedProfileRef,omitempty"`
	AllocatedHardware      *AllocatedHardwareStatus `json:"allocatedHardware,omitempty"`
	CurrentInstallationRef *LocalReference          `json:"currentInstallationRef,omitempty"`
	InstallationRevision   string                   `json:"installationRevision,omitempty"`
	Network                *NetworkStatus           `json:"network,omitempty"`
	Power                  *PowerStatus             `json:"power,omitempty"`
	Conditions             []metav1.Condition       `json:"conditions,omitempty"`
}
type MachineClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineClaimSpec   `json:"spec"`
	Status            MachineClaimStatus `json:"status,omitempty"`
}
type MachineClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineClaim `json:"items"`
}

type ClaimReference struct {
	Name string `json:"name"`
}
type InstallationPredecessor struct {
	Type     string `json:"type"`
	Revision string `json:"revision,omitempty"`
}
type InstallationSpec struct {
	ClaimRef            ClaimReference          `json:"claimRef"`
	ExpectedPredecessor InstallationPredecessor `json:"expectedPredecessor"`
	ImageRef            LocalReference          `json:"imageRef"`
	BootstrapConfigRef  *LocalReference         `json:"bootstrapConfigRef,omitempty"`
}
type InstallationStatus struct {
	Phase                string             `json:"phase,omitempty"`
	InstallationRevision string             `json:"installationRevision,omitempty"`
	CompletedAt          *metav1.Time       `json:"completedAt,omitempty"`
	Conditions           []metav1.Condition `json:"conditions,omitempty"`
}
type Installation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InstallationSpec   `json:"spec"`
	Status            InstallationStatus `json:"status,omitempty"`
}
type InstallationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Installation `json:"items"`
}

func addKnown(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &MachineClass{}, &MachineClassList{}, &MachineProfile{}, &MachineProfileList{}, &Machine{}, &MachineList{}, &MachineClaim{}, &MachineClaimList{}, &Installation{}, &InstallationList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

var AddToScheme = addKnown

func (in *MachineClass) DeepCopyObject() runtime.Object {
	out := new(MachineClass)
	deepCopy(in, out)
	return out
}
func (in *MachineClassList) DeepCopyObject() runtime.Object {
	out := new(MachineClassList)
	deepCopy(in, out)
	return out
}
func (in *MachineProfile) DeepCopyObject() runtime.Object {
	out := new(MachineProfile)
	deepCopy(in, out)
	return out
}
func (in *MachineProfileList) DeepCopyObject() runtime.Object {
	out := new(MachineProfileList)
	deepCopy(in, out)
	return out
}
func (in *Machine) DeepCopyObject() runtime.Object {
	out := new(Machine)
	deepCopy(in, out)
	return out
}
func (in *MachineList) DeepCopyObject() runtime.Object {
	out := new(MachineList)
	deepCopy(in, out)
	return out
}
func (in *MachineClaim) DeepCopyObject() runtime.Object {
	out := new(MachineClaim)
	deepCopy(in, out)
	return out
}
func (in *MachineClaimList) DeepCopyObject() runtime.Object {
	out := new(MachineClaimList)
	deepCopy(in, out)
	return out
}
func (in *Installation) DeepCopyObject() runtime.Object {
	out := new(Installation)
	deepCopy(in, out)
	return out
}
func (in *InstallationList) DeepCopyObject() runtime.Object {
	out := new(InstallationList)
	deepCopy(in, out)
	return out
}

func deepCopy[T any](in *T, out *T) { b, _ := json.Marshal(in); _ = json.Unmarshal(b, out) }
