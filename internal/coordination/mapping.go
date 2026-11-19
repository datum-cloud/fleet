package coordination

import (
	"context"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// LocalAssetMapping is an explicit local-test binding of one project-scoped
// Device to the disposable provider's fixed physical ID. No alias or suffix
// matching is allowed. The exact original request remains durable in the local
// handoff and the backend retains its exact translated request before effects.
// This is a synthetic hardware enrollment seam, not production authority.
type LocalAssetMapping struct {
	API                                         dynamic.Interface
	Namespace, Cell, ScopedAsset, PhysicalAsset string
	Backend                                     executor.Client
}

func (m *LocalAssetMapping) request(ctx context.Context, id string) (Intent, simulator.Request, error) {
	obj, err := m.API.Resource(Resource).Namespace(m.Namespace).Get(ctx, Name(id), metav1.GetOptions{})
	if err != nil {
		return Intent{}, simulator.Request{}, err
	}
	i, _, err := decode(obj)
	if err != nil {
		return i, simulator.Request{}, err
	}
	if i.Cell != m.Cell || i.Request.ID != id || m.ScopedAsset == "" || m.PhysicalAsset == "" || i.Request.AssetID != m.ScopedAsset {
		return i, simulator.Request{}, executor.ErrFenced
	}
	translated := i.Request
	translated.AssetID = m.PhysicalAsset
	return i, translated, nil
}
func (m *LocalAssetMapping) translate(i Intent, translated simulator.Request, r simulator.OperationResult) (simulator.OperationResult, error) {
	if err := ValidateResult(Intent{Cell: i.Cell, Request: translated}, r); err != nil {
		return simulator.OperationResult{}, err
	}
	r.InputDigest, _ = simulator.RequestDigest(i.Request)
	return r, nil
}
func (m *LocalAssetMapping) Execute(ctx context.Context, r simulator.Request) (simulator.OperationResult, error) {
	i, t, err := m.request(ctx, r.ID)
	if err != nil {
		return simulator.OperationResult{}, err
	}
	if Digest(Intent{Cell: i.Cell, Request: r}) != Digest(i) {
		return simulator.OperationResult{}, executor.ErrConflict
	}
	result, err := m.Backend.Execute(ctx, t)
	if err != nil {
		return simulator.OperationResult{}, err
	}
	return m.translate(i, t, result)
}
func (m *LocalAssetMapping) Lookup(ctx context.Context, id string) (simulator.OperationResult, bool, error) {
	i, t, err := m.request(ctx, id)
	if err != nil {
		return simulator.OperationResult{}, false, err
	}
	r, found, err := m.Backend.Lookup(ctx, id)
	if err != nil || !found {
		return r, found, err
	}
	result, err := m.translate(i, t, r)
	return result, true, err
}
