// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var GroupVersion = schema.GroupVersion{Group: "fleet.datumapis.com", Version: "v1alpha1"}

type LocalReference struct {
	Name string `json:"name"`
}

const (
	SessionFinalizer  = "fleet.datumapis.com/machine-management-session"
	ConditionReady    = "Ready"
	ProjectionActive  = "Active"
	ProjectionRevoked = "Revoked"
)

// MachineManagementSession is a short-lived public request for the Machine's
// current allocation. Provider and location details never enter this object.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type MachineManagementSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
	Spec   MachineManagementSessionSpec   `json:"spec"`
	Status MachineManagementSessionStatus `json:"status,omitempty"`
}

type MachineManagementSessionSpec struct {
	MachineRef LocalReference `json:"machineRef"`
	// +kubebuilder:validation:Enum=Redfish
	Protocol string `json:"protocol"`
	// +kubebuilder:validation:Enum=Provisioning
	AccessProfile string `json:"accessProfile"`
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	ClientPublicKey string `json:"clientPublicKey"`
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1m') && duration(self) <= duration('15m')",message="ttl must be between 1m and 15m"
	TTL metav1.Duration `json:"ttl"`
}

type MachineManagementSessionStatus struct {
	Connection    *SessionConnection `json:"connection,omitempty"`
	ConnectBefore *metav1.Time       `json:"connectBefore,omitempty"`
	ExpiresAt     *metav1.Time       `json:"expiresAt,omitempty"`
	Conditions    []metav1.Condition `json:"conditions,omitempty"`
}

// SessionConnection contains non-secret rendezvous information only.
type SessionConnection struct {
	RelayURL          string `json:"relayURL"`
	RouteID           string `json:"routeID"`
	GatewayAudience   string `json:"gatewayAudience"`
	GatewayCertSHA256 string `json:"gatewayCertSHA256"`
	ProtocolVersion   string `json:"protocolVersion"`
}

// +kubebuilder:object:root=true
type MachineManagementSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineManagementSession `json:"items"`
}

// MachineManagementSessionBinding is protected admission evidence. Local UIDs
// are retained only as same-API-server recreation preconditions and are never
// projected through Karmada.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type MachineManagementSessionBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="admitted binding inputs are immutable"
	Spec   MachineManagementSessionBindingSpec   `json:"spec"`
	Status MachineManagementSessionBindingStatus `json:"status,omitempty"`
}

type MachineManagementSessionBindingSpec struct {
	SessionNamespace         string      `json:"sessionNamespace"`
	SessionName              string      `json:"sessionName"`
	SessionUID               types.UID   `json:"sessionUID"`
	RequestingPrincipal      string      `json:"requestingPrincipal"`
	ConsumerProject          string      `json:"consumerProject"`
	ProviderProject          string      `json:"providerProject"`
	MachineNamespace         string      `json:"machineNamespace"`
	MachineName              string      `json:"machineName"`
	MachineUID               types.UID   `json:"machineUID"`
	AllocationName           string      `json:"allocationName"`
	AllocationUID            types.UID   `json:"allocationUID"`
	MachineCapacityNamespace string      `json:"machineCapacityNamespace"`
	MachineCapacityName      string      `json:"machineCapacityName"`
	MachineCapacityUID       types.UID   `json:"machineCapacityUID"`
	DeviceName               string      `json:"deviceName"`
	LocationName             string      `json:"locationName"`
	MemberCluster            string      `json:"memberCluster"`
	Protocol                 string      `json:"protocol"`
	AccessProfile            string      `json:"accessProfile"`
	ClientPublicKey          string      `json:"clientPublicKey"`
	ConnectBefore            metav1.Time `json:"connectBefore"`
	ExpiresAt                metav1.Time `json:"expiresAt"`
	SessionCorrelationID     string      `json:"sessionCorrelationID"`
	OperationCorrelationID   string      `json:"operationCorrelationID"`
}

