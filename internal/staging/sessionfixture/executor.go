package sessionfixture

import (
	"context"
	"fmt"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

type StoreExecutor struct {
	Store *simulator.Store
}

func (e StoreExecutor) Execute(_ context.Context, request simulator.Request) (simulator.OperationResult, error) {
	operation, lost, err := e.Store.Submit(request)
	if err != nil || lost {
		return simulator.OperationResult{}, fmt.Errorf("%w: synthetic submit: %v (lost=%t)", executor.ErrOutcomeUnknown, err, lost)
	}
	return operation.ResultView(), nil
}

func (e StoreExecutor) Lookup(_ context.Context, id string) (simulator.OperationResult, bool, error) {
	operation, found := e.Store.Get(id)
	return operation.ResultView(), found, nil
}
