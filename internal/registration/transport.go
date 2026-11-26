package registration

import (
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.datum.net/fleet/internal/custody/creatorwebhook"
	"go.datum.net/fleet/internal/powerwebhook"
)

// Transport serves the validating webhooks and a transport probe. All sit
// behind the same mutually authenticated TLS listener. The operation creator
// webhook shares this server: it has the same caller, the same transport
// and the same provider scope, and it keeps no state. The Machine power
// webhook shares it for the same reasons, across every consuming project.
func (handler *Handler) Transport(creator *creatorwebhook.Handler, power *powerwebhook.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/validate-registration", &admission.Webhook{Handler: handler})
	mux.Handle(creatorwebhook.Path, &admission.Webhook{Handler: creator})
	mux.Handle(powerwebhook.Path, &admission.Webhook{Handler: power})
	mux.HandleFunc("/transportz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	return mux
}