type MachineManagementSessionBindingStatus struct {
	Connection *SessionConnection `json:"connection,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type MachineManagementSessionBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineManagementSessionBinding `json:"items"`
}

// ProjectedMachineManagementSession is the least-privileged Karmada-delivered
// location copy. Its generated name is the location session identity.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type ProjectedMachineManagementSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProjectedMachineManagementSessionSpec   `json:"spec"`
	Status            ProjectedMachineManagementSessionStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.machineCapacityRef == oldSelf.machineCapacityRef && self.protocol == oldSelf.protocol && self.accessProfile == oldSelf.accessProfile && self.clientPublicKey == oldSelf.clientPublicKey && self.connectBefore == oldSelf.connectBefore && self.expiresAt == oldSelf.expiresAt",message="projected authority inputs are immutable"
// +kubebuilder:validation:XValidation:rule="oldSelf.desiredState == 'Active' || self.desiredState == 'Revoked'",message="revocation is terminal"
// +kubebuilder:validation:XValidation:rule="timestamp(self.authorityFreshUntil) >= timestamp(oldSelf.authorityFreshUntil) && timestamp(self.authorityFreshUntil) <= timestamp(self.expiresAt)",message="authority freshness may only advance within expiry"
type ProjectedMachineManagementSessionSpec struct {
	// +kubebuilder:validation:Enum=Active;Revoked
	DesiredState        string         `json:"desiredState"`
	MachineCapacityRef  LocalReference `json:"machineCapacityRef"`
	Protocol            string         `json:"protocol"`
	AccessProfile       string         `json:"accessProfile"`
	ClientPublicKey     string         `json:"clientPublicKey"`
	ConnectBefore       metav1.Time    `json:"connectBefore"`
	ExpiresAt           metav1.Time    `json:"expiresAt"`
	AuthorityFreshUntil metav1.Time    `json:"authorityFreshUntil"`
}

type ProjectedMachineManagementSessionStatus struct {
	Connection  *SessionConnection `json:"connection,omitempty"`
	ConnectedAt *metav1.Time       `json:"connectedAt,omitempty"`
	EndedAt     *metav1.Time       `json:"endedAt,omitempty"`
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ProjectedMachineManagementSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProjectedMachineManagementSession `json:"items"`
}

func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&MachineManagementSession{}, &MachineManagementSessionList{},
		&MachineManagementSessionBinding{}, &MachineManagementSessionBindingList{},
		&ProjectedMachineManagementSession{}, &ProjectedMachineManagementSessionList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

func (in *MachineManagementSession) DeepCopy() *MachineManagementSession         { return copyJSON(in) }
func (in *MachineManagementSession) DeepCopyObject() runtime.Object              { return in.DeepCopy() }
func (in *MachineManagementSessionList) DeepCopy() *MachineManagementSessionList { return copyJSON(in) }
func (in *MachineManagementSessionList) DeepCopyObject() runtime.Object          { return in.DeepCopy() }
func (in *MachineManagementSessionBinding) DeepCopy() *MachineManagementSessionBinding {
	return copyJSON(in)
}
func (in *MachineManagementSessionBinding) DeepCopyObject() runtime.Object { return in.DeepCopy() }
func (in *MachineManagementSessionBindingList) DeepCopy() *MachineManagementSessionBindingList {
	return copyJSON(in)
}
func (in *MachineManagementSessionBindingList) DeepCopyObject() runtime.Object { return in.DeepCopy() }
func (in *ProjectedMachineManagementSession) DeepCopy() *ProjectedMachineManagementSession {
	return copyJSON(in)
}
func (in *ProjectedMachineManagementSession) DeepCopyObject() runtime.Object { return in.DeepCopy() }
func (in *ProjectedMachineManagementSessionList) DeepCopy() *ProjectedMachineManagementSessionList {
	return copyJSON(in)
}
func (in *ProjectedMachineManagementSessionList) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func copyJSON[T any](in *T) *T {
	b, _ := json.Marshal(in)
	out := new(T)
	_ = json.Unmarshal(b, out)
	return out
}
