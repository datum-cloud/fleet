package manager

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/milo"
)

type fakeMiloClaimAdapter struct {
	prepared    milo.PreparedClaim
	prepareErr  error
	allocateErr error
	release     controller.ReleaseResult
	releaseErr  error
	prepare     milo.PrepareRequest
	allocation  controller.AllocationRequest
}

func (f *fakeMiloClaimAdapter) PrepareOrRecoverClaim(_ context.Context, request milo.PrepareRequest) (milo.PreparedClaim, error) {
	f.prepare = request
	return f.prepared, f.prepareErr
}

func (f *fakeMiloClaimAdapter) Allocate(_ context.Context, prepared milo.PreparedClaim, request controller.AllocationRequest) (*controller.AssetRecord, error) {
	f.prepared = prepared
	f.allocation = request
	return &controller.AssetRecord{}, f.allocateErr
}

func (f *fakeMiloClaimAdapter) ReleaseClaim(_ context.Context, _ controller.ProjectIdentity, _ types.UID) (controller.ReleaseResult, error) {
	return f.release, f.releaseErr
}

func TestMiloMachineLifecycleRecoversPreparationAndSelectsPublishedProfile(t *testing.T) {
	project := controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "project-uid"}
	claim := &fleetv1.Machine{}
	localProfile := &fleetv1.MachineProfile{}
	sourceProfile := &fleetv1.MachineProfile{}
	inputs := MachineInputs{ConsumerRoute: project.RouteName, LocalProfile: localProfile, SourceProfile: sourceProfile, SourceClass: &fleetv1.MachineClass{}}
	prepared := milo.PreparedClaim{Authority: controller.AuthorityRef{Provider: "milo", RecordName: "record", RecordID: "id", ContractDigest: "digest"}}
	adapter := &fakeMiloClaimAdapter{prepared: prepared}
	lifecycle := MiloMachineLifecycle{Adapter: adapter}
	if err := lifecycle.RetainAuthority(context.Background(), project, claim, inputs); err != nil {
		t.Fatal(err)
	}
	if adapter.prepare.Claim != claim {
		t.Fatal("retain did not pass the exact claim to prepare-or-recover")
	}
	if err := lifecycle.Allocate(context.Background(), project, claim, inputs); err != nil {
		t.Fatal(err)
	}
	if adapter.allocation.Profile != sourceProfile || adapter.allocation.Class != inputs.SourceClass {
		t.Fatalf("published allocation inputs = %#v", adapter.allocation)
	}

	inputs.SourceProfile = nil
	if err := lifecycle.Allocate(context.Background(), project, claim, inputs); err != nil {
		t.Fatal(err)
	}
	if adapter.allocation.Profile != localProfile {
		t.Fatal("custom profile did not use the local retained requirements")
	}
}

func TestMiloMachineLifecycleClassifiesOnlyWatchedDependencyWaits(t *testing.T) {
	project := controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "project-uid"}
	claim := &fleetv1.Machine{}
	inputs := MachineInputs{ConsumerRoute: project.RouteName}
	adapter := &fakeMiloClaimAdapter{prepareErr: milo.ErrQuotaPending}
	lifecycle := MiloMachineLifecycle{Adapter: adapter}
	if err := lifecycle.RetainAuthority(context.Background(), project, claim, inputs); !errors.Is(err, ErrDependencyPending) {
		t.Fatalf("quota pending error = %v", err)
	}
	adapter.prepareErr = milo.ErrAuthorityUnavailable
	if err := lifecycle.RetainAuthority(context.Background(), project, claim, inputs); !errors.Is(err, milo.ErrAuthorityUnavailable) || errors.Is(err, ErrDependencyPending) {
		t.Fatalf("authority failure was misclassified: %v", err)
	}
}

func TestMiloMachineLifecycleMapsOnlyProvenNoAuthority(t *testing.T) {
	project := controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "project-uid"}
	adapter := &fakeMiloClaimAdapter{releaseErr: milo.ErrClaimAuthorityNotFound}
	lifecycle := MiloMachineLifecycle{Adapter: adapter}
	if _, err := lifecycle.Release(context.Background(), project, "claim-uid"); !errors.Is(err, ErrMachineAuthorityNotFound) {
		t.Fatalf("proven absence error = %v", err)
	}
	adapter.releaseErr = errors.New("retained record disappeared after lookup")
	if _, err := lifecycle.Release(context.Background(), project, "claim-uid"); errors.Is(err, ErrMachineAuthorityNotFound) {
		t.Fatalf("later record loss was treated as safe absence: %v", err)
	}
}
