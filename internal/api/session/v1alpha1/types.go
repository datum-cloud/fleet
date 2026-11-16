// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var GroupVersion = schema.GroupVersion{Group: "metal.datumapis.com", Version: "v1alpha1"}

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

func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&MachineManagementSession{}, &MachineManagementSessionList{},
		&MachineManagementSessionBinding{}, &MachineManagementSessionBindingList{},
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
func copyJSON[T any](in *T) *T {
	b, _ := json.Marshal(in)
	out := new(T)
	_ = json.Unmarshal(b, out)
	return out
}
