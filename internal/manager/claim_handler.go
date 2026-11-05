package manager

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	fleetnetwork "go.datum.net/fleet/internal/network"
)

// ErrDependencyPending means progress requires an event from an authoritative
// dependency. It is deliberately not converted into a timed retry.
var ErrDependencyPending = errors.New("authoritative dependency is pending")

// ErrClaimAuthorityNotFound is returned only when the lifecycle owner has
// proved that no retained authority or custody exists for the exact claim UID.
var ErrClaimAuthorityNotFound = errors.New("claim has no retained authority")

// ClaimInputs are the authoritative inputs retained by the lifecycle owner.
// Implementations must resolve caller admission, source publication, provider
// inventory and evidence outside the public claim status.
type ClaimInputs struct {
	ConsumerRoute       string
	ProviderRoute       string
	NamespaceUID        types.UID
	LocalClass          *fleetv1.MachineClass
	SourceClass         *fleetv1.MachineClass
	LocalProfile        *fleetv1.MachineProfile
	SourceProfile       *fleetv1.MachineProfile
	PublicationRevision string
	Machines            []fleetv1.Machine
	EvidenceObservedAt  map[types.UID]time.Time
	Location            fleetnetwork.LocationAuthority
	EvidenceExpiresAt   time.Time
}

func (i ClaimInputs) valid() bool {
	return i.ConsumerRoute != "" && i.ProviderRoute != "" && i.NamespaceUID != "" && i.LocalClass != nil && i.SourceClass != nil && i.LocalProfile != nil && i.PublicationRevision != "" && i.Location.Valid()
}

// ClaimInputResolver owns the integrations that cannot be reconstructed from
// an informer event: create-time caller authority, publication provenance and
// trusted provider observations. A dependency that will emit a routed event
// returns an error wrapping ErrDependencyPending.
type ClaimInputResolver interface {
	ResolveClaimInputs(context.Context, controller.ProjectIdentity, *fleetv1.MachineClaim, *corev1.Namespace) (ClaimInputs, error)
}

// ClaimLifecycle composes the Milo adapter and durable allocation coordinator.
// RetainAuthority and Allocate must be idempotent for the exact retained claim
// lifetime. RetainAuthority must persist exact authority and custody before
// returning. Release must not report SafeToRemoveFinalizer until cleanup and
// external settlement are known.
type ClaimLifecycle interface {
	RetainAuthority(context.Context, controller.ProjectIdentity, *fleetv1.MachineClaim, ClaimInputs) error
	Allocate(context.Context, controller.ProjectIdentity, *fleetv1.MachineClaim, ClaimInputs) error
	Release(context.Context, controller.ProjectIdentity, types.UID) (controller.ReleaseResult, error)
}

// ClaimHandler is the event-only MachineClaim lifecycle coordinator. It does
// not project public status; LifecycleOwner invokes the sole-writer projector
// after this handler has durably processed the event.
type ClaimHandler struct {
	Client    client.Client
	Project   controller.ProjectIdentity
	Inputs    ClaimInputResolver
	Lifecycle ClaimLifecycle
}

