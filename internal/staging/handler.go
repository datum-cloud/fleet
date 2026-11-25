// Package staging contains the deliberately synthetic Fleet integration used
// to exercise same-project and explicitly configured cross-project public API
// flows before production admission and provider adapters exist. It is never
// selected by the production overlay.
package staging

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/allocation"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/manager"
)

// Handler is an event-driven synthetic provider and claim coordinator. It uses
// the real durable allocator but substitutes an explicit synthetic admission
// decision and provider observation. It accepts only powered-off claims in
// one configured location, and network interfaces only when its allocator
// has a network owner.
type Handler struct {
	Public   client.Client
	Provider client.Client
	// Allocator allocates and releases in the provider project, on custody.
	Allocator       *allocation.Allocator
	Project         controller.ProjectIdentity
	ProviderProject controller.ProjectIdentity
	Location        string
	Now             func() time.Time
}

func (h Handler) ReconcileEvent(ctx context.Context, request manager.ProjectRequest) manager.Outcome {
	if h.Public == nil || h.Allocator == nil || !h.Project.Valid() || h.Location == "" || !request.Valid() || !request.SameProject(h.Project.AuthorityID, h.Project.UID) {
		return manager.Outcome{RetryError: manager.ErrEventWiringUnavailable}
	}
	crossProject := h.Provider != nil || h.ProviderProject.Valid()
	if crossProject && (h.Provider == nil || !h.ProviderProject.Valid() || h.ProviderProject.SameLifetime(h.Project)) {
		return manager.Outcome{RetryError: manager.ErrEventWiringUnavailable}
	}
	if h.Now == nil {
		h.Now = time.Now
	}
	var claim fleetv1.Machine
	if err := h.Public.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, &claim); apierrors.IsNotFound(err) {
		return h.release(ctx, request.Namespace, request.UID, nil)
	} else if err != nil {
		return manager.Outcome{RetryError: err}
	}
	if claim.UID != request.UID {
		return h.release(ctx, request.Namespace, request.UID, nil)
	}
	if claim.DeletionTimestamp != nil {
		return h.release(ctx, claim.Namespace, claim.UID, &claim)
	}
	if claim.Spec.LocationRef.Name != h.Location || claim.Spec.PowerState != "Off" || claim.Spec.InitialInstallationRef != nil {
		return h.rejected(ctx, &claim, "UnsupportedSyntheticRequest", "Synthetic staging accepts powered-off claims without an initial installation.")
	}

	var namespace corev1.Namespace
	if err := h.Public.Get(ctx, client.ObjectKey{Name: claim.Namespace}, &namespace); err != nil {
		return manager.Outcome{RetryError: err}
	}
	var class fleetv1.MachineClass
	if err := h.Public.Get(ctx, client.ObjectKey{Name: claim.Spec.MachineClassRef.Name}, &class); err != nil {
		return h.dependency(ctx, &claim, "MachineClassPending", err)
	}
	var profile fleetv1.MachineProfile
	if err := h.Public.Get(ctx, client.ObjectKey{Name: claim.Spec.MachineProfileRef.Name}, &profile); err != nil {
		return h.dependency(ctx, &claim, "MachineProfilePending", err)
	}
	sourceClass, sourceProfile := &class, &profile
	machineClient := h.Public
	if crossProject {
		if class.Spec.Source == nil || class.Spec.Source.Project != h.ProviderProject.RouteName || !sourceResolved(&class) || !profilePublished(&profile) || !publishedFor(&class, h.ProviderProject) || !publishedFor(&profile, h.ProviderProject) {
			return h.rejected(ctx, &claim, "UnresolvedPublication", "Synthetic cross-project allocation requires a publisher-owned class and profile with a current resolved source.")
		}
		sourceClass = &fleetv1.MachineClass{}
		if err := h.Provider.Get(ctx, client.ObjectKey{Name: class.Spec.Source.Name}, sourceClass); err != nil {
			return h.dependency(ctx, &claim, "SourceMachineClassPending", err)
		}
		sourceProfile = &fleetv1.MachineProfile{}
		sourceProfileName := profile.Annotations[publicationSourceNameAnnotation]
		if sourceProfileName == "" {
			return h.rejected(ctx, &claim, "UnresolvedPublication", "Published MachineProfile provenance is incomplete.")
		}
		if err := h.Provider.Get(ctx, client.ObjectKey{Name: sourceProfileName}, sourceProfile); err != nil {
			return h.dependency(ctx, &claim, "SourceMachineProfilePending", err)
		}
		revision, revisionErr := publicationRevision(sourceClass, h.ProviderProject)
		resolved := &fleetv1.ResolvedClass{ProfilePolicy: dereferenceProfilePolicy(sourceClass.Spec.ProfilePolicy), Access: dereferenceAccess(sourceClass.Spec.Access)}
		if revisionErr != nil || class.Status.EvaluatedRevision != revision || !reflect.DeepEqual(class.Status.ResolvedClass, resolved) || class.Annotations[publicationSourceUIDAnnotation] != string(sourceClass.UID) || profile.Annotations[publicationSourceUIDAnnotation] != string(sourceProfile.UID) || !reflect.DeepEqual(profile.Spec, sourceProfile.Spec) || !profileAllowed(sourceClass.Spec.ProfilePolicy, sourceProfile.Name) {
			return h.rejected(ctx, &claim, "PublicationDrift", "Published inputs no longer match the exact provider source lifetimes.")
		}
		machineClient = h.Provider
	} else if class.Spec.Source != nil {
		return h.rejected(ctx, &claim, "UnsupportedMachineClass", "Synthetic same-project staging requires a local capacity definition.")
	}
	// The pinned request is authoritative for what this Machine asked for. A
	// recreated profile with different CPU or memory must not be allocated
	// silently under the old snapshot.
	if pinned := claim.Status.RequestedHardware; pinned != nil && !controller.SameRequestedHardware(pinned, sourceProfile.Spec) {
		return h.rejected(ctx, &claim, "RequestedHardwareConflict", "The current MachineProfile no longer requests the pinned CPU and memory.")
	}
	if sourceClass.Spec.Capacity == nil || sourceClass.Spec.Capacity.MachineCapacities.Namespace == "" {
		return h.rejected(ctx, &claim, "UnsupportedMachineClass", "The resolved MachineClass has no explicit provider namespace.")
	}
	var list fleetv1.MachineCapacityList
	if err := machineClient.List(ctx, &list, client.InNamespace(sourceClass.Spec.Capacity.MachineCapacities.Namespace)); err != nil {
		return manager.Outcome{RetryError: err}
	}
	machines := make([]fleetv1.MachineCapacity, 0, len(list.Items))
	evidence := make(map[types.UID]time.Time, len(list.Items))
	now := h.Now().UTC()
	for i := range list.Items {
		machine := &list.Items[i]
		available := true
		if current := apiMeta.FindStatusCondition(machine.Status.Conditions, "Available"); current != nil && current.Status == metav1.ConditionFalse {
			available = false
		}
		if err := h.ensureObservation(ctx, machineClient, machine, available); err != nil {
			return manager.Outcome{RetryError: err}
		}
		machines = append(machines, *machine)
		evidence[machine.UID] = now
	}
	if !contains(claim.Finalizers, allocation.MachineFinalizer) {
		updated := claim.DeepCopyObject().(*fleetv1.Machine)
		updated.Finalizers = append(updated.Finalizers, allocation.MachineFinalizer)
		if err := h.Public.Update(ctx, updated); err != nil {
			return manager.Outcome{RetryError: err}
		}
		return manager.Outcome{}
	}
	authority := fleetv1.AllocationAuthority{Principal: "synthetic-staging:" + string(h.Project.UID), Decision: "synthetic-staging:" + string(claim.UID)}
	// Interfaces are checked before any capacity is reserved.
	existing, err := h.Allocator.Get(ctx, h.machine(&claim))
	if err != nil {
		return manager.Outcome{RetryError: err}
	}
	if existing == nil {
		if err := h.Allocator.CheckNetwork(ctx, &claim, authority.Principal); err != nil {
			network := allocation.NetworkCondition(&claim, false, err)
			if network.Status == metav1.ConditionUnknown {
				return manager.Outcome{RetryError: err}
			}
			return h.pending(ctx, &claim, network.Reason, network.Message, nil)
		}
	}
	record, err := h.Allocator.Allocate(ctx, allocation.Request{
		Machine: h.machine(&claim), Class: sourceClass, Profile: sourceProfile,
		Authority: authority,
	})
	if errors.Is(err, allocation.ErrInsufficientCapacity) {
		return h.pending(ctx, &claim, "InsufficientCapacity", "No matching synthetic machine is currently available.", nil)
	}
	if errors.Is(err, allocation.ErrCustodyLost) {
		return h.pending(ctx, &claim, "CustodyLost", "Device custody no longer names this Machine's allocation; an operator must resolve it.", nil)
	}
	if err != nil {
		return manager.Outcome{RetryError: err}
	}
	for i := range list.Items {
		if list.Items[i].Name == record.Spec.MachineCapacityRef.Name {
			if err := h.ensureObservation(ctx, machineClient, &list.Items[i], false); err != nil {
				return manager.Outcome{RetryError: err}
			}
		}
	}
	networkStatus, networkReady, networkErr := h.Allocator.Attach(ctx, record, &claim)
	networkCondition := allocation.NetworkCondition(&claim, networkReady, networkErr)
	if networkCondition.Status == metav1.ConditionUnknown {
		return manager.Outcome{RetryError: networkErr}
	}
	if len(claim.Spec.NetworkInterfaces) == 0 {
		networkCondition = condition("NetworkReady", metav1.ConditionTrue, claim.Generation, "NoAttachmentsRequired", "No network interfaces were requested.")
		networkStatus = nil
	}
	ready := condition("Ready", metav1.ConditionTrue, claim.Generation, "PoweredOffHandoff", "The synthetic powered-off handoff is ready.")
	phase := "Ready"
	if networkCondition.Status != metav1.ConditionTrue {
		ready = condition("Ready", metav1.ConditionFalse, claim.Generation, networkCondition.Reason, networkCondition.Message)
		phase = "Pending"
	}
	observed := metav1.NewTime(now)
	if claim.Status.AllocatedHardware != nil && claim.Status.AllocatedHardware.ObservedAt != nil {
		observed = *claim.Status.AllocatedHardware.ObservedAt
	}
	status := fleetv1.MachineStatus{
		Phase: phase, Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: h.Location}},
		AcceptedProfileRef: &fleetv1.LocalReference{Name: profile.Name},
		AllocatedHardware:  &fleetv1.AllocatedHardwareStatus{ObservedAt: &observed, Hardware: record.Spec.Hardware},
		Network:            networkStatus,
		Conditions: []metav1.Condition{
			condition("Accepted", metav1.ConditionTrue, claim.Generation, admissionReason(crossProject), admissionMessage(crossProject)),
			condition("Bound", metav1.ConditionTrue, claim.Generation, "MachineAllocationActive", "Device custody names this Machine's allocation."),
			condition(networkCondition.Type, networkCondition.Status, claim.Generation, networkCondition.Reason, networkCondition.Message),
			ready,
		},
	}
	status = preserveRequest(claim.Status, status)
	if sameStatus(claim.Status, status) {
		return manager.Outcome{}
	}
	claim.Status = status
	if err := h.Public.Status().Update(ctx, &claim); err != nil {
		return manager.Outcome{RetryError: err}
	}
	return manager.Outcome{}
}

