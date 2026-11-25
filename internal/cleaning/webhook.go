package cleaning

import (
	"bytes"
	"context"
	"encoding/json"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WebhookPath is where the policy webhook is served.
const WebhookPath = "/validate-cleaning-policy"

// Webhook rejects a CleaningPolicy that Validate rejects. The CRD keeps the
// spec immutable; this checks what a schema cannot, such as a fallback method
// below the policy's assurance. It reads nothing and keeps no state.
type Webhook struct{}

func (Webhook) Handle(_ context.Context, request admission.Request) admission.Response {
	if request.Resource.Group != ops.GroupVersion.Group || request.Resource.Resource != "cleaningpolicies" || request.SubResource != "" ||
		(request.Operation != admissionv1.Create && request.Operation != admissionv1.Update) {
		return admission.Denied("unsupported cleaning policy admission request")
	}
	var policy ops.CleaningPolicy
	decoder := json.NewDecoder(bytes.NewReader(request.Object.Raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return admission.Denied("the cleaning policy could not be decoded: " + err.Error())
	}
	if err := Validate(policy.Spec); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("the cleaning policy is valid")
}
