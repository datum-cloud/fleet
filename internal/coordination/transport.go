// Package coordination is the federation layer between the custody
// coordinator and a cell: the coordinator writes handoffs on the Karmada API,
// Karmada delivers each to its cell, and the cell's status comes back through
// Karmada's status reflection. A handoff carries either one admitted
// operation's assignment or a request for a Device's fence report. It is a
// local acceptance transport, not a production admission authority.
package coordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

var Resource = schema.GroupVersionResource{Group: "coordination.fleet.datumapis.com", Version: "v1alpha1", Resource: "machineallocationhandoffs"}

const (
	Kind = "MachineAllocationHandoff"
	// CellLabel selects the handoffs delivered to one cell.
	CellLabel = "coordination.fleet.datumapis.com/cell"
	// RoleLabel is RoleAssignment or RoleReport. Only assignments start a
	// worker.
	RoleLabel      = "coordination.fleet.datumapis.com/role"
	RoleAssignment = "assignment"
	RoleReport     = "report"
	// AnsweredLabel marks an assignment whose result the coordinator
	// answered. The cell then deletes its copy of the result.
	AnsweredLabel = "coordination.fleet.datumapis.com/answered"
)

var (
	// ErrConflict means a handoff exists with other contents.
	ErrConflict = errors.New("coordination: the handoff exists with other contents")
	// ErrInvalid means a handoff or its reflected status is malformed, from
	// another cell, or stale.
	ErrInvalid = errors.New("coordination: invalid handoff or reflection")
)

// Spec is a handoff's immutable contents.
type Spec struct {
	Cell string `json:"cell"`
	// Device is "<namespace>/<name>".
	Device string `json:"device"`
	// AssignmentJSON is the exact assignment, as a string so an API server
	// cannot reorder it. A report handoff has none.
	AssignmentJSON string `json:"assignmentJSON,omitempty"`
	// Credential is the name of the Device's BMC credential Secret at the
	// site, derived from the Device. The site grants the assignment's worker
	// get on exactly that Secret. A report handoff has none.
	Credential string `json:"credential,omitempty"`
}

// FenceReport is the cell's stored fences for the Device.
type FenceReport struct {
	Seen    int64 `json:"seen"`
	Stopped int64 `json:"stopped"`
}

// Observation is what the cell's observer reports for one handoff.
type Observation struct {
	Cell string `json:"cell"`
	// AssignmentDigest is the digest of the assignment observed, empty for a
	// report.
	AssignmentDigest string              `json:"assignmentDigest,omitempty"`
	ObservedAt       time.Time           `json:"observedAt"`
	Fence            FenceReport         `json:"fence"`
	Result           *custody.SiteResult `json:"result,omitempty"`
	// Delivery is the site enrollment's delivery declaration, on a report
	// handoff. Nil means the site could not read a valid one.
	Delivery *DeliveryDeclaration `json:"delivery,omitempty"`
}

// DeliveryDeclaration is the delivery methods the site's enrollment
// declares, in its order of preference. An empty list offers none.
type DeliveryDeclaration struct {
	Methods []custody.OfferedMethod `json:"methods"`
}

// CellStatus is the status the cell writes.
type CellStatus struct {
	Observation *Observation `json:"observation,omitempty"`
}

// Reflected is one cell's status as Karmada aggregates it on the hub.
type Reflected struct {
	ClusterName string      `json:"clusterName"`
	Observation Observation `json:"observation"`
}

// HubStatus is the status Karmada writes on the hub.
type HubStatus struct {
	Reflected []Reflected `json:"reflected,omitempty"`
}

// AssignmentName is the only name an assignment's handoff may have.
func AssignmentName(assignment site.Assignment) string {
	return "op-" + digest(fmt.Sprintf("%s/%s/%d", assignment.Device, assignment.Operation.UID, assignment.Fence))[:40]
}

// ReportName is the only name a Device's report handoff may have.
func ReportName(device string) string { return "fence-" + digest(device)[:40] }

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Digest is the digest of an assignment's exact JSON.
func Digest(assignmentJSON string) string { return digest(assignmentJSON) }

func decode(object *unstructured.Unstructured, status any) (Spec, error) {
	var parsed struct {
		Spec   Spec            `json:"spec"`
		Status json.RawMessage `json:"status"`
	}
	data, err := object.MarshalJSON()
	if err != nil {
		return Spec{}, err
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Spec{}, err
	}
	if status != nil && len(parsed.Status) != 0 {
		if err := json.Unmarshal(parsed.Status, status); err != nil {
			return Spec{}, err
		}
	}
	return parsed.Spec, nil
}

func newObject(namespace, name, role string, spec Spec) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion(Resource.GroupVersion().String())
	object.SetKind(Kind)
	object.SetNamespace(namespace)
	object.SetName(name)
	object.SetLabels(map[string]string{CellLabel: spec.Cell, RoleLabel: role})
	data, _ := json.Marshal(spec)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	object.Object["spec"] = fields
	return object
}