// machine is the claim's exact identity in its consumer project.
func (h Handler) machine(claim *fleetv1.Machine) allocation.Machine {
	return allocation.Machine{Project: h.Project.RouteName, ProjectUID: h.Project.UID, Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}
}

func (h Handler) dependency(ctx context.Context, claim *fleetv1.Machine, reason string, err error) manager.Outcome {
	if apierrors.IsNotFound(err) {
		return h.pending(ctx, claim, reason, err.Error(), nil)
	}
	return manager.Outcome{RetryError: err}
}

func (h Handler) pending(ctx context.Context, claim *fleetv1.Machine, reason, message string, retryErr error) manager.Outcome {
	crossProject := h.Provider != nil && h.ProviderProject.Valid() && !h.ProviderProject.SameLifetime(h.Project)
	status := fleetv1.MachineStatus{Phase: "Pending", Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: claim.Spec.LocationRef.Name}}, Conditions: []metav1.Condition{
		condition("Accepted", metav1.ConditionTrue, claim.Generation, admissionReason(crossProject), admissionMessage(crossProject)),
		condition("Bound", metav1.ConditionFalse, claim.Generation, reason, message),
		condition("Ready", metav1.ConditionFalse, claim.Generation, reason, message),
	}}
	status = preserveRequest(claim.Status, status)
	if !sameStatus(claim.Status, status) {
		claim.Status = status
		if err := h.Public.Status().Update(ctx, claim); err != nil {
			return manager.Outcome{RetryError: err}
		}
	}
	return manager.Outcome{RetryError: retryErr}
}

