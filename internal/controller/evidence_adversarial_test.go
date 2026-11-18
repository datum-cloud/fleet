package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

// Return a valid terminal result for the same operation name but a different
// physical target. Exercise both direct completion and lost-response recovery.
type substitutedCleanupExecutor struct {
	result simulator.OperationResult
	lost   bool
}

func (e *substitutedCleanupExecutor) Execute(_ context.Context, request simulator.Request) (simulator.OperationResult, error) {
	request.AssetID += "-different-physical-asset"
	digest, err := simulator.RequestDigest(request)
	if err != nil {
		return simulator.OperationResult{}, err
	}
	now := time.Now()
	e.result = simulator.OperationResult{ID: request.ID, Kind: request.Kind, Phase: "Succeeded", Result: "clean", InputDigest: digest, CompletedAt: &now}
	if e.lost {
		return simulator.OperationResult{}, executor.ErrOutcomeUnknown
	}
	return e.result, nil
}

func (e *substitutedCleanupExecutor) Lookup(context.Context, string) (simulator.OperationResult, bool, error) {
	return e.result, true, nil
}

func TestAdversarialCleanupEvidenceCannotReleaseAnotherAsset(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "direct"
		if lost {
			name = "recovered"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			owner, waiter := f.request("owner"), f.request("waiter")
			for _, request := range []AllocationRequest{owner, waiter} {
				if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.coordinator.Allocate(ctx, owner); err != nil {
				t.Fatal(err)
			}
			f.coordinator.executor = &substitutedCleanupExecutor{lost: lost}
			result, err := f.coordinator.Release(ctx, owner.Claim.UID)
			if !errors.Is(err, executor.ErrOutcomeUnknown) || result.SafeToRemoveFinalizer {
				t.Errorf("wrong-asset cleanup authorized release: result=%+v err=%v", result, err)
			}
			if _, err := f.coordinator.Allocate(ctx, waiter); !errors.Is(err, ErrInsufficientCapacity) {
				t.Errorf("wrong-asset cleanup exposed occupied capacity: %v", err)
			}
		})
	}
}
