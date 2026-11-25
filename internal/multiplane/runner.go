// Package multiplane is an explicit local acceptance composition of the shared
// allocator on Device custody, real Milo project APIs, and the Karmada
// federation transport. Admission, quota, catalog publication and discovery
// are synthetic.
package multiplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	rm "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/coordination"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/delivery"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/site/fixture"
)

const Namespace = "default"

// DeviceNamespace holds the provider project's Devices, capacities,
// allocations, custody and operations.
const DeviceNamespace = "fleet-provider"

// Device is the one Device the acceptance allocates.
const Device = "synthetic-device-central-01"

// Location is the only location this acceptance composition serves. It is bound
// to exactly one Karmada member, Config.Cell, and to the one local capacity
// fixture. There is no location catalog: any other Machine locationRef is
// rejected before any allocation or finalizer, so a request for elsewhere
// can never reserve or effect local hardware.
const Location = "local"

var ErrLocationNotServed = errors.New("machine location is not served by the configured cell")

// errPending means a phase's effect has not finished; the phase is retried.
var errPending = errors.New("not finished yet")

type Config struct {
	State, HubKubeconfig, Cell, Image string
	// Enrollment is the cell's fixture enrollment of each Device it serves.
	Enrollment fixture.Enrollment
}

type Project struct {
	API      client.Client
	Identity controller.ProjectIdentity
}

type Runner struct {
	Config    Config
	Provider  Project
	Consumers [2]Project
	Allocator *allocation.Allocator
	Hub       *coordination.Hub
}

func Scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = core.AddToScheme(s)
	_ = fleet.AddToScheme(s)
	_ = inventory.AddToScheme(s)
	_ = ops.AddToScheme(s)
	_ = rm.AddToScheme(s)
	return s
}

func restConfig(path string) (*rest.Config, error) {
	c, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, err
	}
	c.Timeout = 30 * time.Second
	return c, nil
}

func API(path string) (client.Client, error) {
	c, err := restConfig(path)
	if err != nil {
		return nil, err
	}
	return client.New(c, client.Options{Scheme: Scheme()})
}

// Principals are the provider project's Fleet identities, one per component.
// The local Milo authenticates each with its own static token.
func principals(ctx context.Context, state string) (custody.Principals, ops.Creator, ops.Creator, error) {
	provider, err := identity(ctx, filepath.Join(state, "provider", "management-project.json"))
	if err != nil {
		return nil, ops.Creator{}, ops.Creator{}, err
	}
	release, err := identity(ctx, filepath.Join(state, "provider", "release-project.json"))
	if err != nil {
		return nil, ops.Creator{}, ops.Creator{}, err
	}
	p := custody.Principals{custody.CreatorMetalProvider: {{Username: provider.Username, UID: provider.UID}}, custody.CreatorReleaseController: {{Username: release.Username, UID: release.UID}}}
	return p, provider, release, p.Validate()
}

func identity(ctx context.Context, path string) (ops.Creator, error) {
	c, err := restConfig(path)
	if err != nil {
		return ops.Creator{}, err
	}
	return allocation.Creator(ctx, c)
}

// coordinator builds the custody coordinator with the Karmada hub as the
// one site serving the Device.
func coordinator(ctx context.Context, c Config, registry *custody.Registry) (*custody.Coordinator, *coordination.Hub, *delivery.Sites, client.Client, error) {
	api, err := API(filepath.Join(c.State, "provider", "coordinator-project.json"))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	hubConfig, err := restConfig(c.HubKubeconfig)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	dynamicHub, err := dynamic.NewForConfig(hubConfig)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	hub := &coordination.Hub{API: dynamicHub, Namespace: "fleet-coordination", Cell: c.Cell, MaxAge: time.Minute}
	sites := &delivery.Sites{Sites: map[string]delivery.Site{c.Cell: hub}, Devices: map[string][]string{}}
	for key := range c.Enrollment {
		namespace, name, _ := strings.Cut(key, "/")
		if namespace == DeviceNamespace {
			sites.Devices[name] = []string{c.Cell}
		}
	}
	p, _, _, err := principals(ctx, c.State)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return &custody.Coordinator{Client: api, Registry: registry, Sites: sites, Config: custody.Config{Namespace: DeviceNamespace, Principals: p}}, hub, sites, api, nil
}

