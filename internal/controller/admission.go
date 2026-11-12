// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
)

// SessionAdmission retains the authenticated API principal before the
// asynchronous controller resolves private allocation state. Kubernetes RBAC
// separately controls create on sessions; claim read permission does not imply it.
type SessionAdmission struct{}

func (SessionAdmission) Handle(_ context.Context, req admission.Request) admission.Response {
	var session fleetv1alpha1.MachineManagementSession
	if err := json.Unmarshal(req.Object.Raw, &session); err != nil {
		return admission.Errored(400, err)
	}
	if req.Operation == admissionv1.Update {
		var old fleetv1alpha1.MachineManagementSession
		if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
			return admission.Errored(400, err)
		}
		if !reflect.DeepEqual(session.Spec, old.Spec) {
			return admission.Denied("spec is immutable; create a new session")
		}
		if session.Annotations[PrincipalAnnotation] != old.Annotations[PrincipalAnnotation] {
			return admission.Denied("requesting principal is service-owned")
		}
		return admission.Allowed("immutable session update")
	}
	if req.Operation != admissionv1.Create {
		return admission.Allowed("no mutation required")
	}
	if session.Spec.Protocol != "Redfish" || session.Spec.AccessProfile != "Provisioning" {
		return admission.Denied("only the Redfish Provisioning profile is supported")
	}
	if session.Spec.TTL.Duration == 0 {
		session.Spec.TTL = metav1.Duration{Duration: 15 * time.Minute}
	}
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[PrincipalAnnotation] = req.UserInfo.Username
	mutated, err := json.Marshal(&session)
	if err != nil {
		return admission.Errored(500, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, mutated)
}