func (h Handler) rejected(ctx context.Context, claim *fleetv1.Machine, reason, message string) manager.Outcome {
	status := fleetv1.MachineStatus{Phase: "Pending", Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: claim.Spec.LocationRef.Name}}, Conditions: []metav1.Condition{
		condition("Accepted", metav1.ConditionFalse, claim.Generation, reason, message),
		condition("Bound", metav1.ConditionFalse, claim.Generation, reason, message),
		condition("Ready", metav1.ConditionFalse, claim.Generation, reason, message),
	}}
	status = preserveRequest(claim.Status, status)
	if !sameStatus(claim.Status, status) {
		claim.Status = status
		if err := h.Public.Status().Update(ctx, claim); err != nil {
			return manager.Outcome{RetryError: err}
		}
	}
	return manager.Outcome{}
}

// errReleasing means the release has not finished; the event is retried.
var errReleasing = errors.New("synthetic release is in progress")

func (h Handler) release(ctx context.Context, namespace string, uid types.UID, claim *fleetv1.Machine) manager.Outcome {
	if claim == nil || !contains(claim.Finalizers, allocation.MachineFinalizer) {
		// A claim that never reached allocation needs no release.
		return manager.Outcome{}
	}
	machine := h.machine(claim)
	current, err := h.Allocator.Get(ctx, machine)
	if err != nil {
		return manager.Outcome{RetryError: err}
	}
	// Release also covers a Machine deleted before its allocation existed,
	// which may hold a quota claim.
	released, err := h.Allocator.ReleaseMachine(ctx, machine)
	if err != nil {
		return manager.Outcome{RetryError: err}
	}
	if !released {
		return manager.Outcome{RetryError: errReleasing}
	}
	if err := h.removeFinalizer(ctx, client.ObjectKeyFromObject(claim), uid); err != nil {
		return manager.Outcome{RetryError: err}
	}
	if current == nil {
		return manager.Outcome{}
	}
	// The released capacity is offered again. Custody stays the authority.
	capacity := &fleetv1.MachineCapacity{}
	reference := current.Spec.MachineCapacityRef
	if err := h.Allocator.Provider.Get(ctx, client.ObjectKey{Namespace: reference.Namespace, Name: reference.Name}, capacity); apierrors.IsNotFound(err) {
		return manager.Outcome{}
	} else if err != nil {
		return manager.Outcome{RetryError: err}
	}
	if err := h.ensureObservation(ctx, h.Allocator.Provider, capacity, true); err != nil {
		return manager.Outcome{RetryError: err}
	}
	return manager.Outcome{}
}

