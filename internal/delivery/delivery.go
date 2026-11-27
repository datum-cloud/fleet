// Package delivery carries admitted operations from custody to the sites
// that serve each Device, and carries their results back. It is the
// coordinator's side of the federation layer. Delivery is replayed from
// custody after any crash, so a site sees each assignment at least once and
// ignores a fence it has already passed.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

// Site is the coordinator's view of one remote site.
type Site interface {
	// Deliver hands the site one assignment. It is idempotent.
	Deliver(ctx context.Context, assignment site.Assignment) error
	// Fence returns the site's fence report for a "<namespace>/<name>"
	// Device, and false if the site has not reported one.
	Fence(ctx context.Context, device string) (custody.SiteFence, bool, error)
	// Results returns the results the site holds.
	Results(ctx context.Context) ([]site.Stored, error)
	// Forget tells the site the coordinator answered a result.
	Forget(ctx context.Context, stored site.Stored) error
}

// Sites is the coordinator's explicit site configuration: which sites serve
// which Device. Inventory does not yet record a Device's sites, so this is
// configuration, never inferred from placement.
type Sites struct {
	Sites map[string]Site
	// Devices maps a Device name to the names of the sites serving it.
	Devices map[string][]string
	// Methods is each site's declared boot delivery methods, in its order
	// of preference. A site with none offers none.
	Methods map[string][]custody.OfferedMethod
}

func (s *Sites) DeliveryMethods(name string) []custody.OfferedMethod {
	return slices.Clone(s.Methods[name])
}

var _ custody.Sites = (*Sites)(nil)

func (s *Sites) Serving(device *inventory.Device) []string {
	var out []string
	for _, name := range s.Devices[device.Name] {
		if _, ok := s.Sites[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

func (s *Sites) Fence(ctx context.Context, name string, device *inventory.Device) (custody.SiteFence, bool, error) {
	remote, ok := s.Sites[name]
	if !ok {
		return custody.SiteFence{}, false, nil
	}
	return remote.Fence(ctx, device.Namespace+"/"+device.Name)
}

// Assignment builds the assignment for the active operation from custody
// and the operation's spec, exactly as admitted.
func Assignment(state *ops.DeviceCustody, operation ops.Operation) (site.Assignment, error) {
	active := state.Spec.ActiveOperation
	if active == nil || string(operation.GetUID()) != active.UID || operation.GetName() != active.Name {
		return site.Assignment{}, errors.New("delivery: the operation is not custody's active operation")
	}
	var object struct {
		Spec json.RawMessage `json:"spec"`
	}
	data, err := json.Marshal(operation)
	if err != nil {
		return site.Assignment{}, err
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return site.Assignment{}, err
	}
	return site.Assignment{
		Device:      state.Namespace + "/" + state.Name,
		Operation:   active.OperationReference,
		Fence:       active.Fence,
		AdmittedAt:  active.AdmittedAt.Time,
		Deadline:    active.Deadline.Time,
		Snapshot:    active.Snapshot,
		InputDigest: active.InputDigest,
		Inputs:      object.Spec,
	}, nil
}

// Courier delivers each Device's active operation to its sites and reports
// their results to the coordinator.
type Courier struct {
	// Client reads custody and operations. It must read through to the API
	// server.
	Client      client.Reader
	Registry    *custody.Registry
	Coordinator *custody.Coordinator
	Sites       *Sites
	Namespace   string
}

// Deliver sends a Device's active operation to every site serving it, once
// its admission is copied to the operation's status.
func (c *Courier) Deliver(ctx context.Context, name string) error {
	state := &ops.DeviceCustody{}
	if err := c.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, state); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	active := state.Spec.ActiveOperation
	if active == nil {
		return nil
	}
	kind := c.Registry.Kind(active.Kind)
	if kind == nil {
		return fmt.Errorf("delivery: custody names unregistered kind %s", active.Kind)
	}
	operation := kind.New()
	if err := c.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: active.Name}, operation); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	status := operation.OperationStatus()
	if string(operation.GetUID()) != active.UID || status.Fence != active.Fence || status.Phase == ops.PhasePending || status.Phase.Ended() {
		return nil
	}
	assignment, err := Assignment(state, operation)
	if err != nil {
		return err
	}
	device := &inventory.Device{}
	if err := c.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, device); err != nil {
		return err
	}
	var failed error
	for _, serving := range c.Sites.Serving(device) {
		if err := c.Sites.Sites[serving].Deliver(ctx, assignment); err != nil {
			failed = errors.Join(failed, fmt.Errorf("deliver to %s: %w", serving, err))
		}
	}
	return failed
}

