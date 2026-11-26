package coordination

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"go.datum.net/fleet/internal/site"
)

// ErrNotDelivered means the handoff is not an assignment delivered to this
// cell under its own name, or it is being deleted. The worker sends nothing.
var ErrNotDelivered = errors.New("coordination: the handoff is not an assignment for this cell")

// Cell is one cell's view of its delivered handoffs. Execution is split by
// least privilege: a per-operation worker Job runs one assignment and can
// read only its own handoff; the observer is the only writer of handoff
// status, and reports only what the site store holds.
type Cell struct {
	API       dynamic.Interface
	Namespace string
	Name      string
	Now       func() time.Time
}

func (c *Cell) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Cell) api() dynamic.ResourceInterface {
	return c.API.Resource(Resource).Namespace(c.Namespace)
}

// assignment validates one delivered assignment handoff.
func (c *Cell) assignment(object *unstructured.Unstructured) (site.Assignment, Spec, error) {
	spec, err := decode(object, nil)
	if err != nil || spec.Cell != c.Name || object.GetLabels()[CellLabel] != c.Name || object.GetLabels()[RoleLabel] != RoleAssignment || object.GetDeletionTimestamp() != nil {
		return site.Assignment{}, spec, ErrNotDelivered
	}
	var assignment site.Assignment
	if err := json.Unmarshal([]byte(spec.AssignmentJSON), &assignment); err != nil || assignment.Device != spec.Device || object.GetName() != AssignmentName(assignment) || spec.Credential != site.CredentialSecretName(assignment.Device) {
		return site.Assignment{}, spec, ErrNotDelivered
	}
	// The handoff names a payload object exactly when the admitted inputs
	// name a payload, and only under its derived name.
	want := ""
	if assignment.Sealed() != nil {
		want = PayloadName(assignment)
	}
	if spec.Payload != want {
		return site.Assignment{}, spec, ErrNotDelivered
	}
	return assignment, spec, nil
}

// Work runs the named handoff's assignment through the site runner. A
// DeviceRecovery only raises the fence and confirms the older worker
// stopped. Work never writes status.
func (c *Cell) Work(ctx context.Context, name string, runner *site.Runner) error {
	object, err := c.api().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	assignment, _, err := c.assignment(object)
	if err != nil {
		return err
	}
	if assignment.Operation.Kind == "DeviceRecovery" {
		return runner.Fence(ctx, assignment)
	}
	// Federation can deliver the handoff before the payload object. The
	// worker exits to be retried, and stores no result, until it arrives.
	if assignment.Sealed() != nil {
		if runner.Payloads == nil {
			return ErrNotDelivered
		}
		payload, err := runner.Payloads.Get(ctx, assignment)
		if err != nil {
			return err
		}
		if payload == nil {
			return ErrPayloadPending
		}
	}
	_, err = runner.Run(ctx, assignment)
	return err
}

// ErrPayloadPending means the assignment's payload object has not reached
// the cell yet. The worker sends nothing and is retried.
var ErrPayloadPending = errors.New("coordination: the bootstrap payload has not reached the cell")

// HandoffPayloads reads an assignment's payload from the one
// MachineAllocationPayload federation delivered for it. The worker may get
// exactly that object, and may list, watch, change or delete none. The cell
// keeps the payload only until the coordinator answers the result: the hub
// then deletes its object, and federation deletes the cell's copy. So
// Delete and Prune have nothing a worker can do.
type HandoffPayloads struct {
	API       dynamic.Interface
	Namespace string
	Cell      string
}

var _ site.Payloads = HandoffPayloads{}

func (s HandoffPayloads) Get(ctx context.Context, assignment site.Assignment) (*site.Payload, error) {
	object, err := s.API.Resource(PayloadResource).Namespace(s.Namespace).Get(ctx, PayloadName(assignment), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if cell, _, _ := unstructured.NestedString(object.Object, "spec", "cell"); cell != s.Cell || object.GetLabels()[CellLabel] != s.Cell || object.GetLabels()[RoleLabel] != RolePayload {
		return nil, ErrNotDelivered
	}
	// A payload that does not decode was not delivered by the hub; the
	// worker refuses the handoff rather than retry it.
	encoded, _, _ := unstructured.NestedString(object.Object, "spec", "payload")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrNotDelivered
	}
	var payload site.Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, ErrNotDelivered
	}
	return &payload, nil
}

func (HandoffPayloads) Delete(context.Context, site.Assignment) error { return nil }

func (HandoffPayloads) Prune(context.Context, string) error { return nil }

// Publish writes the observation of one handoff: the Device's stored fences
// and, for an assignment, its stored result. It deletes the site's copy of a
// result the coordinator answered.
func (c *Cell) Publish(ctx context.Context, object *unstructured.Unstructured, store *site.Store, results *site.Results) error {
	observation := Observation{Cell: c.Name, ObservedAt: c.now()}
	var device string
	switch object.GetLabels()[RoleLabel] {
	case RoleAssignment:
		assignment, spec, err := c.assignment(object)
		if err != nil {
			return err
		}
		device, observation.AssignmentDigest = assignment.Device, Digest(spec.AssignmentJSON)
		result, found, err := results.Get(assignment.Device, assignment.Operation, assignment.Fence)
		if err != nil {
			return err
		}
		if found && object.GetLabels()[AnsweredLabel] == "true" {
			if err := results.Forget(assignment.Device, result); err != nil {
				return err
			}
		} else if found {
			observation.Result = &result
		}
	case RoleReport:
		spec, err := decode(object, nil)
		if err != nil || spec.Cell != c.Name || object.GetLabels()[CellLabel] != c.Name || spec.AssignmentJSON != "" || spec.Credential != "" || object.GetName() != ReportName(spec.Device) {
			return ErrNotDelivered
		}
		device = spec.Device
	default:
		return ErrNotDelivered
	}
	seen, stopped, err := store.Peek(device)
	if err != nil {
		return err
	}
	observation.Fence = FenceReport{Seen: seen, Stopped: stopped}
	data, err := json.Marshal(CellStatus{Observation: &observation})
	if err != nil {
		return err
	}
	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		return err
	}
	object.Object["status"] = status
	_, err = c.api().UpdateStatus(ctx, object, metav1.UpdateOptions{})
	return err
}

// Observe republishes every handoff of this cell on a fixed interval, so the
// coordinator can check that fence reports are fresh. onError must not log
// handoff contents.
func (c *Cell) Observe(ctx context.Context, interval time.Duration, store *site.Store, results *site.Results, onError func(string, error)) error {
	for {
		list, err := c.api().List(ctx, metav1.ListOptions{LabelSelector: CellLabel + "=" + c.Name})
		if err != nil {
			onError("", err)
		} else {
			for index := range list.Items {
				if err := c.Publish(ctx, &list.Items[index], store, results); err != nil {
					onError(list.Items[index].GetName(), err)
				}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