func (h ClaimHandler) ReconcileEvent(ctx context.Context, request ProjectRequest) Outcome {
	if h.Client == nil || !h.Project.Valid() || h.Inputs == nil || h.Lifecycle == nil {
		return Outcome{RetryError: ErrEventWiringUnavailable}
	}
	if !request.Valid() || !request.SameProject(h.Project.AuthorityID, h.Project.UID) {
		return Outcome{}
	}
	var claim fleetv1.MachineClaim
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, &claim); apierrors.IsNotFound(err) {
		return h.releaseUID(ctx, request.UID, nil)
	} else if err != nil {
		return Outcome{RetryError: err}
	}
	if claim.UID != request.UID {
		return h.releaseUID(ctx, request.UID, nil)
	}
	if claim.DeletionTimestamp != nil {
		return h.release(ctx, &claim)
	}

	var namespace corev1.Namespace
	if err := h.Client.Get(ctx, client.ObjectKey{Name: claim.Namespace}, &namespace); apierrors.IsNotFound(err) {
		return Outcome{}
	} else if err != nil {
		return Outcome{RetryError: err}
	}
	inputs, err := h.Inputs.ResolveClaimInputs(ctx, h.Project, &claim, &namespace)
	if err != nil {
		return dependencyOutcome(err)
	}
	if !inputs.valid() || inputs.ConsumerRoute != h.Project.RouteName || inputs.NamespaceUID != namespace.UID || inputs.Location.Name != claim.Spec.LocationRef.Name || inputs.LocalClass.Name != claim.Spec.MachineClassRef.Name || inputs.LocalProfile.Name != claim.Spec.MachineProfileRef.Name {
		return Outcome{RetryError: ErrEventWiringUnavailable}
	}
	if err := h.Lifecycle.RetainAuthority(ctx, h.Project, &claim, inputs); err != nil {
		return dependencyOutcome(err)
	}
	deadline := deadlineFor(inputs.EvidenceExpiresAt)
	if !containsString(claim.Finalizers, controller.MachineClaimFinalizer) {
		if err := h.addFinalizer(ctx, &claim); err != nil {
			return Outcome{RetryError: err}
		}
		// The finalizer update is itself a primary event. Allocation occurs only
		// after the API confirms the protected lifetime marker is present.
		return Outcome{Deadline: deadline}
	}
	if err := h.Lifecycle.Allocate(ctx, h.Project, &claim, inputs); err != nil {
		outcome := dependencyOutcome(err)
		if outcome.RetryError == nil {
			outcome.Deadline = deadline
		}
		return outcome
	}
	return Outcome{Deadline: deadline}
}

func (h ClaimHandler) release(ctx context.Context, claim *fleetv1.MachineClaim) Outcome {
	return h.releaseUID(ctx, claim.UID, claim)
}

func (h ClaimHandler) releaseUID(ctx context.Context, uid types.UID, claim *fleetv1.MachineClaim) Outcome {
	result, err := h.Lifecycle.Release(ctx, h.Project, uid)
	if err != nil {
		if errors.Is(err, ErrClaimAuthorityNotFound) {
			return Outcome{Deadline: CancelDeadline()}
		}
		return dependencyOutcome(err)
	}
	if !result.SafeToRemoveFinalizer {
		return Outcome{RetryError: ErrEventWiringUnavailable}
	}
	if claim == nil || !containsString(claim.Finalizers, controller.MachineClaimFinalizer) {
		return Outcome{Deadline: CancelDeadline()}
	}
	if err := h.removeFinalizer(ctx, client.ObjectKeyFromObject(claim), uid); err != nil {
		return Outcome{RetryError: err}
	}
	return Outcome{Deadline: CancelDeadline()}
}

func dependencyOutcome(err error) Outcome {
	if errors.Is(err, ErrDependencyPending) {
		return Outcome{}
	}
	return Outcome{RetryError: err}
}

func deadlineFor(at time.Time) DeadlineDirective {
	if at.IsZero() {
		return DeadlineDirective{}
	}
	return ScheduleDeadline(at)
}

func (h ClaimHandler) addFinalizer(ctx context.Context, admitted *fleetv1.MachineClaim) error {
	if admitted == nil || admitted.UID == "" || admitted.ResourceVersion == "" || admitted.DeletionTimestamp != nil {
		return ErrEventWiringUnavailable
	}
	if containsString(admitted.Finalizers, controller.MachineClaimFinalizer) {
		return nil
	}
	updated := admitted.DeepCopyObject().(*fleetv1.MachineClaim)
	updated.Finalizers = append(updated.Finalizers, controller.MachineClaimFinalizer)
	// A conflict forces full input and authority resolution again; never attach
	// a retained decision to a newer spec.
	return h.Client.Update(ctx, updated)
}

func (h ClaimHandler) removeFinalizer(ctx context.Context, key client.ObjectKey, uid types.UID) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current fleetv1.MachineClaim
		if err := h.Client.Get(ctx, key, &current); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if current.UID != uid {
			return ErrEventWiringUnavailable
		}
		if !containsString(current.Finalizers, controller.MachineClaimFinalizer) {
			return nil
		}
		finalizers := current.Finalizers[:0]
		for _, finalizer := range current.Finalizers {
			if finalizer != controller.MachineClaimFinalizer {
				finalizers = append(finalizers, finalizer)
			}
		}
		current.Finalizers = finalizers
		return h.Client.Update(ctx, &current)
	})
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
