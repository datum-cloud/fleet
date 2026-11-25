// Package standalone runs the standalone smoke test's phases on Device
// custody. Each phase is the Metal provider and, for release, the release
// controller, acting on fixed fixtures in one kind cluster. The coordinator
// runs as its own process and the simulator is the site.
package standalone

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/manager"
)

const (
	PhaseAllocate  = "allocate"
	PhaseInstall   = "install"
	PhaseReinstall = "reinstall"
	PhaseRelease   = "release"
)

type Options struct {
	ProviderNamespace  string
	FirstNamespace     string
	SecondNamespace    string
	ClassName          string
	ProfileName        string
	MachineName        string
	ClaimName          string
	InstallationName   string
	ReinstallationName string
	LocationName       string
	// Wait bounds how long a phase waits for the coordinator and the site.
	Wait time.Duration
}

func DefaultOptions() Options {
	return Options{
		ProviderNamespace: "fleet-provider",
		FirstNamespace:    "fleet-project-a", SecondNamespace: "fleet-project-b",
		ClassName: "standard-metal", ProfileName: "general-purpose-24c-128g",
		MachineName: "central1-baremetal-01", ClaimName: "allocation",
		ReinstallationName: "debian-12-reinstall", InstallationName: "debian-12",
		LocationName: "us-central-1", Wait: 3 * time.Minute,
	}
}

// Runner runs one phase. Client is the Metal provider's identity. The
// allocator carries the coordinator's and the release controller's clients.
type Runner struct {
	Client    client.Client
	Allocator *allocation.Allocator
	Options   Options
	Now       func() time.Time
}

func (r Runner) Run(ctx context.Context, phase string) error {
	if r.Client == nil || r.Allocator == nil {
		return errors.New("standalone: a client and an allocator are required")
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	switch phase {
	case PhaseAllocate:
		return r.allocate(ctx)
	case PhaseInstall:
		return r.install(ctx, r.Options.InstallationName)
	case PhaseReinstall:
		if r.Options.ReinstallationName == "" || r.Options.ReinstallationName == r.Options.InstallationName {
			return errors.New("reinstall requires a distinct installation name")
		}
		return r.install(ctx, r.Options.ReinstallationName)
	case PhaseRelease:
		return r.release(ctx)
	default:
		return fmt.Errorf("unsupported smoke phase %q", phase)
	}
}

func (r Runner) allocate(ctx context.Context) error {
	class, profile, capacity, err := r.inputs(ctx)
	if err != nil {
		return err
	}
	if err := r.await(ctx, "custody of the fixture Device is Verified", func() (bool, error) {
		state := &ops.DeviceCustody{}
		if err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Options.ProviderNamespace, Name: capacity.Spec.DeviceRef.Name}, state); apierrors.IsNotFound(err) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		return state.Spec.State == ops.CustodyVerified, nil
	}); err != nil {
		return err
	}
	first, err := r.prepareClaim(ctx, r.Options.FirstNamespace)
	if err != nil {
		return err
	}
	second, err := r.prepareClaim(ctx, r.Options.SecondNamespace)
	if err != nil {
		return err
	}
	won, err := r.Allocator.Allocate(ctx, r.request(first, class, profile))
	if err != nil {
		return fmt.Errorf("allocate first consumer: %w", err)
	}
	if err := r.projectBound(ctx, first, profile, won); err != nil {
		return err
	}
	// Both contenders see the same Available capacity. Custody, not a
	// status projection, decides the winner.
	if _, err := r.Allocator.Allocate(ctx, r.request(second, class, profile)); !errors.Is(err, allocation.ErrInsufficientCapacity) {
		return fmt.Errorf("second consumer must wait for capacity: %w", err)
	}
	if lost, err := r.Allocator.Get(ctx, second.machine); err != nil || lost != nil {
		return fmt.Errorf("the losing consumer kept an allocation: %v", err)
	}
	if err := r.projectOccupied(ctx, capacity); err != nil {
		return err
	}
	return r.projectPending(ctx, second, profile, "InsufficientCapacity", "The only synthetic machine is allocated to another consumer.")
}

