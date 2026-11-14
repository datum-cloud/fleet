package simulator

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const maxRequestBodyBytes = maxInputsBytes + 8*maxIdentifierBytes

type BearerTokenSource func() (string, error)

func RuntimeHandler(store *Store, bearerToken string, logger *slog.Logger) http.Handler {
	if bearerToken == "" {
		panic("runtime bearer token must not be empty")
	}
	return RuntimeHandlerWithTokenSource(store, func() (string, error) { return bearerToken, nil }, logger)
}

func RuntimeHandlerWithTokenSource(store *Store, tokenSource BearerTokenSource, logger *slog.Logger) http.Handler {
	if tokenSource == nil {
		panic("runtime bearer token source must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/operations", requireBearer(tokenSource, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := decodeRequest(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
			return
		}
		op, lost, err := store.Submit(req)
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalid):
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			case errors.Is(err, ErrConflict):
				writeError(w, http.StatusConflict, "operation_conflict", err.Error())
			case errors.Is(err, ErrFenced):
				writeError(w, http.StatusConflict, "operation_fenced", err.Error())
			case errors.Is(err, ErrOutcomeUnknown):
				logger.Error("operation persistence outcome is unknown", "operationID", req.ID, "error", err)
				writeError(w, http.StatusServiceUnavailable, "outcome_unknown", "recover the operation by ID before retrying")
			default:
				logger.Error("submit simulator operation", "error", err)
				writeError(w, http.StatusInternalServerError, "internal_error", "operation outcome could not be returned")
			}
			return
		}
		if lost {
			writeError(w, http.StatusServiceUnavailable, "response_lost", "durable effect completed; recover the operation by ID")
			return
		}
		writeJSON(w, http.StatusOK, op.ResultView())
	})))
	mux.Handle("GET /v1/operations/{id}", requireBearer(tokenSource, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op, ok := store.Get(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "operation was not found")
			return
		}
		writeJSON(w, http.StatusOK, op.ResultView())
	})))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if err := store.Ready(); err != nil {
			logger.Error("simulator readiness check failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "not_ready", "durable state is unavailable")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func AdminHandler(store *Store, bearerToken string, logger *slog.Logger) http.Handler {
	if bearerToken == "" {
		panic("admin bearer token must not be empty")
	}
	return AdminHandlerWithTokenSource(store, func() (string, error) { return bearerToken, nil }, logger)
}

func AdminHandlerWithTokenSource(store *Store, tokenSource BearerTokenSource, logger *slog.Logger) http.Handler {
	if tokenSource == nil {
		panic("admin bearer token source must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.Handle("PUT /v1/faults/{name}", requireBearer(tokenSource, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeRequest(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
			return
		}
		name := strings.TrimSpace(r.PathValue("name"))
		if err := store.SetFault(name, body.Enabled); err != nil {
			if errors.Is(err, ErrInvalid) {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			if errors.Is(err, ErrOutcomeUnknown) {
				logger.Error("fault persistence outcome is unknown", "fault", name, "error", err)
				writeError(w, http.StatusServiceUnavailable, "outcome_unknown", "read retained state before retrying")
				return
			}
			logger.Error("set simulator fault", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "fault state could not be persisted")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /v1/state", requireBearer(tokenSource, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, store.Snapshot())
	})))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func requireBearer(tokenSource BearerTokenSource, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := tokenSource()
		if err != nil || token == "" {
			writeError(w, http.StatusServiceUnavailable, "authentication_unavailable", "current bearer authentication is unavailable")
			return
		}
		want := []byte("Bearer " + token)
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer authentication is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