// Coordinate is the coordinator process: the custody loop, delivering
// through Karmada, and fixture discovery for the cell's Devices under the
// discovery identity.
// A positive installationDeadline replaces the installation kind's deadline
// for operations this process admits; the deadline scenario uses it.
func Coordinate(ctx context.Context, c Config, installationDeadline time.Duration, onError func(error)) error {
	registry, err := custody.DefaultRegistry()
	if err != nil {
		return err
	}
	if installationDeadline > 0 {
		registry.Kind("DeviceInstallation").Deadline = installationDeadline
	}
	coord, _, sites, api, err := coordinator(ctx, c, registry)
	if err != nil {
		return err
	}
	discovery, err := API(filepath.Join(c.State, "provider", "discovery-project.json"))
	if err != nil {
		return err
	}
	loop := &delivery.Loop{Client: api, Coordinator: coord, Namespace: DeviceNamespace, OnError: onError,
		Courier: &delivery.Courier{Client: api, Registry: registry, Coordinator: coord, Sites: sites, Namespace: DeviceNamespace}}
	go fixture.Run(ctx, discovery, c.Enrollment, 30*time.Second, func(err error) { onError(fmt.Errorf("fixture discovery: %w", err)) })
	return loop.Run(ctx)
}

func New(ctx context.Context, c Config) (*Runner, error) {
	r := &Runner{Config: c}
	registry, err := custody.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	coord, hub, _, _, err := coordinator(ctx, c, registry)
	if err != nil {
		return nil, err
	}
	r.Hub = hub
	_, providerCreator, releaseCreator, err := principals(ctx, c.State)
	if err != nil {
		return nil, err
	}
	for index, name := range []string{"provider", "consumer-a", "consumer-b"} {
		root, err := API(filepath.Join(c.State, name, "management-root.json"))
		if err != nil {
			return nil, err
		}
		identity, err := manager.ResolveProjectIdentity(ctx, root, "local-milo", name)
		if err != nil {
			return nil, err
		}
		project, err := API(filepath.Join(c.State, name, "management-project.json"))
		if err != nil {
			return nil, err
		}
		p := Project{API: project, Identity: identity}
		if index == 0 {
			r.Provider = p
		} else {
			r.Consumers[index-1] = p
		}
	}
	releaser, err := API(filepath.Join(c.State, "provider", "release-project.json"))
	if err != nil {
		return nil, err
	}
	r.Allocator = &allocation.Allocator{
		Provider: r.Provider.API, ProviderCreator: providerCreator,
		Releaser: releaser, ReleaseCreator: releaseCreator,
		Custody: coord, Namespace: DeviceNamespace,
		Cleaning: func(name string) (string, error) {
			if name != "local-cleaning" {
				return "", fmt.Errorf("no fixture cleaning policy %q", name)
			}
			return custody.SpecDigest(`{"method":"overwrite","disks":"all"}`)
		},
		BootInterface: "primary", TargetDisks: []string{"disk-0"},
	}
	return r, nil
}

func condition(kind string, status metav1.ConditionStatus, generation int64, reason string) metav1.Condition {
	return metav1.Condition{Type: kind, Status: status, ObservedGeneration: generation, Reason: reason, Message: "Local multi-plane acceptance; synthetic IAM, quota, catalog and networkless fixtures", LastTransitionTime: metav1.Now()}
}

func ensure(ctx context.Context, c client.Client, obj client.Object) error {
	before, _ := json.Marshal(obj)
	var want map[string]interface{}
	_ = json.Unmarshal(before, &want)
	err := c.Create(ctx, obj)
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		return err
	}
	after, _ := json.Marshal(obj)
	var got map[string]interface{}
	_ = json.Unmarshal(after, &got)
	w, _ := json.Marshal(want["spec"])
	g, _ := json.Marshal(got["spec"])
	if string(w) != string(g) || obj.GetDeletionTimestamp() != nil {
		return errors.New("existing fixture differs; refusing replacement")
	}
	return nil
}