func (r Runner) install(ctx context.Context, installationName string) error {
	first, err := r.claim(ctx, r.Options.FirstNamespace)
	if err != nil {
		return err
	}
	current, err := r.Allocator.Get(ctx, first.machine)
	if err != nil {
		return err
	}
	if current == nil || current.Status.Phase != fleetv1.AllocationActive {
		return errors.New("the first consumer has no active allocation")
	}
	var installation fleetv1.Installation
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: first.claim.Namespace, Name: installationName}, &installation); err != nil {
		return err
	}
	if installationName == r.Options.ReinstallationName && installation.Spec.ExpectedPredecessor != (fleetv1.InstallationPredecessor{Type: "Installation", Name: r.Options.InstallationName}) {
		return errors.New("synthetic reinstall must name the configured initial installation as predecessor")
	}
	// A rerun after the site restarted finds the same operation by name and
	// waits for its result; it never creates a second one.
	operation := &ops.DeviceInstallation{}
	name := custody.InstallationName(current.Name, string(installation.UID))
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Options.ProviderNamespace, Name: name}, operation); apierrors.IsNotFound(err) {
		previous := ""
		if ref := first.claim.Status.CurrentInstallationRef; ref != nil {
			previous = ref.Name
		}
		apiMeta.SetStatusCondition(&first.claim.Status.Conditions, condition("Ready", metav1.ConditionUnknown, first.claim.Generation, "InstallationUnverified", "The installation has not yet returned a verified result."))
		if err := r.Client.Status().Update(ctx, first.claim); err != nil {
			return err
		}
		if operation, err = r.Allocator.Install(ctx, current, allocation.Installation{Installation: &installation, Current: previous}); err != nil {
			return fmt.Errorf("install synthetic image: %w", err)
		}
	} else if err != nil {
		return err
	}
	if err := r.await(ctx, "the DeviceInstallation succeeds", func() (bool, error) {
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
			return false, err
		}
		if phase := operation.Status.Phase; phase.Ended() && phase != ops.PhaseSucceeded {
			return false, fmt.Errorf("the installation ended %s: %s", phase, operation.Status.Reason)
		}
		return operation.Status.Phase == ops.PhaseSucceeded, nil
	}); err != nil {
		return err
	}
	now := metav1.NewTime(r.Now().UTC())
	installation.Status = fleetv1.InstallationStatus{Phase: "Ready", CompletedAt: &now, Conditions: []metav1.Condition{
		condition("Accepted", metav1.ConditionTrue, installation.Generation, "SyntheticFixtureAccepted", "The standalone fixture admitted this request; this is not Milo authorization."),
		condition("Executing", metav1.ConditionFalse, installation.Generation, "Completed", "The simulator site returned a verified result under the operation's fence."),
		condition("Ready", metav1.ConditionTrue, installation.Generation, "Installed", "The simulator site verified the installation."),
	}}
	if err := r.Client.Status().Update(ctx, &installation); err != nil {
		return err
	}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(first.claim), first.claim); err != nil {
		return err
	}
	first.claim.Status.CurrentInstallationRef = &fleetv1.LocalReference{Name: installation.Name}
	apiMeta.SetStatusCondition(&first.claim.Status.Conditions, condition("Ready", metav1.ConditionTrue, first.claim.Generation, "SyntheticHandoffReady", "The installation is verified."))
	if err := r.Client.Status().Update(ctx, first.claim); err != nil {
		return err
	}
	return r.projectPower(ctx, first.machine)
}

