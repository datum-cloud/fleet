// Package v1alpha1 holds the observation-only inventory API in
// fleet.datumapis.com. An InventoryObservation records what an external
// inventory system reports about one physical asset. It is evidence, not a
// Device, MachineCapacity, or plan: nothing in Fleet reads it to publish supply,
// authorize hardware effects, or admit an allocation.
package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "fleet.datumapis.com", Version: "v1alpha1"}

const (
	// SourceSystemNetBox is the only implemented external inventory system.
	SourceSystemNetBox = "NetBox"
	// SourceKindDevice selects a NetBox dcim device.
	SourceKindDevice = "Device"

	// TrustProfileObservedOnly marks facts as attributable but unverified. It
	// grants no trusted binding, free capacity, ownership, preparation, or effect.
	TrustProfileObservedOnly = "ObservedOnly-v1"

	// ConditionObserved is True only when the latest refresh read the exact
	// source object and its identity matched the spec.
	ConditionObserved = "Observed"

	ReasonObserved            = "Observed"
	ReasonSourceUnavailable   = "SourceUnavailable"
	ReasonSourceUnauthorized  = "SourceUnauthorized"
	ReasonSourceObjectMissing = "SourceObjectMissing"
	ReasonIdentityMismatch    = "IdentityMismatch"
	ReasonUnsupportedSource   = "UnsupportedSource"
)

// InventoryObservationSpec names one external object to observe. An operator
// writes it; the observer never edits spec, creates, or deletes observations.
type InventoryObservationSpec struct {
	Source ObservationSource `json:"source"`
}

// ObservationSource is immutable: retargeting an observation would let facts
// from one asset be read as another's. Create a new observation instead.
type ObservationSource struct {
	System string `json:"system"`
	Kind   string `json:"kind"`
	// ID is the stable object identifier in the source system.
	ID int64 `json:"id"`
	// ExpectedName guards against a mistyped ID. When set, a source object with
	// a different name is reported as IdentityMismatch and its facts are not kept.
	ExpectedName string `json:"expectedName,omitempty"`
}

// InventoryObservationStatus is written only by the observer.
type InventoryObservationStatus struct {
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	TrustProfile       string       `json:"trustProfile,omitempty"`
	LastAttemptTime    *metav1.Time `json:"lastAttemptTime,omitempty"`
	// ObservedAt is when the facts below were last read successfully. Facts are
	// retained when a later refresh fails; the Observed condition reports that.
	ObservedAt      *metav1.Time         `json:"observedAt,omitempty"`
	SourceUpdatedAt *metav1.Time         `json:"sourceUpdatedAt,omitempty"`
	FactsDigest     string               `json:"factsDigest,omitempty"`
	Facts           *ObservedDeviceFacts `json:"facts,omitempty"`
	Conditions      []metav1.Condition   `json:"conditions,omitempty"`
}

// ObservedDeviceFacts is an allowlist of source fields. Custom fields, comments,
// config contexts, and any other free-form source data are never copied.
type ObservedDeviceFacts struct {
	Name         string              `json:"name,omitempty"`
	Manufacturer string              `json:"manufacturer,omitempty"`
	Model        string              `json:"model,omitempty"`
	Serial       string              `json:"serial,omitempty"`
	AssetTag     string              `json:"assetTag,omitempty"`
	Role         string              `json:"role,omitempty"`
	Status       string              `json:"status,omitempty"`
	Placement    ObservedPlacement   `json:"placement,omitempty"`
	Interfaces   []ObservedInterface `json:"interfaces,omitempty"`
	Addresses    []ObservedAddress   `json:"addresses,omitempty"`
}

type ObservedPlacement struct {
	Site     string `json:"site,omitempty"`
	Rack     string `json:"rack,omitempty"`
	Position string `json:"position,omitempty"`
	Face     string `json:"face,omitempty"`
}

type ObservedInterface struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// SpeedKbps is the source value unchanged. NetBox defines it in kbps; it is
	// operator-entered and is not verified against the link.
	SpeedKbps      int64          `json:"speedKbps,omitempty"`
	Enabled        bool           `json:"enabled"`
	ManagementOnly bool           `json:"managementOnly,omitempty"`
	LAG            string         `json:"lag,omitempty"`
	MACAddresses   []string       `json:"macAddresses,omitempty"`
	Peers          []ObservedPeer `json:"peers,omitempty"`
}

// ObservedPeer is the recorded far end of a cable, not a verified path.
type ObservedPeer struct {
	Kind   string `json:"kind,omitempty"`
	Device string `json:"device,omitempty"`
	Port   string `json:"port,omitempty"`
}

// ObservedAddress is an address recorded in the source. Networking and IPAM
// remain the owners of address allocation; this is not an assignment.
type ObservedAddress struct {
	Role    string `json:"role"`
	Address string `json:"address"`
}

type InventoryObservation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InventoryObservationSpec   `json:"spec"`
	Status            InventoryObservationStatus `json:"status,omitempty"`
}

type InventoryObservationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InventoryObservation `json:"items"`
}

func addKnown(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &InventoryObservation{}, &InventoryObservationList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

var AddToScheme = addKnown

func (in *InventoryObservation) DeepCopyObject() runtime.Object {
	out := new(InventoryObservation)
	deepCopy(in, out)
	return out
}

func (in *InventoryObservation) DeepCopy() *InventoryObservation {
	out := new(InventoryObservation)
	deepCopy(in, out)
	return out
}

func (in *InventoryObservationList) DeepCopyObject() runtime.Object {
	out := new(InventoryObservationList)
	deepCopy(in, out)
	return out
}

func deepCopy[T any](in *T, out *T) { b, _ := json.Marshal(in); _ = json.Unmarshal(b, out) }
