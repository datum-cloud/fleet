// SPDX-License-Identifier: Apache-2.0

package redfish

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.datum.net/fleet/internal/journal"
	fleetmetrics "go.datum.net/fleet/internal/metrics"
)

const SystemID = "allocated"

type Authority struct {
	SessionName         string
	MachineCapacityName string
	DeviceName          string
	OperationEpoch      int64
	ExpiresAt           time.Time
	FreshUntil          time.Time
}

type Handler struct {
	Adapter          Adapter
	Journal          *journal.Store
	Authority        Authority
	CurrentAuthority func() Authority
	Now              func() time.Time
	mu               sync.Mutex
	tokens           map[string]bool
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setODataHeaders(w)
	if err := h.checkRequest(r); err != nil {
		writeError(w, http.StatusBadRequest, "Base.1.18.PropertyValueFormatError", err.Error())
		return
	}
	authority := h.authority()
	if h.now().After(authority.ExpiresAt) {
		writeError(w, http.StatusForbidden, "Fleet.1.0.SessionExpired", "The machine-management session has expired.")
		return
	}
	if h.now().After(authority.FreshUntil) {
		writeError(w, http.StatusServiceUnavailable, "Fleet.1.0.AuthorityStale", "Allocation authority is stale; the gateway has failed closed.")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/":
		h.serviceRoot(w)
	case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/SessionService":
		h.sessionService(w)
	case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/SessionService/Sessions":
		writeJSON(w, http.StatusOK, map[string]any{"@odata.type": "#SessionCollection.SessionCollection", "@odata.id": r.URL.Path, "Name": "Sessions", "Members": []any{}, "Members@odata.count": 0})
	case r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/SessionService/Sessions":
		h.login(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/redfish/v1/SessionService/Sessions/"):
		h.logout(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Systems":
		h.systems(w)
	case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Systems/"+SystemID:
		h.system(w, r)
	case r.Method == http.MethodPatch && r.URL.Path == "/redfish/v1/Systems/"+SystemID:
		h.patchSystem(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/Systems/"+SystemID+"/Actions/ComputerSystem.Reset":
		h.reset(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/TaskService":
		h.taskService(w)
	case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/TaskService/Tasks":
		h.tasks(w)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/redfish/v1/TaskService/Tasks/"):
		h.task(w, strings.TrimPrefix(r.URL.Path, "/redfish/v1/TaskService/Tasks/"))
	default:
		writeError(w, http.StatusNotFound, "Base.1.18.ResourceMissingAtURI", "The resource is outside the Fleet Provisioning profile.")
	}
}

func (h *Handler) checkRequest(r *http.Request) error {
	if r.URL.RawPath != "" {
		if u, err := url.PathUnescape(r.URL.RawPath); err != nil || u != r.URL.Path {
			return errors.New("encoded or ambiguous paths are not accepted")
		}
	}
	if strings.Contains(r.URL.Path, "..") || !strings.HasPrefix(r.URL.Path, "/redfish/v1") {
		return errors.New("target is outside the virtual Redfish service")
	}
	if r.URL.IsAbs() || r.RequestURI == "*" {
		return errors.New("absolute and arbitrary targets are not accepted")
	}
	host := r.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	if host != "" && host != "127.0.0.1" && host != "localhost" && host != "fleet-gateway" {
		return errors.New("Host routing is not accepted")
	}
	return nil
}

func (h *Handler) serviceRoot(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"@odata.type": "#ServiceRoot.v1_15_0.ServiceRoot", "@odata.id": "/redfish/v1/", "Id": "RootService", "Name": "Fleet Virtual Redfish Service", "RedfishVersion": "1.18.0", "UUID": virtualUUID(h.authority().SessionName), "Systems": odata("/redfish/v1/Systems"), "SessionService": odata("/redfish/v1/SessionService"), "TaskService": odata("/redfish/v1/TaskService")})
}

func virtualUUID(sessionName string) string {
	sum := sha256.Sum256([]byte("go.datum.net/fleet/redfish-service-root/v1\n" + sessionName))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func (h *Handler) sessionService(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"@odata.type": "#SessionService.v1_2_0.SessionService", "@odata.id": "/redfish/v1/SessionService", "Id": "SessionService", "Name": "Session Service", "ServiceEnabled": true, "SessionTimeout": 900, "Sessions": odata("/redfish/v1/SessionService/Sessions")})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserName string `json:"UserName"`
		Password string `json:"Password"`
	}
	if err := decodeExact(r, &in); err != nil || in.UserName == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "Base.1.18.MalformedJSON", "UserName and Password are required.")
		return
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	h.mu.Lock()
	if h.tokens == nil {
		h.tokens = map[string]bool{}
	}
	h.tokens[token] = true
	h.mu.Unlock()
	loc := "/redfish/v1/SessionService/Sessions/local"
	w.Header().Set("X-Auth-Token", token)
	w.Header().Set("Location", loc)
	writeJSON(w, http.StatusCreated, map[string]any{"@odata.type": "#Session.v1_7_0.Session", "@odata.id": loc, "Id": "local", "Name": "Fleet session-local login", "UserName": in.UserName})
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	delete(h.tokens, r.Header.Get("X-Auth-Token"))
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) systems(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"@odata.type": "#ComputerSystemCollection.ComputerSystemCollection", "@odata.id": "/redfish/v1/Systems", "Name": "Computer System Collection", "Members": []any{odata("/redfish/v1/Systems/" + SystemID)}, "Members@odata.count": 1})
}

