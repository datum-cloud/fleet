package manager

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/milo"
)

// MiloMachineLifecycle is the durable bridge between the event handler and the
// existing Milo authority/allocation adapter. Prepared authority is recovered
// from private records on every event; no process-local result is required.
type MiloMachineLifecycle struct {
	Adapter MiloClaimAdapter
}

type MiloClaimAdapter interface {
	PrepareOrRecoverClaim(context.Context, milo.PrepareRequest) (milo.PreparedClaim, error)
	Allocate(context.Context, milo.PreparedClaim, controller.AllocationRequest) (*controller.AssetRecord, error)
	ReleaseClaim(context.Context, controller.ProjectIdentity, types.UID) (controller.ReleaseResult, error)
}

func (l MiloMachineLifecycle) RetainAuthority(ctx context.Context, project controller.ProjectIdentity, claim *fleetv1.Machine, inputs MachineInputs) error {
	if l.Adapter == nil || !project.Valid() || claim == nil || inputs.ConsumerRoute != project.RouteName {
		return ErrEventWiringUnavailable
	}
	_, err := l.Adapter.PrepareOrRecoverClaim(ctx, prepareRequest(claim, inputs))
	return claimLifecycleError(err)
}

func (l MiloMachineLifecycle) Allocate(ctx context.Context, project controller.ProjectIdentity, claim *fleetv1.Machine, inputs MachineInputs) error {
	if l.Adapter == nil || !project.Valid() || claim == nil || inputs.ConsumerRoute != project.RouteName {
		return ErrEventWiringUnavailable
	}
	prepared, err := l.Adapter.PrepareOrRecoverClaim(ctx, prepareRequest(claim, inputs))
	if err != nil {
		return claimLifecycleError(err)
	}
	profile := inputs.LocalProfile
	if inputs.SourceProfile != nil {
		profile = inputs.SourceProfile
	}
	_, err = l.Adapter.Allocate(ctx, prepared, controller.AllocationRequest{
		Claim: claim, Class: inputs.SourceClass, Profile: profile,
		MachineCapacities: inputs.Machines, EvidenceObservedAt: inputs.EvidenceObservedAt,
		Location: inputs.Location,
	})
	return claimLifecycleError(err)
}

func (l MiloMachineLifecycle) Release(ctx context.Context, project controller.ProjectIdentity, claimUID types.UID) (controller.ReleaseResult, error) {
	if l.Adapter == nil || !project.Valid() || claimUID == "" {
		return controller.ReleaseResult{}, ErrEventWiringUnavailable
	}
	result, err := l.Adapter.ReleaseClaim(ctx, project, claimUID)
	if errors.Is(err, milo.ErrClaimAuthorityNotFound) {
		return controller.ReleaseResult{}, ErrMachineAuthorityNotFound
	}
	return result, claimLifecycleError(err)
}

func prepareRequest(claim *fleetv1.Machine, inputs MachineInputs) milo.PrepareRequest {
	return milo.PrepareRequest{
		ConsumerRoute: inputs.ConsumerRoute, ProviderRoute: inputs.ProviderRoute,
		NamespaceUID: inputs.NamespaceUID, Claim: claim,
		LocalClass: inputs.LocalClass, SourceClass: inputs.SourceClass,
		LocalProfile: inputs.LocalProfile, SourceProfile: inputs.SourceProfile,
		PublicationRevision: inputs.PublicationRevision, Location: inputs.Location,
	}
}

func claimLifecycleError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, milo.ErrQuotaPending) || errors.Is(err, milo.ErrEntitlementUnavailable) || errors.Is(err, controller.ErrInsufficientCapacity) {
		return fmt.Errorf("%w: %v", ErrDependencyPending, err)
	}
	return err
}
