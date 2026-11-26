// Package powerwebhook is the validating webhook that keeps the manager's
// power projector the only writer of a Machine's status.power.
//
// RBAC cannot keep that field to one writer. Milo's authorizer checks a status
// write against the Machine's own update and patch permissions, which the
// consumer roles hold, and Machine status has other writers that share the
// projector's identity. Milo's root API server calls this webhook, because
// Milo does not evaluate admission policies stored in a project. The request's
// UserInfo is the original caller. The webhook compares status.power on the
// old and new objects and admits a change only from the configured writer.
// It reads nothing, keeps no state and holds no credentials.
package powerwebhook

import (
	"context"
	"encoding/json"
	"reflect"

	metal "go.datum.net/fleet/internal/api/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Path is where the handler is served.
const Path = "/validate-machine-power"

// Handler admits Machine status writes that leave status.power unchanged, and
// changes to it from the writer.
type Handler struct {
	// Writer is the username of the power projector's identity: the
	// manager's Milo identity. Empty refuses every change to status.power.
	Writer string
}

// powerOf reads status.power with exact, case-sensitive keys, as the API
// server does. encoding/json into a struct would match keys without regard
// to case and let a later duplicate win. An absent or null power is nil.
func powerOf(raw []byte) (any, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	status, _ := object["status"].(map[string]any)
	return status["power"], nil
}

func (h *Handler) Handle(_ context.Context, request admission.Request) admission.Response {
	if request.Resource.Group != metal.GroupVersion.Group || request.Resource.Resource != "machines" || request.SubResource != "status" || request.Operation != admissionv1.Update {
		return admission.Denied("unsupported Machine power admission request")
	}
	power, err := powerOf(request.Object.Raw)
	if err != nil {
		return admission.Denied("the Machine could not be decoded")
	}
	previous, err := powerOf(request.OldObject.Raw)
	if err != nil {
		return admission.Denied("the Machine could not be decoded")
	}
	if reflect.DeepEqual(power, previous) {
		return admission.Allowed("status.power is unchanged")
	}
	if h.Writer != "" && request.UserInfo.Username == h.Writer {
		return admission.Allowed("the power projector writes status.power")
	}
	return admission.Denied("only the power projector writes status.power")
}