// request is one consumer's Machine, resolved against the provider's
// published class and profile. Location is checked before anything else.
type request struct {
	project Project
	machine *fleet.Machine
	alloc   allocation.Request
}

func (r *Runner) request(ctx context.Context, index int) (*request, error) {
	p := r.Consumers[index]
	var m fleet.Machine
	var class, localClass fleet.MachineClass
	var profile, localProfile fleet.MachineProfile
	objects := []struct {
		api client.Client
		key client.ObjectKey
		obj client.Object
	}{{p.API, client.ObjectKey{Namespace: Namespace, Name: "allocation"}, &m}, {r.Provider.API, client.ObjectKey{Name: "local-class"}, &class}, {r.Provider.API, client.ObjectKey{Name: "local-profile"}, &profile}, {p.API, client.ObjectKey{Name: "local-class"}, &localClass}, {p.API, client.ObjectKey{Name: "local-profile"}, &localProfile}}
	for _, o := range objects {
		if err := o.api.Get(ctx, o.key, o.obj); err != nil {
			return nil, err
		}
	}
	if m.Spec.LocationRef.Name != Location || r.Config.Cell == "" || r.Hub != nil && r.Hub.Cell != r.Config.Cell {
		return nil, fmt.Errorf("%w: %q", ErrLocationNotServed, m.Spec.LocationRef.Name)
	}
	if localClass.Spec.Source == nil || localClass.Spec.Source.Project != "provider" || localClass.Spec.Source.Name != class.Name {
		return nil, errors.New("publication source mismatch")
	}
	return &request{project: p, machine: &m, alloc: allocation.Request{
		Machine: allocation.Machine{Project: p.Identity.RouteName, ProjectUID: p.Identity.UID, Namespace: m.Namespace, Name: m.Name, UID: m.UID},
		Class:   &class, Profile: &profile,
		Authority: fleet.AllocationAuthority{Principal: "local-fixture:" + string(p.Identity.UID), Decision: "local-fixture:" + string(m.UID)},
	}}, nil
}

// protect adds the finalizer that keeps the Machine until it is released.
func (q *request) protect(ctx context.Context) error {
	for _, f := range q.machine.Finalizers {
		if f == allocation.MachineFinalizer {
			return nil
		}
	}
	if q.machine.DeletionTimestamp != nil {
		return nil
	}
	q.machine.Finalizers = append(q.machine.Finalizers, allocation.MachineFinalizer)
	return q.project.API.Update(ctx, q.machine)
}

