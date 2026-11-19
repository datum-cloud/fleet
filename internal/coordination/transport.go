// Package coordination implements the experimental management -> Karmada ->
// cell executor transport. It is not a production admission authority.
package coordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"time"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var Resource = schema.GroupVersionResource{Group: "coordination.fleet.datumapis.com", Version: "v1alpha1", Resource: "machineallocationhandoffs"}

const Kind = "MachineAllocationHandoff"

// Intent is immutable. Request retains the allocator's exact operation, asset,
// allocation, epoch, fence and inputs before federation may deliver any effect.
type Intent struct {
	Cell    string            `json:"cell"`
	Request simulator.Request `json:"-"`
}

// Encode exact request bytes as a string so an API server cannot reorder opaque
// input object fields and silently change the executor's retained digest.
func (i Intent) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(i.Request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Cell        string `json:"cell"`
		RequestJSON string `json:"requestJSON"`
	}{i.Cell, string(b)})
}
func (i *Intent) UnmarshalJSON(b []byte) error {
	var wire struct {
		Cell        string `json:"cell"`
		RequestJSON string `json:"requestJSON"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	i.Cell = wire.Cell
	return json.Unmarshal([]byte(wire.RequestJSON), &i.Request)
}

type Observation struct {
	Cell         string                    `json:"cell"`
	IntentDigest string                    `json:"intentDigest"`
	ObservedAt   time.Time                 `json:"observedAt"`
	Result       simulator.OperationResult `json:"result"`
}
type Reflected struct {
	ClusterName string      `json:"clusterName"`
	Observation Observation `json:"observation"`
}
type Status struct {
	Observation *Observation `json:"observation,omitempty"`
	Reflected   []Reflected  `json:"reflected,omitempty"`
}

func Name(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "op-" + hex.EncodeToString(sum[:])[:40]
}
func Digest(i Intent) string {
	b, _ := json.Marshal(i)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (i Intent) Validate() error {
	r := i.Request
	if i.Cell == "" || r.ID == "" || r.AssetID == "" || r.BindingID == "" || r.Epoch < 1 || r.FenceToken == "" {
		return errors.New("incomplete retained execution authority")
	}
	switch r.Kind {
	case "install", "cleanup", "power":
	default:
		return errors.New("unsupported operation kind")
	}
	_, err := simulator.RequestDigest(r)
	return err
}
func decode(obj *unstructured.Unstructured) (Intent, Status, error) {
	var i Intent
	var s Status
	b, err := json.Marshal(obj.Object["spec"])
	if err != nil {
		return i, s, err
	}
	if err = json.Unmarshal(b, &i); err != nil {
		return i, s, err
	}
	b, err = json.Marshal(obj.Object["status"])
	if err != nil {
		return i, s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return i, s, err
	}
	return i, s, i.Validate()
}
func object(ns string, i Intent) *unstructured.Unstructured {
	b, _ := json.Marshal(i)
	var spec map[string]interface{}
	_ = json.Unmarshal(b, &spec)
	return &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": Resource.GroupVersion().String(), "kind": Kind, "metadata": map[string]interface{}{"name": Name(i.Request.ID), "namespace": ns, "labels": map[string]interface{}{CellLabel: i.Cell}}, "spec": spec}}
}

// ValidateResult never interprets an incomplete/uncertain result as success.
func ValidateResult(i Intent, r simulator.OperationResult) error {
	d, err := simulator.RequestDigest(i.Request)
	if err != nil || r.ID != i.Request.ID || r.Kind != i.Request.Kind || r.InputDigest != d || r.Phase != "Succeeded" || r.CompletedAt == nil || r.CreatedAt.IsZero() || r.CompletedAt.Before(r.CreatedAt) {
		return executor.ErrOutcomeUnknown
	}
	switch r.Kind {
	case "install":
		if r.Result != "installed" {
			return executor.ErrOutcomeUnknown
		}
	case "cleanup":
		if r.Result != "clean" {
			return executor.ErrOutcomeUnknown
		}
	}
	return nil
}
func ValidateReflection(i Intent, s Status, now time.Time, maxAge time.Duration) (simulator.OperationResult, error) {
	// ClusterName is supplied by Karmada's status aggregator, independently of
	// the cell-reported identity. Never take the first of conflicting replicas.
	if i.Validate() != nil || maxAge <= 0 || len(s.Reflected) != 1 {
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	r := s.Reflected[0]
	o := r.Observation
	if r.ClusterName != i.Cell || o.Cell != i.Cell || o.IntentDigest != Digest(i) || o.ObservedAt.IsZero() || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > maxAge {
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	if err := ValidateResult(i, o.Result); err != nil {
		return simulator.OperationResult{}, err
	}
	if o.Result.CompletedAt.After(o.ObservedAt) {
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	return o.Result, nil
}

// Hub implements executor.Client. Only management holds this client. It writes
// immutable intent, reads federation-owned status, and never writes hub status.
// The caller must have retained lifecycle authority before Execute.
type Hub struct {
	API             dynamic.Interface
	Namespace, Cell string
	MaxAge          time.Duration
	Now             func() time.Time
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}
func (h *Hub) Execute(ctx context.Context, r simulator.Request) (simulator.OperationResult, error) {
	if h.API == nil || h.Namespace == "" {
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	i := Intent{Cell: h.Cell, Request: r}
	if err := i.Validate(); err != nil {
		return simulator.OperationResult{}, err
	}
	api := h.API.Resource(Resource).Namespace(h.Namespace)
	obj, err := api.Create(ctx, object(h.Namespace, i), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		obj, err = api.Get(ctx, Name(r.ID), metav1.GetOptions{})
	}
	if err != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: handoff persistence unavailable", executor.ErrOutcomeUnknown)
	}
	retained, status, err := decode(obj)
	if err != nil || Digest(retained) != Digest(i) {
		return simulator.OperationResult{}, executor.ErrConflict
	}
	return ValidateReflection(retained, status, h.now(), h.MaxAge)
}
func (h *Hub) Lookup(ctx context.Context, id string) (simulator.OperationResult, bool, error) {
	if h.API == nil || h.Namespace == "" || id == "" {
		return simulator.OperationResult{}, false, executor.ErrOutcomeUnknown
	}
	obj, err := h.API.Resource(Resource).Namespace(h.Namespace).Get(ctx, Name(id), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return simulator.OperationResult{}, false, nil
	}
	if err != nil {
		return simulator.OperationResult{}, false, err
	}
	i, s, err := decode(obj)
	if err != nil || i.Request.ID != id || i.Cell != h.Cell {
		return simulator.OperationResult{}, true, executor.ErrConflict
	}
	r, err := ValidateReflection(i, s, h.now(), h.MaxAge)
	return r, true, err
}

// Cell is the selected location's view of delivered intent. Site execution is
// split by least privilege: a per-operation worker Job calls Execute and holds
// only read access to its one handoff; the site observer calls Publish and is
// the only writer of handoff status. Publish derives every observation from
// the provider journal, never from Job, Pod or KRO status.
type Cell struct {
	API             dynamic.Interface
	Namespace, Name string
	Executor        executor.Client
	Now             func() time.Time
}

// intent reads and validates one delivered handoff for this cell.
func (c *Cell) intent(ctx context.Context, name string) (*unstructured.Unstructured, Intent, error) {
	if c.API == nil || c.Executor == nil || c.Namespace == "" || c.Name == "" {
		return nil, Intent{}, executor.ErrOutcomeUnknown
	}
	obj, err := c.API.Resource(Resource).Namespace(c.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, Intent{}, err
	}
	i, _, err := decode(obj)
	if err != nil {
		return nil, Intent{}, fmt.Errorf("%w: invalid delivered intent", executor.ErrInvalid)
	}
	if i.Cell != c.Name || name != Name(i.Request.ID) || obj.GetDeletionTimestamp() != nil {
		return nil, Intent{}, executor.ErrFenced
	}
	return obj, i, nil
}

// Execute runs or resumes exactly the delivered operation and returns nil only
// for a validated terminal provider result. It never writes status.
//
// Lookup Unknown means the provider retained this exact operation but has not
// reached a terminal result. Resuming re-delivers the identical retained
// request under the same operation ID, which the executor contract replays
// against its own journal (digest-checked, never a new operation). Conflict,
// fencing and transport errors stop before any effect.
func (c *Cell) Execute(ctx context.Context, name string) error {
	_, i, err := c.intent(ctx, name)
	if err != nil {
		return err
	}
	r, found, err := c.Executor.Lookup(ctx, i.Request.ID)
	if err != nil && (found || !errors.Is(err, executor.ErrOutcomeUnknown)) {
		return err
	}
	if !found {
		if r, err = c.Executor.Execute(ctx, i.Request); err != nil {
			return err
		}
	}
	return ValidateResult(i, r)
}

// Publish writes a fresh observation only for a validated terminal journal
// result. It never executes; absent or unfinished results stay Unknown.
func (c *Cell) Publish(ctx context.Context, name string) error {
	obj, i, err := c.intent(ctx, name)
	if err != nil {
		return err
	}
	r, found, err := c.Executor.Lookup(ctx, i.Request.ID)
	if err != nil {
		return err
	}
	if !found {
		return executor.ErrOutcomeUnknown
	}
	if err = ValidateResult(i, r); err != nil {
		return err
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	o := Observation{Cell: c.Name, IntentDigest: Digest(i), ObservedAt: now, Result: r}
	b, _ := json.Marshal(Status{Observation: &o})
	var status map[string]interface{}
	_ = json.Unmarshal(b, &status)
	obj.Object["status"] = status
	_, err = c.API.Resource(Resource).Namespace(c.Namespace).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	return err
}