func (h Handler) ensureObservation(ctx context.Context, target client.Client, machine *fleetv1.MachineCapacity, available bool) error {
	want := fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 24, Threads: 48, Sockets: 2}, Memory: fleetv1.HardwareMemory{Capacity: "128Gi"}}
	conditions := []metav1.Condition{
		condition("Prepared", metav1.ConditionTrue, machine.Generation, "SyntheticProvider", "Synthetic staging provider reports the machine prepared."),
		condition("Healthy", metav1.ConditionTrue, machine.Generation, "SyntheticProvider", "Synthetic staging provider reports the machine healthy."),
	}
	if available {
		conditions = append(conditions, condition("Available", metav1.ConditionTrue, machine.Generation, "SyntheticProvider", "Synthetic staging provider reports the machine available; private custody enforces exclusivity."))
	} else {
		conditions = append(conditions, condition("Available", metav1.ConditionFalse, machine.Generation, "Allocated", "Private custody records an active allocation."))
	}
	if machine.Status.Hardware.CPU.Cores == want.CPU.Cores && machine.Status.Hardware.Memory.Capacity == want.Memory.Capacity {
		current := apiMeta.FindStatusCondition(machine.Status.Conditions, "Available")
		if current != nil && current.Status == conditions[2].Status && current.ObservedGeneration == machine.Generation {
			return nil
		}
	}
	machine.Status = fleetv1.MachineCapacityStatus{Hardware: want, Conditions: conditions}
	return target.Status().Update(ctx, machine)
}

func publishedFor(object client.Object, provider controller.ProjectIdentity) bool {
	annotations := object.GetAnnotations()
	return annotations[publicationManagedByAnnotation] == publicationManager && annotations[publicationProviderUIDAnnotation] == string(provider.UID) && annotations[publicationSourceUIDAnnotation] != "" && annotations[publicationSourceNameAnnotation] != ""
}

func profilePublished(profile *fleetv1.MachineProfile) bool {
	condition := apiMeta.FindStatusCondition(profile.Status.Conditions, "Published")
	return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == profile.Generation
}

func admissionReason(crossProject bool) string {
	if crossProject {
		return "SyntheticCrossProjectAdmission"
	}
	return "SyntheticStagingAdmission"
}

func admissionMessage(crossProject bool) string {
	if crossProject {
		return "Accepted through the explicit synthetic cross-project publication and allocation boundary."
	}
	return "Accepted by the explicit synthetic staging admission boundary."
}

