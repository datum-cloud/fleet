// Package staging contains the deliberately synthetic Fleet integration used
// to exercise same-project and explicitly configured cross-project public API
// flows before production admission and provider adapters exist. It is never
// selected by the production overlay.
package staging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/manager"
)

// Handler is an event-driven synthetic provider and claim coordinator. It uses
// the real durable allocator but substitutes an explicit synthetic admission
// decision and provider observation. It accepts only powered-off, networkless
// claims in one configured location.
type Handler struct {
	Public          client.Client
	Provider        client.Client
	Coordinator     *controller.Coordinator
	Project         controller.ProjectIdentity
	ProviderProject controller.ProjectIdentity
	Location        string
	Now             func() time.Time
}

func (h Handler) ReconcileEvent(ctx context.Context, request manager.ProjectRequest) manager.Outcome {
	if h.Public == nil || h.Coordinator == nil || !h.Project.Valid() || h.Location == "" || !request.Valid() || !request.SameProject(h.Project.AuthorityID, h.Project.UID) {
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
	if claim.Spec.LocationRef.Name != h.Location || claim.Spec.PowerState != "Off" || len(claim.Spec.NetworkInterfaces) != 0 || claim.Spec.InitialInstallationRef != nil {
		return h.rejected(ctx, &claim, "UnsupportedSyntheticRequest", "Synthetic staging accepts powered-off, networkless claims without an initial installation.")
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
	admission := controller.AdmissionAuthority{PrincipalUID: "synthetic-staging:" + string(h.Project.UID), DecisionID: "synthetic-staging:" + string(claim.UID)}
	if crossProject {
		if err := h.Coordinator.EnsureClaimCustody(ctx, &claim, namespace.UID, admission, h.Project); err != nil {
			return manager.Outcome{RetryError: err}
		}
		authority, hold := syntheticCrossProjectAuthority(claim.UID, claim.Namespace, h.Project, h.ProviderProject)
		if err := h.Coordinator.RetainClaimAuthority(ctx, claim.UID, h.Project, h.ProviderProject, authority); err != nil {
			return manager.Outcome{RetryError: err}
		}
		if err := h.Coordinator.RetainAllocationAuthority(ctx, claim.UID, h.Project, h.ProviderProject, authority, hold); err != nil {
			return manager.Outcome{RetryError: err}
		}
	} else if err := h.Coordinator.EnsureClaimCustody(ctx, &claim, namespace.UID, admission); err != nil {
		return manager.Outcome{RetryError: err}
	}
	if !contains(claim.Finalizers, controller.MachineFinalizer) {
		updated := claim.DeepCopyObject().(*fleetv1.Machine)
		updated.Finalizers = append(updated.Finalizers, controller.MachineFinalizer)
		if err := h.Public.Update(ctx, updated); err != nil {
			return manager.Outcome{RetryError: err}
		}
		return manager.Outcome{}
	}
	allocation := controller.AllocationRequest{
		Claim: &claim, Class: sourceClass, Profile: sourceProfile, MachineCapacities: machines,
		EvidenceObservedAt: evidence, NamespaceUID: namespace.UID, Admission: admission,
		LocalClassName: class.Name, LocalClassUID: class.UID,
		LocalProfileName: profile.Name, LocalProfileUID: profile.UID,
	}
	if crossProject {
		allocation.ConsumerProject, allocation.ProviderProject = h.Project, h.ProviderProject
		allocation.Authority, allocation.ExternalHold = syntheticCrossProjectAuthority(claim.UID, claim.Namespace, h.Project, h.ProviderProject)
	}
	record, err := h.Coordinator.Allocate(ctx, allocation)
	if errors.Is(err, controller.ErrInsufficientCapacity) {
		return h.pending(ctx, &claim, "InsufficientCapacity", "No matching synthetic machine is currently available.", nil)
	}
	if err != nil {
		return manager.Outcome{RetryError: err}
	}
	for i := range list.Items {
		if list.Items[i].UID == record.MachineUID {
			if err := h.ensureObservation(ctx, machineClient, &list.Items[i], false); err != nil {
				return manager.Outcome{RetryError: err}
			}
		}
	}
	observed := metav1.NewTime(now)
	if claim.Status.AllocatedHardware != nil && claim.Status.AllocatedHardware.ObservedAt != nil {
		observed = *claim.Status.AllocatedHardware.ObservedAt
	}
	status := fleetv1.MachineStatus{
		Phase: "Ready", Placement: &fleetv1.Placement{LocationRef: fleetv1.LocalReference{Name: h.Location}},
		AcceptedProfileRef: &fleetv1.LocalReference{Name: profile.Name},
		AllocatedHardware:  &fleetv1.AllocatedHardwareStatus{ObservedAt: &observed, Hardware: record.Hardware},
		Power:              &fleetv1.PowerStatus{State: "Off"},
		Conditions: []metav1.Condition{
			condition("Accepted", metav1.ConditionTrue, claim.Generation, admissionReason(crossProject), admissionMessage(crossProject)),
			condition("Bound", metav1.ConditionTrue, claim.Generation, "ExclusiveBinding", "Private custody contains the exclusive binding."),
			condition("NetworkReady", metav1.ConditionTrue, claim.Generation, "NoAttachmentsRequired", "No network interfaces were requested."),
			condition("Ready", metav1.ConditionTrue, claim.Generation, "PoweredOffHandoff", "The synthetic powered-off handoff is ready."),
		},
	}
	if sameStatus(claim.Status, status) {
		return manager.Outcome{}
	}
	claim.Status = status
	if err := h.Public.Status().Update(ctx, &claim); err != nil {
		return manager.Outcome{RetryError: err}
	}
	return manager.Outcome{}
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
	if !sameStatus(claim.Status, status) {
		claim.Status = status
		if err := h.Public.Status().Update(ctx, claim); err != nil {
			return manager.Outcome{RetryError: err}
		}
	}
	return manager.Outcome{}
}

func (h Handler) release(ctx context.Context, namespace string, uid types.UID, claim *fleetv1.Machine) manager.Outcome {
	crossProject := h.Provider != nil && h.ProviderProject.Valid() && !h.ProviderProject.SameLifetime(h.Project)
	var result controller.ReleaseResult
	var err error
	if crossProject {
		result, err = h.Coordinator.ReleaseScoped(ctx, h.Project, uid)
	} else {
		result, err = h.Coordinator.Release(ctx, uid)
	}
	if err != nil {
		// A claim that never reached custody needs no synthetic cleanup.
		if errors.Is(err, controller.ErrRecordInvalid) && (claim == nil || !contains(claim.Finalizers, controller.MachineFinalizer)) {
			return manager.Outcome{}
		}
		return manager.Outcome{RetryError: err}
	}
	if crossProject && result.ExternalSettlementRequired {
		authority, hold := syntheticCrossProjectAuthority(uid, namespace, h.Project, h.ProviderProject)
		result, err = h.Coordinator.CompleteExternalRelease(ctx, uid, authority, hold, h.Project)
		if err != nil {
			return manager.Outcome{RetryError: err}
		}
	}
	if !result.SafeToRemoveFinalizer {
		return manager.Outcome{RetryError: fmt.Errorf("synthetic cleanup is not settled")}
	}
	if claim != nil && contains(claim.Finalizers, controller.MachineFinalizer) {
		if err := h.removeFinalizer(ctx, client.ObjectKeyFromObject(claim), uid); err != nil {
			return manager.Outcome{RetryError: err}
		}
	}
	// A Machine event wakes all claims routed through its class. Private
	// custody remains the authority if more than one synthetic machine exists.
	var machines fleetv1.MachineCapacityList
	machineClient := h.Public
	if crossProject {
		machineClient = h.Provider
	}
	if err := machineClient.List(ctx, &machines); err != nil {
		return manager.Outcome{RetryError: err}
	}
	for i := range machines.Items {
		machine := &machines.Items[i]
		if err := h.ensureObservation(ctx, machineClient, machine, true); err != nil {
			return manager.Outcome{RetryError: err}
		}
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

func syntheticCrossProjectAuthority(claimUID types.UID, namespace string, consumer, provider controller.ProjectIdentity) (controller.AuthorityRef, controller.ExternalHoldRef) {
	value := "synthetic-cross-project-v1/" + consumer.AuthorityID + "/" + string(consumer.UID) + "/" + string(provider.UID) + "/" + string(claimUID)
	sum := sha256.Sum256([]byte(value))
	digest := hex.EncodeToString(sum[:])
	authority := controller.AuthorityRef{Provider: "synthetic-staging", RecordName: "synthetic-publication-" + digest[:24], RecordID: digest[:32], ContractDigest: digest}
	hold := controller.ExternalHoldRef{Provider: "synthetic-staging", Namespace: namespace, Name: "machine-" + digest[:24], UID: types.UID("synthetic-" + string(claimUID))}
	return authority, hold
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
			return controller.ErrRecordInvalid
		}
		out := current.Finalizers[:0]
		for _, value := range current.Finalizers {
			if value != controller.MachineFinalizer {
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
	return reflect.DeepEqual(left, right)
}
