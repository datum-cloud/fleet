// SPDX-License-Identifier: Apache-2.0

// This file is intentionally hand-maintained until generation is added to the
// repository. The JSON round-trip keeps the prototype correct for pointer,
// slice, map, and metav1 fields; `task generate` replaces it in production.
package v1alpha1

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/runtime"
)

func copyJSON[T any](in *T) *T {
	if in == nil {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	out := new(T)
	if err := json.Unmarshal(b, out); err != nil {
		panic(err)
	}
	return out
}

func (in *MachineClaim) DeepCopy() *MachineClaim                                 { return copyJSON(in) }
func (in *MachineClaim) DeepCopyObject() runtime.Object                          { return in.DeepCopy() }
func (in *MachineClaimList) DeepCopy() *MachineClaimList                         { return copyJSON(in) }
func (in *MachineClaimList) DeepCopyObject() runtime.Object                      { return in.DeepCopy() }
func (in *MachineManagementSession) DeepCopy() *MachineManagementSession         { return copyJSON(in) }
func (in *MachineManagementSession) DeepCopyObject() runtime.Object              { return in.DeepCopy() }
func (in *MachineManagementSessionList) DeepCopy() *MachineManagementSessionList { return copyJSON(in) }
func (in *MachineManagementSessionList) DeepCopyObject() runtime.Object          { return in.DeepCopy() }
func (in *MachineAllocationBinding) DeepCopy() *MachineAllocationBinding         { return copyJSON(in) }
func (in *MachineAllocationBinding) DeepCopyObject() runtime.Object              { return in.DeepCopy() }
func (in *MachineAllocationBindingList) DeepCopy() *MachineAllocationBindingList { return copyJSON(in) }
func (in *MachineAllocationBindingList) DeepCopyObject() runtime.Object          { return in.DeepCopy() }
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