func (r Runner) release(ctx context.Context) error {
	first, err := r.claim(ctx, r.Options.FirstNamespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if first.claim.DeletionTimestamp == nil {
			uid, version := first.claim.UID, first.claim.ResourceVersion
			if err := r.Client.Delete(ctx, first.claim, client.Preconditions{UID: &uid, ResourceVersion: &version}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		current, err := r.Allocator.Get(ctx, first.machine)
		if err != nil {
			return err
		}
		if current != nil {
			if err := r.await(ctx, "the first allocation is Released", func() (bool, error) {
				if err := r.Client.Get(ctx, client.ObjectKeyFromObject(current), current); err != nil {
					return false, err
				}
				released, err := r.Allocator.Release(ctx, current, first.machine)
				if errors.Is(err, allocation.ErrWipeFailed) {
					return false, err
				}
				return released, nil
			}); err != nil {
				return err
			}
		}
		if err := r.removeFinalizer(ctx, client.ObjectKeyFromObject(first.claim), first.claim.UID); err != nil {
			return err
		}
	}
	class, profile, capacity, err := r.inputs(ctx)
	if err != nil {
		return err
	}
	second, err := r.claim(ctx, r.Options.SecondNamespace)
	if err != nil {
		return err
	}
	reused, err := r.Allocator.Allocate(ctx, r.request(second, class, profile))
	if err != nil {
		return fmt.Errorf("allocate waiting consumer: %w", err)
	}
	if reused.Spec.DeviceRef.Name != capacity.Spec.DeviceRef.Name {
		return errors.New("the waiting consumer did not reuse the released Device")
	}
	if err := r.projectBound(ctx, second, profile, reused); err != nil {
		return err
	}
	return r.projectOccupied(ctx, capacity)
}

// await polls until done, a failure, or the phase's wait bound.
func (r Runner) await(ctx context.Context, what string, done func() (bool, error)) error {
	deadline := r.Now().Add(r.Options.Wait)
	for {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		if r.Now().After(deadline) {
			return fmt.Errorf("timed out waiting until %s", what)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// inputs reads the fixtures and writes the synthetic capacity evidence. The
// capacity projector is not wired, so the fixture writes Prepared, Healthy
// and Available itself. Custody stays authoritative for exclusivity.
func (r Runner) inputs(ctx context.Context) (*fleetv1.MachineClass, *fleetv1.MachineProfile, *fleetv1.MachineCapacity, error) {
	var class fleetv1.MachineClass
	var profile fleetv1.MachineProfile
	var capacity fleetv1.MachineCapacity
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.Options.ClassName}, &class); err != nil {
		return nil, nil, nil, err
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.Options.ProfileName}, &profile); err != nil {
		return nil, nil, nil, err
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Options.ProviderNamespace, Name: r.Options.MachineName}, &capacity); err != nil {
		return nil, nil, nil, err
	}
	if class.Spec.Capacity == nil || class.Spec.Capacity.MachineCapacities.Namespace != r.Options.ProviderNamespace {
		return nil, nil, nil, errors.New("synthetic provider scope does not match configuration")
	}
	capacity.Status.Hardware = fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 24, Threads: 48, Sockets: 2}, Memory: fleetv1.HardwareMemory{Capacity: "128Gi"}}
	capacity.Status.Conditions = []metav1.Condition{
		condition("Prepared", metav1.ConditionTrue, capacity.Generation, "SyntheticEvidence", "Synthetic fixture evidence is current."),
		condition("Healthy", metav1.ConditionTrue, capacity.Generation, "SyntheticEvidence", "Synthetic fixture evidence is current."),
		condition("Available", metav1.ConditionTrue, capacity.Generation, "SyntheticEvidence", "Device custody remains authoritative for exclusivity."),
	}
	if err := r.Client.Status().Update(ctx, &capacity); err != nil {
		return nil, nil, nil, err
	}
	return &class, &profile, &capacity, nil
}

type claim struct {
	claim   *fleetv1.Machine
	machine allocation.Machine
}

func (r Runner) claim(ctx context.Context, namespaceName string) (claim, error) {
	machine := &fleetv1.Machine{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: r.Options.ClaimName}, machine); err != nil {
		return claim{}, err
	}
	namespace := &corev1.Namespace{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: namespaceName}, namespace); err != nil {
		return claim{}, err
	}
	// Each consumer namespace stands in for a consumer project.
	return claim{claim: machine, machine: allocation.Machine{Project: namespace.Name, ProjectUID: namespace.UID, Namespace: machine.Namespace, Name: machine.Name, UID: machine.UID}}, nil
}

func (r Runner) prepareClaim(ctx context.Context, namespaceName string) (claim, error) {
	current, err := r.claim(ctx, namespaceName)
	if err != nil {
		return claim{}, err
	}
	spec := current.claim.Spec
	if spec.LocationRef.Name != r.Options.LocationName || spec.MachineClassRef.Name != r.Options.ClassName || spec.MachineProfileRef.Name != r.Options.ProfileName || len(spec.NetworkInterfaces) != 0 || spec.PowerState != "Off" {
		return claim{}, errors.New("the claim does not match the configured synthetic fixture")
	}
	if !contains(current.claim.Finalizers, allocation.MachineFinalizer) {
		current.claim.Finalizers = append(current.claim.Finalizers, allocation.MachineFinalizer)
		if err := r.Client.Update(ctx, current.claim); err != nil {
			return claim{}, err
		}
	}
	return current, nil
}

func (r Runner) request(c claim, class *fleetv1.MachineClass, profile *fleetv1.MachineProfile) allocation.Request {
	return allocation.Request{Machine: c.machine, Class: class, Profile: profile, Authority: fleetv1.AllocationAuthority{Principal: "standalone-fixture", Decision: "standalone-claim-" + string(c.machine.UID)}}
}