// Hub is the coordinator's view of one cell through Karmada. It implements
// delivery.Site. It writes handoffs and their answered label; it never
// writes status.
type Hub struct {
	API       dynamic.Interface
	Namespace string
	Cell      string
	// MaxAge is how old a fence report may be. A result is a durable fact
	// and has no age limit.
	MaxAge time.Duration
	Now    func() time.Time
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

func (h *Hub) api() dynamic.ResourceInterface { return h.API.Resource(Resource).Namespace(h.Namespace) }

// ensure creates the handoff or requires the existing one to be identical.
func (h *Hub) ensure(ctx context.Context, name, role string, spec Spec) (*unstructured.Unstructured, error) {
	object, err := h.api().Create(ctx, newObject(h.Namespace, name, role, spec), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		object, err = h.api().Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}
	retained, err := decode(object, nil)
	if err != nil || retained != spec || object.GetLabels()[RoleLabel] != role || object.GetLabels()[CellLabel] != h.Cell {
		return nil, ErrConflict
	}
	return object, nil
}

func (h *Hub) Deliver(ctx context.Context, assignment site.Assignment) error {
	data, err := json.Marshal(assignment)
	if err != nil {
		return err
	}
	_, err = h.ensure(ctx, AssignmentName(assignment), RoleAssignment, Spec{Cell: h.Cell, Device: assignment.Device, AssignmentJSON: string(data), Credential: site.CredentialSecretName(assignment.Device)})
	return err
}

// reflected returns the one valid observation of this cell.
func (h *Hub) reflected(object *unstructured.Unstructured) (Spec, Observation, error) {
	var status HubStatus
	spec, err := decode(object, &status)
	if err != nil || spec.Cell != h.Cell || len(status.Reflected) != 1 {
		return spec, Observation{}, ErrInvalid
	}
	reflected := status.Reflected[0]
	observation := reflected.Observation
	if reflected.ClusterName != h.Cell || observation.Cell != h.Cell || observation.ObservedAt.IsZero() || observation.ObservedAt.After(h.now()) {
		return spec, Observation{}, ErrInvalid
	}
	return spec, observation, nil
}

// Fence reads the Device's fence report, creating its report handoff on
// first use. A missing, stale or foreign report is not a report.
func (h *Hub) Fence(ctx context.Context, device string) (custody.SiteFence, bool, error) {
	object, err := h.ensure(ctx, ReportName(device), RoleReport, Spec{Cell: h.Cell, Device: device})
	if err != nil {
		return custody.SiteFence{}, false, err
	}
	spec, observation, err := h.reflected(object)
	if err != nil || spec.Device != device || observation.AssignmentDigest != "" || h.now().Sub(observation.ObservedAt) > h.MaxAge {
		return custody.SiteFence{}, false, nil
	}
	return custody.SiteFence{Seen: observation.Fence.Seen, Stopped: observation.Fence.Stopped}, true, nil
}

// DeliveryMethods reads the site's delivery declaration from the Device's
// report handoff. A missing, stale or foreign report, or one with no valid
// declaration, is no declaration: the coordinator then decides nothing.
func (h *Hub) DeliveryMethods(ctx context.Context, device string) ([]custody.OfferedMethod, bool, error) {
	object, err := h.ensure(ctx, ReportName(device), RoleReport, Spec{Cell: h.Cell, Device: device})
	if err != nil {
		return nil, false, err
	}
	spec, observation, err := h.reflected(object)
	if err != nil || spec.Device != device || observation.AssignmentDigest != "" || h.now().Sub(observation.ObservedAt) > h.MaxAge || observation.Delivery == nil {
		return nil, false, nil
	}
	return observation.Delivery.Methods, true, nil
}

// Results returns each unanswered assignment's reflected result. A result
// counts only when it is for exactly the assignment the handoff carries.
func (h *Hub) Results(ctx context.Context) ([]site.Stored, error) {
	list, err := h.api().List(ctx, metav1.ListOptions{LabelSelector: CellLabel + "=" + h.Cell + "," + RoleLabel + "=" + RoleAssignment + "," + AnsweredLabel + "!=true"})
	if err != nil {
		return nil, err
	}
	var out []site.Stored
	for index := range list.Items {
		spec, observation, err := h.reflected(&list.Items[index])
		if err != nil || observation.Result == nil || observation.AssignmentDigest != Digest(spec.AssignmentJSON) {
			continue
		}
		var assignment site.Assignment
		if err := json.Unmarshal([]byte(spec.AssignmentJSON), &assignment); err != nil || assignment.Device != spec.Device || list.Items[index].GetName() != AssignmentName(assignment) {
			continue
		}
		result := *observation.Result
		if result.Operation != assignment.Operation || result.Fence != assignment.Fence || spec.Device != namespaceOf(spec.Device)+"/"+result.Device {
			continue
		}
		out = append(out, site.Stored{Device: spec.Device, Result: result})
	}
	return out, nil
}

func namespaceOf(device string) string {
	for index := range device {
		if device[index] == '/' {
			return device[:index]
		}
	}
	return ""
}

// Forget marks the assignment answered, so the cell deletes its copy.
func (h *Hub) Forget(ctx context.Context, stored site.Stored) error {
	name := AssignmentName(site.Assignment{Device: stored.Device, Operation: stored.Result.Operation, Fence: stored.Result.Fence})
	patch := []byte(`{"metadata":{"labels":{"` + AnsweredLabel + `":"true"}}}`)
	_, err := h.api().Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