func dereferenceProfilePolicy(policy *fleetv1.ProfilePolicy) fleetv1.ProfilePolicy {
	if policy == nil {
		return fleetv1.ProfilePolicy{}
	}
	return *policy
}

func (h Handler) removeFinalizer(ctx context.Context, key client.ObjectKey, uid types.UID) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current fleetv1.Machine
		if err := h.Public.Get(ctx, key, &current); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if current.UID != uid {
			return errors.New("the claim name was recreated before finalizer removal")
		}
		out := current.Finalizers[:0]
		for _, value := range current.Finalizers {
			if value != allocation.MachineFinalizer {
				out = append(out, value)
			}
		}
		current.Finalizers = out
		return h.Public.Update(ctx, &current)
	})
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func condition(kind string, status metav1.ConditionStatus, generation int64, reason, message string) metav1.Condition {
	return metav1.Condition{Type: kind, Status: status, ObservedGeneration: generation, LastTransitionTime: metav1.Now(), Reason: reason, Message: message}
}

// preserveRequest carries forward the fields owned by the projectors: the
// requested-hardware snapshot with its RequestResolved condition, and power.
// This handler replaces Machine status wholesale and must never drop or
// rewrite them.
func preserveRequest(current, next fleetv1.MachineStatus) fleetv1.MachineStatus {
	next.RequestedHardware = current.RequestedHardware
	next.Power = current.Power
	if resolved := apiMeta.FindStatusCondition(current.Conditions, controller.ConditionRequestResolved); resolved != nil {
		next.Conditions = append(next.Conditions, *resolved)
	}
	return next
}

// Ignore transition timestamps when deciding whether another status write is
// necessary; otherwise a dependency event would create a write loop.
func sameStatus(left, right fleetv1.MachineStatus) bool {
	if left.Phase != right.Phase || len(left.Conditions) != len(right.Conditions) || (left.Placement == nil) != (right.Placement == nil) || (left.AcceptedProfileRef == nil) != (right.AcceptedProfileRef == nil) || (left.AllocatedHardware == nil) != (right.AllocatedHardware == nil) {
		return false
	}
	left.Conditions = append([]metav1.Condition(nil), left.Conditions...)
	right.Conditions = append([]metav1.Condition(nil), right.Conditions...)
	for i := range left.Conditions {
		left.Conditions[i].LastTransitionTime = metav1.Time{}
		right.Conditions[i].LastTransitionTime = metav1.Time{}
	}
	left.Network, right.Network = timeless(left.Network), timeless(right.Network)
	return reflect.DeepEqual(left, right)
}

// timeless copies network status without condition transition times.
func timeless(status *fleetv1.NetworkStatus) *fleetv1.NetworkStatus {
	if status == nil {
		return nil
	}
	out := &fleetv1.NetworkStatus{Revision: status.Revision}
	for _, iface := range status.Interfaces {
		iface.Conditions = append([]metav1.Condition(nil), iface.Conditions...)
		for i := range iface.Conditions {
			iface.Conditions[i].LastTransitionTime = metav1.Time{}
		}
		out.Interfaces = append(out.Interfaces, iface)
	}
	return out
}

// ConsumerHandlers installs the synthetic staging handler for exactly one
// engaged project, the configured publication consumer, and base handlers for
// every other project. template supplies the coordinator, location, and
// optional provider; its Public client and Project are taken from the
// consumer's binding. An engagement under the consumer's name that is not the
// exact consumer lifetime is refused.
func ConsumerHandlers(base manager.HandlerFactory, consumer controller.ProjectIdentity, template Handler) manager.HandlerFactory {
	return func(binding manager.ProjectBinding) (manager.Handlers, error) {
		handlers, err := base(binding)
		if err != nil || string(binding.ClusterName) != consumer.RouteName {
			return handlers, err
		}
		if template.Allocator == nil {
			return manager.Handlers{}, fmt.Errorf("%w: synthetic staging has no allocator", manager.ErrEventWiringUnavailable)
		}
		if !binding.Project.SameLifetime(consumer) {
			return manager.Handlers{}, fmt.Errorf("%w: synthetic consumer %q is engaged as a different project lifetime", manager.ErrProjectIdentityUnavailable, binding.ClusterName)
		}
		handler := template
		handler.Public, handler.Project = binding.Client, binding.Project
		handlers.Machine = handler
		// The power projector reads the custody of the Machine's allocation
		// in the provider project.
		handlers.MachineProjector = manager.Chain(handlers.MachineProjector, manager.PowerProjector{Client: binding.Client, Project: binding.Project, Provider: template.Allocator.Provider, Namespace: template.Allocator.Namespace})
		return handlers, nil
	}
}