// withRequestedHardware applies the same resolve-once contract as the
// manager's requested-hardware projector.
func withRequestedHardware(machine *fleetv1.Machine, profile *fleetv1.MachineProfile, status fleetv1.MachineStatus) (fleetv1.MachineStatus, error) {
	requested := machine.Status.RequestedHardware
	if requested == nil {
		var err error
		if requested, err = controller.ResolveRequestedHardware(machine, profile); err != nil {
			return status, fmt.Errorf("resolve requested hardware: %w", err)
		}
	}
	status.RequestedHardware = requested
	status.Conditions = append(status.Conditions, condition(controller.ConditionRequestResolved, metav1.ConditionTrue, machine.Generation, controller.ReasonRequestResolved, "Requested hardware is pinned from the fixture MachineProfile."))
	return status, nil
}

func (r Runner) projectBound(ctx context.Context, c claim, profile *fleetv1.MachineProfile, bound *fleetv1.MachineAllocation) error {
	observed := metav1.NewTime(r.Now().UTC())
	status := fleetv1.MachineStatus{Phase: "Ready", Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: r.Options.LocationName}}, AcceptedProfileRef: &fleetv1.LocalReference{Name: r.Options.ProfileName}, AllocatedHardware: &fleetv1.AllocatedHardwareStatus{ObservedAt: &observed, Hardware: bound.Spec.Hardware}, Conditions: []metav1.Condition{
		condition("Accepted", metav1.ConditionTrue, c.claim.Generation, "SyntheticFixtureAccepted", "This standalone fixture does not represent Milo admission."),
		condition("Bound", metav1.ConditionTrue, c.claim.Generation, "MachineAllocationActive", "Device custody names this Machine's allocation."),
		condition("NetworkReady", metav1.ConditionTrue, c.claim.Generation, "NoAttachmentsRequired", "No network interfaces were requested."),
		condition("Ready", metav1.ConditionTrue, c.claim.Generation, "PoweredOffHandoff", "Synthetic powered-off handoff is ready."),
	}}
	if err := r.writeStatus(ctx, c.claim, profile, status); err != nil {
		return err
	}
	return r.projectPower(ctx, c.machine)
}

func (r Runner) projectPending(ctx context.Context, c claim, profile *fleetv1.MachineProfile, reason, message string) error {
	status := fleetv1.MachineStatus{Phase: "Pending", Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: r.Options.LocationName}}, Conditions: []metav1.Condition{
		condition("Accepted", metav1.ConditionTrue, c.claim.Generation, "SyntheticFixtureAccepted", "This standalone fixture does not represent Milo admission."),
		condition("Bound", metav1.ConditionFalse, c.claim.Generation, reason, message),
		condition("Ready", metav1.ConditionFalse, c.claim.Generation, reason, message),
	}}
	return r.writeStatus(ctx, c.claim, profile, status)
}

// writeStatus keeps status.power, which only the manager's power projector
// writes.
func (r Runner) writeStatus(ctx context.Context, machine *fleetv1.Machine, profile *fleetv1.MachineProfile, status fleetv1.MachineStatus) error {
	status, err := withRequestedHardware(machine, profile, status)
	if err != nil {
		return err
	}
	status.Power = machine.Status.Power
	machine.Status = status
	return r.Client.Status().Update(ctx, machine)
}

// projectPower runs the manager's power projector for a fixture Machine.
func (r Runner) projectPower(ctx context.Context, machine allocation.Machine) error {
	return manager.ProjectPower(ctx, r.Client, r.Client, r.Options.ProviderNamespace, machine)
}

func (r Runner) projectOccupied(ctx context.Context, capacity *fleetv1.MachineCapacity) error {
	apiMeta.SetStatusCondition(&capacity.Status.Conditions, condition("Available", metav1.ConditionFalse, capacity.Generation, "Allocated", "Device custody names an active allocation."))
	return r.Client.Status().Update(ctx, capacity)
}

func (r Runner) removeFinalizer(ctx context.Context, key client.ObjectKey, uid types.UID) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current fleetv1.Machine
		if err := r.Client.Get(ctx, key, &current); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if current.UID != uid {
			return errors.New("the claim name was recreated before finalizer removal")
		}
		if !contains(current.Finalizers, allocation.MachineFinalizer) {
			return nil
		}
		current.Finalizers = remove(current.Finalizers, allocation.MachineFinalizer)
		return r.Client.Update(ctx, &current)
	})
}

func condition(t string, s metav1.ConditionStatus, g int64, reason, message string) metav1.Condition {
	return metav1.Condition{Type: t, Status: s, ObservedGeneration: g, LastTransitionTime: metav1.Now(), Reason: reason, Message: message}
}

func contains(v []string, w string) bool {
	for _, x := range v {
		if x == w {
			return true
		}
	}
	return false
}

func remove(v []string, w string) []string {
	out := v[:0]
	for _, x := range v {
		if x != w {
			out = append(out, x)
		}
	}
	return out
}
