package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type ExternalReference struct {
	System string `json:"system"`
	Type   string `json:"type"`
	ID     string `json:"id"`
}

// ModelShape is a nominal shape, never a measurement. FullDepth means the
// chassis reaches both the front and rear rails. Unset means unknown and is
// never defaulted. Width is Full or Half; unset means unknown, and a reader
// that draws it assumes Full and says so. A Device value overrides its
// DeviceType's value.
type ModelShape struct {
	HeightUnits *float64     `json:"heightUnits,omitempty"`
	FullDepth   *bool        `json:"fullDepth,omitempty"`
	Width       ChassisWidth `json:"width,omitempty"`
}

// ChassisWidth is the nominal chassis width: the full rack width, or half,
// for servers and switches that sit side by side in one unit. Which half a
// Device occupies is a placement fact and is not recorded here.
type ChassisWidth string

const (
	ChassisFullWidth ChassisWidth = "Full"
	ChassisHalfWidth ChassisWidth = "Half"
)

type PhysicalMetadata struct {
	DisplayName string `json:"displayName"`
}

type FacilitySpec struct {
	PhysicalMetadata `json:",inline"`
	LocationRef      *LocalReference      `json:"locationRef,omitempty"`
	Identifiers      []FacilityIdentifier `json:"identifiers,omitempty"`
}

type FacilityIdentifier struct {
	Provider string `json:"provider"`
	Type     string `json:"type"`
	Value    string `json:"value"`
}

type SiteSpec struct {
	PhysicalMetadata `json:",inline"`
	Code             string           `json:"code"`
	FacilityRefs     []LocalReference `json:"facilityRefs,omitempty"`
}

type Site struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SiteSpec `json:"spec"`
}

type SiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Site `json:"items"`
}

func (in *Site) DeepCopyObject() runtime.Object {
	out := new(Site)
	deepCopy(in, out)
	return out
}

func (in *SiteList) DeepCopyObject() runtime.Object {
	out := new(SiteList)
	deepCopy(in, out)
	return out
}

type RackSpec struct {
	PhysicalMetadata `json:",inline"`
	FacilityRef      LocalReference `json:"facilityRef"`
	HeightUnits      int32          `json:"heightUnits,omitempty"`
}

type Component struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Connector string `json:"connector,omitempty"`
	Positions int32  `json:"positions,omitempty"`
	Serial    string `json:"serial,omitempty"`
	AssetTag  string `json:"assetTag,omitempty"`
}

type ComponentPosition struct {
	ComponentID string `json:"componentID"`
	Position    int32  `json:"position"`
}

type InternalMapping struct {
	From ComponentPosition `json:"from"`
	To   ComponentPosition `json:"to"`
}

type ServiceAssociation struct {
	ServiceRef  ExternalReference `json:"serviceRef"`
	ComponentID string            `json:"componentID"`
}

type CableEndpoint struct {
	DeviceRef    *LocalReference `json:"deviceRef,omitempty"`
	PowerFeedRef *LocalReference `json:"powerFeedRef,omitempty"`
	ComponentID  string          `json:"componentID"`
	Position     int32           `json:"position"`
}

type CableLane struct {
	ID string        `json:"id"`
	A  CableEndpoint `json:"a"`
	B  CableEndpoint `json:"b"`
}

type CableSpec struct {
	PhysicalMetadata `json:",inline"`
	Medium           string      `json:"medium"`
	Lanes            []CableLane `json:"lanes"`
}

type PowerSupply struct {
	Type     string `json:"type"`
	VoltageV int32  `json:"voltageV,omitempty"`
	CurrentA int32  `json:"currentA,omitempty"`
	Phase    string `json:"phase,omitempty"`
}

type PowerFeedSpec struct {
	PhysicalMetadata `json:",inline"`
	FacilityRef      LocalReference      `json:"facilityRef"`
	RackRef          *LocalReference     `json:"rackRef,omitempty"`
	Supply           *PowerSupply        `json:"supply,omitempty"`
	Terminations     []Component         `json:"terminations"`
	UpstreamSources  []ExternalReference `json:"upstreamSources,omitempty"`
}

type Facility struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FacilitySpec `json:"spec"`
}
type Rack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RackSpec `json:"spec"`
}
type Cable struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CableSpec `json:"spec"`
}
type PowerFeed struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PowerFeedSpec `json:"spec"`
}
type FacilityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Facility `json:"items"`
}
type RackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Rack `json:"items"`
}
type CableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cable `json:"items"`
}
type PowerFeedList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PowerFeed `json:"items"`
}

func (in *Facility) DeepCopyObject() runtime.Object {
	out := new(Facility)
	deepCopy(in, out)
	return out
}
func (in *Rack) DeepCopyObject() runtime.Object  { out := new(Rack); deepCopy(in, out); return out }
func (in *Cable) DeepCopyObject() runtime.Object { out := new(Cable); deepCopy(in, out); return out }
func (in *PowerFeed) DeepCopyObject() runtime.Object {
	out := new(PowerFeed)
	deepCopy(in, out)
	return out
}
func (in *FacilityList) DeepCopyObject() runtime.Object {
	out := new(FacilityList)
	deepCopy(in, out)
	return out
}
func (in *RackList) DeepCopyObject() runtime.Object {
	out := new(RackList)
	deepCopy(in, out)
	return out
}
func (in *CableList) DeepCopyObject() runtime.Object {
	out := new(CableList)
	deepCopy(in, out)
	return out
}
func (in *PowerFeedList) DeepCopyObject() runtime.Object {
	out := new(PowerFeedList)
	deepCopy(in, out)
	return out
}