func (r *Runner) Allocate(ctx context.Context) error {
	a, err := r.request(ctx, 0)
	if err != nil {
		return err
	}
	b, err := r.request(ctx, 1)
	if err != nil {
		return err
	}
	if a.project.Identity.SameLifetime(b.project.Identity) {
		return errors.New("consumer control planes do not have distinct identities")
	}
	if err := r.verified(ctx); err != nil {
		return err
	}
	for _, q := range []*request{a, b} {
		if err := q.protect(ctx); err != nil {
			return err
		}
	}
	if _, err := r.Allocator.Allocate(ctx, a.alloc); err != nil {
		return err
	}
	if _, err := r.Allocator.Allocate(ctx, b.alloc); !errors.Is(err, allocation.ErrInsufficientCapacity) {
		return fmt.Errorf("contention must retain one winner: %v", err)
	}
	for index, q := range []*request{a, b} {
		phase, reason := "Bound", "AwaitingVerifiedInstallation"
		status := metav1.ConditionTrue
		if index == 1 {
			phase, reason, status = "Pending", "InsufficientCapacity", metav1.ConditionFalse
		}
		q.machine.Status.Phase = phase
		q.machine.Status.Conditions = []metav1.Condition{condition("Bound", status, q.machine.Generation, reason), condition("Ready", metav1.ConditionFalse, q.machine.Generation, reason)}
		if err := q.project.API.Status().Update(ctx, q.machine); err != nil {
			return err
		}
		if err := r.projectPower(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// verified waits for custody of the Device to be Verified, which needs the
// coordinator process and the cell's fence report.
func (r *Runner) verified(ctx context.Context) error {
	state := &ops.DeviceCustody{}
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: Device}, state); err != nil {
		return fmt.Errorf("%w: custody: %v", errPending, err)
	}
	if state.Spec.State != ops.CustodyVerified {
		return fmt.Errorf("%w: custody is %s", errPending, state.Spec.State)
	}
	return nil
}

func (r *Runner) projectPower(ctx context.Context, q *request) error {
	return manager.ProjectPower(ctx, q.project.API, r.Provider.API, DeviceNamespace, q.alloc.Machine)
}

// installation creates the consumer's DeviceInstallation once and returns
// it with the allocation.
func (r *Runner) installation(ctx context.Context, index int) (*request, *fleet.MachineAllocation, *fleet.Installation, *ops.DeviceInstallation, error) {
	q, err := r.request(ctx, index)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	current, err := r.Allocator.Get(ctx, q.alloc.Machine)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if current == nil || current.Status.Phase != fleet.AllocationActive {
		return nil, nil, nil, nil, errors.New("the consumer has no active allocation")
	}
	var install fleet.Installation
	if err := q.project.API.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "debian-pxe"}, &install); err != nil {
		return nil, nil, nil, nil, err
	}
	operation := &ops.DeviceInstallation{}
	name := custody.InstallationName(current.Name, string(install.UID))
	if err := r.Provider.API.Get(ctx, client.ObjectKey{Namespace: DeviceNamespace, Name: name}, operation); apierrors.IsNotFound(err) {
		previous := ""
		if ref := q.machine.Status.CurrentInstallationRef; ref != nil {
			previous = ref.Name
		}
		if operation, err = r.Allocator.Install(ctx, current, allocation.Installation{Installation: &install, Current: previous}); err != nil {
			return nil, nil, nil, nil, err
		}
	} else if err != nil {
		return nil, nil, nil, nil, err
	}
	return q, current, &install, operation, nil
}

// ErrAdmittedUnknown reports that an installation is admitted and delivered
// but its result is still unknown. The admit-install phase expects it.
var ErrAdmittedUnknown = errors.New("installation admitted; its result remains unknown")

// AdmitInstall creates consumer A's installation and returns
// ErrAdmittedUnknown once custody admitted it and its handoff exists.
func (r *Runner) AdmitInstall(ctx context.Context) error { return r.admitIndex(ctx, 0) }

func (r *Runner) admitIndex(ctx context.Context, index int) error {
	_, _, _, operation, err := r.installation(ctx, index)
	if err != nil {
		return err
	}
	switch phase := operation.Status.Phase; {
	case phase == ops.PhaseSucceeded:
		return errors.New("the installation already completed; run before the cell dispatches")
	case phase.Ended():
		return fmt.Errorf("the installation ended %s: %s", phase, operation.Status.Reason)
	case phase == ops.PhasePending || phase == "":
		return fmt.Errorf("%w: the installation is %s", errPending, phase)
	}
	return ErrAdmittedUnknown
}

func (r *Runner) Install(ctx context.Context) error { return r.InstallIndex(ctx, 0) }

func (r *Runner) InstallIndex(ctx context.Context, index int) error {
	q, _, install, operation, err := r.installation(ctx, index)
	if err != nil {
		return err
	}
	switch phase := operation.Status.Phase; {
	case phase.Ended() && phase != ops.PhaseSucceeded:
		return fmt.Errorf("%w: the installation ended %s: %s", errTerminal, phase, operation.Status.Reason)
	case phase != ops.PhaseSucceeded:
		return fmt.Errorf("%w: the installation is %s", errPending, phase)
	}
	install.Status.Phase = "Ready"
	install.Status.Conditions = []metav1.Condition{condition("Ready", metav1.ConditionTrue, install.Generation, "LocalPXEVerifiedViaKarmada")}
	if err := q.project.API.Status().Update(ctx, install); err != nil {
		return err
	}
	m := q.machine
	m.Status.CurrentInstallationRef = &fleet.LocalReference{Name: install.Name}
	m.Status.Phase = "Ready"
	meta.SetStatusCondition(&m.Status.Conditions, condition("Ready", metav1.ConditionTrue, m.Generation, "LocalPXEVerifiedViaKarmada"))
	if err := q.project.API.Status().Update(ctx, m); err != nil {
		return err
	}
	return r.projectPower(ctx, q)
}

