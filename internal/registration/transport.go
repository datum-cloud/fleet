package registration

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func ParseControlKey(encoded []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(encoded)
	if block == nil || len(rest) != 0 {
		return nil, errors.New("expected one PKIX public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("control key must be Ed25519")
	}
	return key, nil
}

func ParseControl(encoded []byte) (SignedControl, error) {
	var control SignedControl
	if len(encoded) > 16384 {
		return control, errors.New("control request exceeds budget")
	}
	err := json.Unmarshal(encoded, &control)
	return control, err
}

func (handler *Handler) Transport() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/validate-registration", &admission.Webhook{Handler: handler})
	mux.HandleFunc("/transportz", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/activationz", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
		defer cancel()
		if err := handler.Ready(ctx); err != nil {
			http.Error(writer, "registration non-admitting or dependencies unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	// /metrics shares this listener, so it inherits the dedicated API-server
	// client mTLS boundary; there is no separate or anonymous metrics port.
	mux.Handle("/metrics", handler.metricsHandler())
	mux.HandleFunc("/status", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
		defer cancel()
		status, err := handler.Status(ctx)
		if err != nil {
			http.Error(writer, "registration ledger unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(status)
	})
	return mux
}

type Status struct {
	Authority          string    `json:"authority"`
	Projects           []string  `json:"projects"`
	RootUID            types.UID `json:"rootUID"`
	ResourceVersion    string    `json:"resourceVersion"`
	Epoch              uint64    `json:"epoch"`
	TailBytes          int       `json:"tailBytes"`
	HeadroomBytes      int       `json:"headroomBytes"`
	RemainingSegments  int       `json:"remainingSegments"`
	Pending            bool      `json:"pending"`
	ActivationVerified bool      `json:"activationVerified"`
	FenceDigest        string    `json:"fenceDigest,omitempty"`
}

func (handler *Handler) Status(ctx context.Context) (Status, error) {
	secret, route, err := handler.Store.root(ctx)
	if err != nil {
		return Status{}, err
	}
	if _, _, err := handler.Store.readSnapshot(ctx, secret); err != nil {
		return Status{}, err
	}
	tail := secret.Data["tail.json"]
	if _, exists := secret.Data["routing.json"]; !exists {
		tail = secret.Data["ledger.json"]
	}
	status := Status{Authority: handler.Store.Authority, Projects: handler.Store.Projects, RootUID: secret.UID, ResourceVersion: secret.ResourceVersion, Epoch: route.Epoch, TailBytes: len(tail), HeadroomBytes: max(0, TailBudget-len(tail)), RemainingSegments: MaxSegments - len(route.Segments), Pending: route.Pending != nil}
	status.ActivationVerified = handler.readySnapshot(ctx, secret, route) == nil
	if handler.FenceDigest != nil {
		status.FenceDigest, _ = handler.FenceDigest(ctx)
	}
	return status, nil
}