// Collect reports every site's held results to the coordinator, and tells
// the site to forget each one the coordinator accepted or rejected as stale.
func (c *Courier) Collect(ctx context.Context) error {
	var failed error
	names := make([]string, 0, len(c.Sites.Sites))
	for name := range c.Sites.Sites {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		remote := c.Sites.Sites[name]
		held, err := remote.Results(ctx)
		if err != nil {
			failed = errors.Join(failed, fmt.Errorf("results from %s: %w", name, err))
			continue
		}
		for _, stored := range held {
			// The site names itself; the coordinator records the site it
			// asked.
			stored.Result.Site = name
			if err := c.Coordinator.AcceptResult(ctx, stored.Result); err != nil && !errors.Is(err, custody.ErrStaleFence) {
				failed = errors.Join(failed, err)
				continue
			}
			if err := remote.Forget(ctx, stored); err != nil {
				failed = errors.Join(failed, err)
			}
		}
	}
	return failed
}

// Loop is the coordinator process: it reconciles every Device's custody,
// delivers active operations, collects results and sweeps retention.
type Loop struct {
	Client      client.Reader
	Coordinator *custody.Coordinator
	Courier     *Courier
	Namespace   string
	// Interval is the time between passes. It defaults to one second.
	Interval time.Duration
	// OnError reports a pass's errors. The loop keeps running.
	OnError func(error)
	// lastSweep is when retention last ran.
	lastSweep time.Time
}

// maxSteps bounds the reconciles of one Device in one pass.
const maxSteps = 32

// Pass reconciles each Device until it settles, delivers, and collects.
func (l *Loop) Pass(ctx context.Context) error {
	names := map[string]bool{}
	var devices inventory.DeviceList
	if err := l.Client.List(ctx, &devices, client.InNamespace(l.Namespace)); err != nil {
		return err
	}
	for _, device := range devices.Items {
		names[device.Name] = true
	}
	var custodies ops.DeviceCustodyList
	if err := l.Client.List(ctx, &custodies, client.InNamespace(l.Namespace)); err != nil {
		return err
	}
	for _, state := range custodies.Items {
		names[state.Name] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	var failed error
	for _, name := range sorted {
		if err := l.Settle(ctx, name); err != nil {
			failed = errors.Join(failed, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if err := l.Courier.Deliver(ctx, name); err != nil {
			failed = errors.Join(failed, fmt.Errorf("%s: %w", name, err))
		}
	}
	if err := l.Courier.Collect(ctx); err != nil {
		failed = errors.Join(failed, err)
	}
	if time.Since(l.lastSweep) > time.Hour {
		if _, err := l.Coordinator.SweepRetention(ctx); err != nil {
			failed = errors.Join(failed, err)
		} else {
			l.lastSweep = time.Now()
		}
	}
	return failed
}

// Settle reconciles one Device until a reconcile changes nothing.
func (l *Loop) Settle(ctx context.Context, name string) error {
	for range maxSteps {
		outcome, err := l.Coordinator.Reconcile(ctx, name)
		if err != nil || !outcome.Changed {
			return err
		}
	}
	return nil
}

// Run makes passes until the context ends.
func (l *Loop) Run(ctx context.Context) error {
	interval := l.Interval
	if interval <= 0 {
		interval = time.Second
	}
	for {
		if err := l.Pass(ctx); err != nil && ctx.Err() == nil && l.OnError != nil {
			l.OnError(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