// errTerminal marks an error that retrying cannot fix.
var errTerminal = errors.New("terminal")

// Terminal reports whether a phase must stop retrying.
func Terminal(err error) bool { return errors.Is(err, errTerminal) }

// Pending reports whether a phase is waiting for an effect.
func Pending(err error) bool { return errors.Is(err, errPending) }

func (r *Runner) Release(ctx context.Context) error       { return r.ReleaseIndex(ctx, 0) }
func (r *Runner) ReleaseSecond(ctx context.Context) error { return r.ReleaseIndex(ctx, 1) }

// ReleaseIndex advances the consumer's release one step; the phase repeats
// it until the allocation is Released, then publishes the Machine Released.
// The acceptance releases a Machine that still exists, as the consumer's
// release request; the management identity cannot delete Machines.
func (r *Runner) ReleaseIndex(ctx context.Context, index int) error {
	q, err := r.request(ctx, index)
	if err != nil {
		return err
	}
	released, err := r.Allocator.ReleaseMachine(ctx, q.alloc.Machine)
	if errors.Is(err, allocation.ErrWipeFailed) {
		return fmt.Errorf("%w: %v", errTerminal, err)
	}
	if err != nil || !released {
		return fmt.Errorf("%w: the release is not finished (%v)", errPending, err)
	}
	return publishRelease(ctx, q.project.API, q.machine)
}

// publishRelease runs only after the allocation is Released. Status is
// written first because a status response overwrites the in-memory object,
// including metadata, from the server; the finalizer is removed afterwards
// from that fresh copy so the removal is not silently reverted.
func publishRelease(ctx context.Context, api client.Client, m *fleet.Machine) error {
	// Retire the current installation before clearing the reference, so retries
	// can finish projection if either API write fails after cleanup.
	if ref := m.Status.CurrentInstallationRef; ref != nil {
		var install fleet.Installation
		err := api.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: ref.Name}, &install)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			install.Status.Phase = "Released"
			meta.SetStatusCondition(&install.Status.Conditions, condition("Ready", metav1.ConditionFalse, install.Generation, "Released"))
			if err := api.Status().Update(ctx, &install); err != nil {
				return err
			}
		}
	}
	m.Status.CurrentInstallationRef = nil
	m.Status.Phase = "Released"
	meta.SetStatusCondition(&m.Status.Conditions, condition("Bound", metav1.ConditionFalse, m.Generation, "Released"))
	meta.SetStatusCondition(&m.Status.Conditions, condition("Ready", metav1.ConditionFalse, m.Generation, "Released"))
	if err := api.Status().Update(ctx, m); err != nil {
		return err
	}
	retainedFinalizers := make([]string, 0, len(m.Finalizers))
	for _, f := range m.Finalizers {
		if f != allocation.MachineFinalizer {
			retainedFinalizers = append(retainedFinalizers, f)
		}
	}
	m.Finalizers = retainedFinalizers
	return api.Update(ctx, m)
}

func (r *Runner) Reuse(ctx context.Context) error {
	b, err := r.request(ctx, 1)
	if err != nil {
		return err
	}
	if _, err = r.Allocator.Allocate(ctx, b.alloc); err != nil {
		return err
	}
	// Public status follows the active allocation; readiness still requires
	// a verified installation for it.
	m := b.machine
	m.Status.Phase = "Bound"
	meta.SetStatusCondition(&m.Status.Conditions, condition("Bound", metav1.ConditionTrue, m.Generation, "AwaitingVerifiedInstallation"))
	meta.SetStatusCondition(&m.Status.Conditions, condition("Ready", metav1.ConditionFalse, m.Generation, "AwaitingVerifiedInstallation"))
	if err := r.Consumers[1].API.Status().Update(ctx, m); err != nil {
		return err
	}
	return r.projectPower(ctx, b)
}

func ReadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}
