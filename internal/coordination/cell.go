package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"go.datum.net/fleet/internal/custody"
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
	// Delivery reads the site enrollment's delivery declaration, once per
	// observer pass. An error publishes no declaration.
	Delivery func() ([]custody.OfferedMethod, error)
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
	_, err = runner.Run(ctx, assignment)
	return err
}

// declaration reads the delivery declaration for one pass, or nil.
func (c *Cell) declaration() (*DeliveryDeclaration, error) {
	if c.Delivery == nil {
		return nil, errors.New("coordination: the cell has no delivery declaration")
	}
	methods, err := c.Delivery()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(methods)
	if err != nil {
		return nil, err
	}
	return &DeliveryDeclaration{Methods: string(data)}, nil
}

// Publish writes the observation of one handoff: the Device's stored fences,
// the site's delivery declaration on a report, and, for an assignment, its
// stored result. It deletes the site's copy of a result the coordinator
// answered.
func (c *Cell) Publish(ctx context.Context, object *unstructured.Unstructured, store *site.Store, results *site.Results) error {
	declared, _ := c.declaration()
	return c.publish(ctx, object, store, results, declared)
}

func (c *Cell) publish(ctx context.Context, object *unstructured.Unstructured, store *site.Store, results *site.Results, declared *DeliveryDeclaration) error {
	observation := Observation{Cell: c.Name, ObservedAt: c.now()}
	var device string
	switch object.GetLabels()[RoleLabel] {
	case RoleAssignment:
		assignment, spec, err := c.assignment(object)
		if err != nil {
			return err
		}
		device, observation.AssignmentDigest = assignment.Device, Digest(spec.AssignmentJSON)
		result, found, err := results.Get(assignment.Device, assignment.Operation.UID, assignment.Fence)
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
		device, observation.Delivery = spec.Device, declared
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
			// One read per pass, so an invalid declaration is reported once.
			declared, err := c.declaration()
			if err != nil {
				onError("delivery declaration", err)
			}
			for index := range list.Items {
				if err := c.publish(ctx, &list.Items[index], store, results, declared); err != nil {
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
