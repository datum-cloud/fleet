package v1alpha1

import (
	"encoding/json"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "metal.datumapis.com", Version: "v1alpha1"}

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

// SourceReference names the provider class a published class copies: the
// provider that publishes it, as a typed reference, and the class's name.
type SourceReference struct {
	PublisherRef TypedReference `json:"publisherRef"`
	Name         string         `json:"name"`
}
type ProfilePolicy struct {
	AllowedProfiles []LocalReference    `json:"allowedProfiles,omitempty"`
	CustomProfiles  CustomProfilePolicy `json:"customProfiles,omitempty"`
}
type CustomProfilePolicy struct {
	Enabled bool `json:"enabled,omitempty"`
}
type CapacityPolicy struct {
	MachineCapacities MachineSelector `json:"machineCapacities"`
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

// CPURequirement requests an exact CPU shape. Cores counts physical cores
// across all sockets; ranges and minimums are intentionally not expressible.
type CPURequirement struct {
	Architecture string `json:"architecture"`
	Cores        int64  `json:"cores"`
}

// MemoryRequirement requests an exact installed RAM capacity as a positive
// integer byte quantity such as "32Gi". Ranges are intentionally not
// expressible.
type MemoryRequirement struct {
	Capacity string `json:"capacity"`
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
type MachineCapacitySpec struct {
	DeviceRef         LocalReference `json:"deviceRef"`
	AllocationPolicy  string         `json:"allocationPolicy"`
	CleaningPolicyRef LocalReference `json:"cleaningPolicyRef"`
	ReclaimPolicy     string         `json:"reclaimPolicy"`
}
type MachineCapacityStatus struct {
	Hardware   Hardware           `json:"hardware,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
type MachineCapacity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineCapacitySpec   `json:"spec"`
	Status            MachineCapacityStatus `json:"status,omitempty"`
}
type MachineCapacityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineCapacity `json:"items"`
}

type MachineSpec struct {
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

// RequestedHardwareStatus is the controller-maintained snapshot of the exact
// CPU and memory resolved from the Machine's MachineProfile. It is written once,
// when the request is accepted, and is then pinned: later profile changes,
// deletion, or recreation never rewrite it. It describes the request, not the
// assigned hardware; see AllocatedHardwareStatus for the latter.
type RequestedHardwareStatus struct {
	CPU    CPURequirement          `json:"cpu"`
	Memory MemoryRequirement       `json:"memory"`
	Source RequestedHardwareSource `json:"source"`
}

// RequestedHardwareSource records the exact inputs the snapshot was resolved
// from. Names, not object UIDs, are exposed; the digest distinguishes profile
// contents across same-name recreation.
type RequestedHardwareSource struct {
	// MachineGeneration is the Machine metadata.generation at resolution.
	MachineGeneration int64 `json:"machineGeneration"`
	// ProfileRef names the MachineProfile that was resolved.
	ProfileRef LocalReference `json:"profileRef"`
	// ProfileGeneration is the MachineProfile metadata.generation at resolution.
	ProfileGeneration int64 `json:"profileGeneration"`
	// ProfileDigest is "sha256:<hex>" over the complete resolved profile spec.
	ProfileDigest string `json:"profileDigest"`
}

type MachineStatus struct {
	Phase                  string                   `json:"phase,omitempty"`
	Placement              *Placement               `json:"placement,omitempty"`
	AcceptedProfileRef     *LocalReference          `json:"acceptedProfileRef,omitempty"`
	RequestedHardware      *RequestedHardwareStatus `json:"requestedHardware,omitempty"`
	AllocatedHardware      *AllocatedHardwareStatus `json:"allocatedHardware,omitempty"`
	CurrentInstallationRef *LocalReference          `json:"currentInstallationRef,omitempty"`
	Network                *NetworkStatus           `json:"network,omitempty"`
	Power                  *PowerStatus             `json:"power,omitempty"`
	Conditions             []metav1.Condition       `json:"conditions,omitempty"`
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

type MachineReference struct {
	Name string `json:"name"`
}
type InstallationPredecessor struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}
type InstallationSpec struct {
	MachineRef          MachineReference        `json:"machineRef"`
	ExpectedPredecessor InstallationPredecessor `json:"expectedPredecessor"`
	ImageRef            LocalReference          `json:"imageRef"`
	BootstrapConfigRef  *LocalReference         `json:"bootstrapConfigRef,omitempty"`
}
type InstallationStatus struct {
	Phase       string             `json:"phase,omitempty"`
	CompletedAt *metav1.Time       `json:"completedAt,omitempty"`
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
}

// MachineAllocation is a protected, service-owned record in the provider
// project, in the namespace of its Device. It holds an allocation's private
// facts: which Machine holds which Device, the contract Fleet accepted, the
// hardware it saw, and the authority it acted on. Nothing secret is on it.
// Its name derives from the Machine's UID, so each Machine gets one
// allocation. Fleet writes the spec once and the API server keeps it
// immutable; status carries the lifecycle, and release is terminal.
type MachineAllocationSpec struct {
	// ConsumerRef is the consumer the Machine belongs to: a Milo Project, or
	// a Namespace in standalone mode. It stays in spec because release needs
	// it after the consumer is deleted.
	ConsumerRef        TypedReference            `json:"consumerRef"`
	MachineRef         AllocatedMachineReference `json:"machineRef"`
	MachineCapacityRef NamespacedReference       `json:"machineCapacityRef"`
	DeviceRef          AllocatedDeviceReference  `json:"deviceRef"`
	Accepted           AcceptedContract          `json:"accepted"`
	// Hardware is the capacity's hardware when Fleet allocated it.
	Hardware  Hardware            `json:"hardware"`
	Authority AllocationAuthority `json:"authority"`
	// QuotaClaimRef names the quota claim Fleet reserved before the
	// allocation, in the Machine's namespace. It is absent where no quota
	// system serves the consumer.
	QuotaClaimRef *LocalReference `json:"quotaClaimRef,omitempty"`
	// CleaningPolicy is the copy of the capacity's cleaning policy the
	// release wipes under. It is pinned at allocation.
	CleaningPolicy ops.PinnedCleaningPolicy `json:"cleaningPolicy"`
	// Drives is the media manifest: every drive the server had when Fleet
	// allocated it, named for this allocation and matched to a rule of the
	// pinned policy. Release cleans every one of them.
	Drives []ops.ManifestDrive `json:"drives"`
	// BootInterface and TargetDisks are what installations and wipes for
	// this allocation use.
	BootInterface string   `json:"bootInterface"`
	TargetDisks   []string `json:"targetDisks"`
}

// Labels that pin the object incarnations a MachineAllocation was made for.
// No spec holds an object UID: the allocation's name hashes the consumer
// project and Machine UIDs, and these labels let a reader confirm them, so a
// same-name Machine, project or Device never inherits the allocation.
const (
	LabelMachineUID  = "fleet.datumapis.com/machine-uid"
	LabelConsumerUID = "fleet.datumapis.com/consumer-uid"
	LabelDeviceUID   = "fleet.datumapis.com/device-uid"
)

// TypedReference names an object by API group, kind and name, with no UID,
// in the shape of Milo's ResourceClaim consumerRef. An empty APIGroup is the
// core group.
type TypedReference struct {
	APIGroup string `json:"apiGroup"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

// AllocatedMachineReference names the Machine within its consumer. The
// incarnation is in the allocation's name and labels.
type AllocatedMachineReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// AllocatedDeviceReference names the Device in the allocation's namespace.
// The incarnation is in the allocation's device-uid label.
type AllocatedDeviceReference struct {
	Name string `json:"name"`
}

// AcceptedContract is the class and profile Fleet accepted, with the exact
// CPU and memory it allocated against.
type AcceptedContract struct {
	MachineClassRef   ContentReference  `json:"machineClassRef"`
	MachineProfileRef ContentReference  `json:"machineProfileRef"`
	CPU               CPURequirement    `json:"cpu"`
	Memory            MemoryRequirement `json:"memory"`
}

// ContentReference pins an object's content by its generation and a digest
// of its spec, which also tells same-name recreations apart.
type ContentReference struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
	// Digest is "sha256:<hex>" over the object's complete spec.
	Digest string `json:"digest"`
}

// AllocationAuthority is the admission decision Fleet acted on.
type AllocationAuthority struct {
	Principal string `json:"principal"`
	Decision  string `json:"decision"`
}

// MachineAllocation phases.
const (
	AllocationPending   = "Pending"
	AllocationActive    = "Active"
	AllocationReleasing = "Releasing"
	AllocationReleased  = "Released"
)

type MachineAllocationStatus struct {
	Phase string `json:"phase,omitempty"`
	// Wipe is the latest DeviceWipe the release created.
	Wipe       *WipeReference     `json:"wipe,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// WipeReference names a release's DeviceWipe and its step.
type WipeReference struct {
	Name string `json:"name"`
	Step int32  `json:"step"`
}

type MachineAllocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineAllocationSpec   `json:"spec"`
	Status            MachineAllocationStatus `json:"status,omitempty"`
}

type MachineAllocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineAllocation `json:"items"`
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
	s.AddKnownTypes(GroupVersion, &MachineClass{}, &MachineClassList{}, &MachineProfile{}, &MachineProfileList{}, &MachineCapacity{}, &MachineCapacityList{}, &Machine{}, &MachineList{}, &MachineAllocation{}, &MachineAllocationList{}, &Installation{}, &InstallationList{})
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
func (in *MachineCapacity) DeepCopyObject() runtime.Object {
	out := new(MachineCapacity)
	deepCopy(in, out)
	return out
}
func (in *MachineCapacityList) DeepCopyObject() runtime.Object {
	out := new(MachineCapacityList)
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
func (in *MachineAllocation) DeepCopyObject() runtime.Object {
	out := new(MachineAllocation)
	deepCopy(in, out)
	return out
}
func (in *MachineAllocationList) DeepCopyObject() runtime.Object {
	out := new(MachineAllocationList)
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