func (h *Handler) system(w http.ResponseWriter, r *http.Request) {
	s, err := h.Adapter.Observe(r.Context())
	if err != nil {
		fleetmetrics.BMCReachability.WithLabelValues("unreachable").Inc()
		writeError(w, http.StatusServiceUnavailable, "Fleet.1.0.BMCUnavailable", "The allocated system management endpoint is unavailable.")
		return
	}
	fleetmetrics.BMCReachability.WithLabelValues("reachable").Inc()
	writeJSON(w, http.StatusOK, map[string]any{"@odata.type": "#ComputerSystem.v1_20_0.ComputerSystem", "@odata.id": r.URL.Path, "Id": SystemID, "Name": "Allocated Machine", "SystemType": "Physical", "PowerState": s.PowerState, "Status": map[string]string{"State": "Enabled", "Health": "OK"}, "Boot": map[string]any{"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": s.BootTarget, "BootSourceOverrideTarget@Redfish.AllowableValues": []string{"Pxe", "Hdd"}}, "Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": r.URL.Path + "/Actions/ComputerSystem.Reset", "ResetType@Redfish.AllowableValues": []string{"On", "ForceOff", "GracefulShutdown", "GracefulRestart", "ForceRestart"}}}})
}

func (h *Handler) patchSystem(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Base.1.18.MalformedJSON", err.Error())
		return
	}
	var in struct {
		Boot *struct {
			Enabled string `json:"BootSourceOverrideEnabled"`
			Target  string `json:"BootSourceOverrideTarget"`
		} `json:"Boot"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Boot == nil {
		writeError(w, http.StatusBadRequest, "Base.1.18.PropertyNotWritable", "Only Boot one-time override is writable.")
		return
	}
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	boot, bootOK := generic["Boot"].(map[string]any)
	if len(generic) != 1 || !bootOK || len(boot) != 2 || in.Boot.Enabled != "Once" || (in.Boot.Target != "Pxe" && in.Boot.Target != "Hdd") {
		writeError(w, http.StatusBadRequest, "Base.1.18.PropertyValueNotInList", "Only one-time Pxe or Hdd boot selection is supported.")
		return
	}
	h.mutate(w, r, raw, func() error { return h.Adapter.SetOneTimeBoot(r.Context(), in.Boot.Target) })
}

func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Base.1.18.MalformedJSON", err.Error())
		return
	}
	var in struct {
		ResetType string `json:"ResetType"`
	}
	var generic map[string]any
	if json.Unmarshal(raw, &in) != nil || json.Unmarshal(raw, &generic) != nil || len(generic) != 1 || !supportedReset(in.ResetType) {
		writeError(w, http.StatusBadRequest, "Base.1.18.ActionParameterNotSupported", "ResetType is not supported by this profile.")
		return
	}
	h.mutate(w, r, raw, func() error { return h.Adapter.Reset(r.Context(), in.ResetType) })
}

func (h *Handler) mutate(w http.ResponseWriter, r *http.Request, raw []byte, effect func() error) {
	now := h.now()
	authority := h.authority()
	if now.After(authority.ExpiresAt) {
		writeError(w, http.StatusForbidden, "Fleet.1.0.SessionExpired", "The machine-management session has expired.")
		return
	}
	if now.After(authority.FreshUntil) {
		writeError(w, http.StatusServiceUnavailable, "Fleet.1.0.AuthorityStale", "Allocation authority is stale; the gateway has failed closed.")
		return
	}
	id := journal.Identity(authority.SessionName, r.Method, r.URL.Path, raw)
	unlock := h.Journal.MachineLock(authority.DeviceName)
	defer unlock()
	op, fresh, err := h.Journal.Admit(journal.Operation{ID: id, SessionName: authority.SessionName, MachineCapacityName: authority.MachineCapacityName, DeviceName: authority.DeviceName, Method: r.Method, VirtualTarget: r.URL.Path, AcceptedBody: append([]byte(nil), raw...), PhysicalTarget: h.Adapter.PhysicalTarget(), IdempotencyIdentity: id, OperationEpoch: authority.OperationEpoch, ConflictPrecondition: fmt.Sprintf("capacity=%s device=%s fresh-before=%s", authority.MachineCapacityName, authority.DeviceName, authority.FreshUntil.UTC().Format(time.RFC3339))})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Fleet.1.0.JournalUnavailable", "The mutation could not be durably admitted.")
		return
	}
	if !fresh {
		h.writeExistingTask(w, op)
		return
	}
	if err := effect(); err != nil {
		if errors.Is(err, ErrResponseLost) {
			op, _ = h.Journal.Finish(id, journal.Unknown, 0, "effect submitted; response lost; recover before retry")
			fleetmetrics.Operations.WithLabelValues("unknown").Inc()
			fleetmetrics.UnknownOperations.Inc()
			h.writeExistingTask(w, op)
			return
		}
		op, _ = h.Journal.Finish(id, journal.Failed, http.StatusBadGateway, "adapter returned a bounded non-secret failure")
		fleetmetrics.Operations.WithLabelValues("failed").Inc()
		writeError(w, http.StatusBadGateway, "Fleet.1.0.BMCOperationFailed", "The allocated system did not complete the requested operation.")
		return
	}
	op, _ = h.Journal.Finish(id, journal.Succeeded, http.StatusOK, "adapter response received")
	fleetmetrics.Operations.WithLabelValues("succeeded").Inc()
	w.Header().Set("Location", "/redfish/v1/TaskService/Tasks/"+op.ID)
	writeJSON(w, http.StatusAccepted, taskBody(op))
}

func (h *Handler) writeExistingTask(w http.ResponseWriter, op journal.Operation) {
	w.Header().Set("Location", "/redfish/v1/TaskService/Tasks/"+op.ID)
	writeJSON(w, http.StatusAccepted, taskBody(op))
}
func (h *Handler) taskService(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"@odata.type": "#TaskService.v1_2_0.TaskService", "@odata.id": "/redfish/v1/TaskService", "Id": "TaskService", "Name": "Task Service", "ServiceEnabled": true, "Tasks": odata("/redfish/v1/TaskService/Tasks")})
}
func (h *Handler) tasks(w http.ResponseWriter) {
	operations, err := h.Journal.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Fleet.1.0.JournalUnavailable", "Tasks could not be read from the durable journal.")
		return
	}
	members := make([]any, 0, len(operations))
	for _, operation := range operations {
		if operation.SessionName == h.authority().SessionName {
			members = append(members, odata("/redfish/v1/TaskService/Tasks/"+operation.ID))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"@odata.type": "#TaskCollection.TaskCollection", "@odata.id": "/redfish/v1/TaskService/Tasks", "Name": "Tasks", "Members": members, "Members@odata.count": len(members)})
}
func (h *Handler) task(w http.ResponseWriter, id string) {
	op, err := h.Journal.Get(id)
	if err != nil || op.SessionName != h.authority().SessionName {
		writeError(w, http.StatusNotFound, "Base.1.18.ResourceMissingAtURI", "Task not found.")
		return
	}
	writeJSON(w, http.StatusOK, taskBody(op))
}

func taskBody(op journal.Operation) map[string]any {
	state, status := "Running", "OK"
	if op.Outcome == journal.Succeeded {
		state = "Completed"
	}
	if op.Outcome == journal.Failed {
		state = "Exception"
		status = "Critical"
	}
	if op.Outcome == journal.Unknown {
		state = "Exception"
		status = "Warning"
	}
	return map[string]any{"@odata.type": "#Task.v1_7_4.Task", "@odata.id": "/redfish/v1/TaskService/Tasks/" + op.ID, "Id": op.ID, "Name": "Fleet machine operation", "TaskState": state, "TaskStatus": status, "Messages": []any{map[string]any{"MessageId": "Fleet.1.0." + string(op.Outcome), "Message": op.Evidence, "Severity": status, "Resolution": "Inspect or recover the existing operation; do not resubmit an unknown effect."}}}
}
func odata(path string) map[string]string { return map[string]string{"@odata.id": path} }
func setODataHeaders(w http.ResponseWriter) {
	w.Header().Set("OData-Version", "4.0")
	w.Header().Set("Content-Type", "application/json")
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, id, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    id,
			"message": message,
			"@Message.ExtendedInfo": []any{map[string]any{
				"MessageId":  id,
				"Message":    message,
				"Severity":   "Critical",
				"Resolution": "Use only resources and values declared by the Fleet Provisioning profile.",
			}},
		},
	})
}
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("request body is required")
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil, errors.New("request body must be valid JSON")
	}
	return b, nil
}
func decodeExact(r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) authority() Authority {
	if h.CurrentAuthority != nil {
		return h.CurrentAuthority()
	}
	return h.Authority
}
