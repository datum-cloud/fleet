// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import (
	"encoding/json"
	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "coordination.fleet.datumapis.com", Version: "v1alpha1"}

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
	DesiredState        string                   `json:"desiredState"`
	MachineCapacityRef  sessionv1.LocalReference `json:"machineCapacityRef"`
	Protocol            string                   `json:"protocol"`
	AccessProfile       string                   `json:"accessProfile"`
	ClientPublicKey     string                   `json:"clientPublicKey"`
	ConnectBefore       metav1.Time              `json:"connectBefore"`
	ExpiresAt           metav1.Time              `json:"expiresAt"`
	AuthorityFreshUntil metav1.Time              `json:"authorityFreshUntil"`
}

type ProjectedMachineManagementSessionStatus struct {
	Connection  *sessionv1.SessionConnection `json:"connection,omitempty"`
	ConnectedAt *metav1.Time                 `json:"connectedAt,omitempty"`
	EndedAt     *metav1.Time                 `json:"endedAt,omitempty"`
	Conditions  []metav1.Condition           `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ProjectedMachineManagementSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProjectedMachineManagementSession `json:"items"`
}

func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &ProjectedMachineManagementSession{}, &ProjectedMachineManagementSessionList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
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
