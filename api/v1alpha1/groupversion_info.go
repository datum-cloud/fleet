// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 contains the provisional Fleet machine-management APIs.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "fleet.datumapis.com", Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&MachineClaim{}, &MachineClaimList{},
		&MachineManagementSession{}, &MachineManagementSessionList{},
		&MachineAllocationBinding{}, &MachineAllocationBindingList{},
		&MachineManagementSessionBinding{}, &MachineManagementSessionBindingList{},
		&ProjectedMachineManagementSession{}, &ProjectedMachineManagementSessionList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
