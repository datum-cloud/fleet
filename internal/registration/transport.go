package registration

import (
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.datum.net/fleet/internal/custody/creatorwebhook"
	"go.datum.net/fleet/internal/machineadmission"
)

// Transport serves the validating webhooks and a transport probe. All sit
// behind the same mutually authenticated TLS listener. The operation creator
// webhook shares this server: it has the same caller, the same transport
// and the same provider scope, and it keeps no state. So do the Machine
// creator webhooks, which need no provider scope.
func (handler *Handler) Transport(creator *creatorwebhook.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/validate-registration", &admission.Webhook{Handler: handler})
	mux.Handle(creatorwebhook.Path, &admission.Webhook{Handler: creator})
	mux.Handle(machineadmission.MutatePath, &admission.Webhook{Handler: machineadmission.Mutator{}})
	mux.Handle(machineadmission.ValidatePath, &admission.Webhook{Handler: machineadmission.Validator{}})
	mux.HandleFunc("/transportz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	return mux
}
