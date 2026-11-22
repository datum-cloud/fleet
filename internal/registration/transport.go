package registration

import (
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Transport serves the validating webhook and a transport probe. Both sit
// behind the same mutually authenticated TLS listener.
func (handler *Handler) Transport() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/validate-registration", &admission.Webhook{Handler: handler})
	mux.HandleFunc("/transportz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	return mux
}
