// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	SessionFinalizer = "fleet.datumapis.com/machine-management-session"
	ConditionReady   = "Ready"
)

// MachineClaim is the public allocation handle. The prototype seeds it; the
// protected MachineAllocationBinding remains the authoritative allocation.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type MachineClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineClaimSpec   `json:"spec,omitempty"`
	Status            MachineClaimStatus `json:"status,omitempty"`
}

type MachineClaimSpec struct {
	MachineClassRef LocalObjectReference `json:"machineClassRef"`
}

type MachineClaimStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type LocalObjectReference struct {
	Name string `json:"name"`
}

// +kubebuilder:object:root=true
type MachineClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineClaim `json:"items"`
}

// MachineManagementSession is a short-lived public request for the machine
// currently bound to a claim. Resolved allocation details never enter it.
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
	MachineClaimRef LocalObjectReference `json:"machineClaimRef"`
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

// SessionConnection is non-secret rendezvous information. It never names a
// physical endpoint or adapter target.
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

// MachineAllocationBinding is synthetic prototype input owned by the allocator.
// It is protected in fleet-system and is never returned to customers.
// +kubebuilder:object:root=true
type MachineAllocationBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineAllocationBindingSpec `json:"spec"`
}

type MachineAllocationBindingSpec struct {
	ConsumerProjectUID    types.UID `json:"consumerProjectUID"`
	ProviderProjectUID    types.UID `json:"providerProjectUID"`
	ClaimNamespace        string    `json:"claimNamespace"`
	ClaimName             string    `json:"claimName"`
	ClaimUID              types.UID `json:"claimUID"`
	MachineName           string    `json:"machineName"`
	MachineUID            types.UID `json:"machineUID"`
	BindingID             string    `json:"bindingID"`
	BindingEpoch          int64     `json:"bindingEpoch"`
	LocationUID           types.UID `json:"locationUID"`
	LocationRevision      string    `json:"locationRevision"`
	BareMetalMachineUID   types.UID `json:"bareMetalMachineUID"`
	AccessProfileRevision string    `json:"accessProfileRevision"`
	AdapterRoute          string    `json:"adapterRoute"`
	BMCSystemID           string    `json:"bmcSystemID"`
	Active                bool      `json:"active"`
}

// +kubebuilder:object:root=true
type MachineAllocationBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MachineAllocationBinding `json:"items"`
}

// MachineManagementSessionBinding is the service-owned admission record.
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
	SessionNamespace       string      `json:"sessionNamespace"`
	SessionName            string      `json:"sessionName"`
	SessionUID             types.UID   `json:"sessionUID"`
	RequestingPrincipal    string      `json:"requestingPrincipal"`
	ConsumerProjectUID     types.UID   `json:"consumerProjectUID"`
	ProviderProjectUID     types.UID   `json:"providerProjectUID"`
	ClaimName              string      `json:"claimName"`
	ClaimUID               types.UID   `json:"claimUID"`
	MachineName            string      `json:"machineName"`
	MachineUID             types.UID   `json:"machineUID"`
	BindingID              string      `json:"bindingID"`
	BindingEpoch           int64       `json:"bindingEpoch"`
	LocationUID            types.UID   `json:"locationUID"`
	LocationRevision       string      `json:"locationRevision"`
	BareMetalMachineUID    types.UID   `json:"bareMetalMachineUID"`
	AccessProfileRevision  string      `json:"accessProfileRevision"`
	ClientPublicKey        string      `json:"clientPublicKey"`
	ConnectBefore          metav1.Time `json:"connectBefore"`
	ExpiresAt              metav1.Time `json:"expiresAt"`
	SessionCorrelationID   string      `json:"sessionCorrelationID"`
	OperationCorrelationID string      `json:"operationCorrelationID"`
	AdapterRoute           string      `json:"adapterRoute"`
	BMCSystemID            string      `json:"bmcSystemID"`
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

// ProjectedMachineManagementSession is the least-privileged location copy.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type ProjectedMachineManagementSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProjectedMachineManagementSessionSpec   `json:"spec"`
	Status            ProjectedMachineManagementSessionStatus `json:"status,omitempty"`
}

type ProjectedMachineManagementSessionSpec struct {
	SessionUID             types.UID   `json:"sessionUID"`
	ClaimUID               types.UID   `json:"claimUID"`
	MachineUID             types.UID   `json:"machineUID"`
	BindingID              string      `json:"bindingID"`
	BindingEpoch           int64       `json:"bindingEpoch"`
	LocationUID            types.UID   `json:"locationUID"`
	LocationRevision       string      `json:"locationRevision"`
	BareMetalMachineUID    types.UID   `json:"bareMetalMachineUID"`
	AccessProfileRevision  string      `json:"accessProfileRevision"`
	ClientPublicKey        string      `json:"clientPublicKey"`
	ConnectBefore          metav1.Time `json:"connectBefore"`
	ExpiresAt              metav1.Time `json:"expiresAt"`
	AuthorityFreshUntil    metav1.Time `json:"authorityFreshUntil"`
	SessionCorrelationID   string      `json:"sessionCorrelationID"`
	OperationCorrelationID string      `json:"operationCorrelationID"`
	AdapterRoute           string      `json:"adapterRoute"`
	BMCSystemID            string      `json:"bmcSystemID"`
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
