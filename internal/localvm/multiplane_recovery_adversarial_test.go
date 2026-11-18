package localvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"go.datum.net/fleet/internal/coordination"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func TestAdversarialCellResumesRetainedInstallerWithoutSecondStart(t *testing.T) {
	ctx := context.Background()
	fixture := newVMFixture(t)
	fixture.failStart = true
	request := vmRequest("cell-recovery")
	intent := coordination.Intent{Cell: "cell-a", Request: request}
	name := coordination.Name(request.ID)
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), handoff(t, intent))
	mapping := &coordination.LocalAssetMapping{API: api, Namespace: "private", Cell: "cell-a", ScopedAsset: request.AssetID, PhysicalAsset: AssetID, Backend: fixture.p}
	cell := coordination.Cell{API: api, Namespace: "private", Name: "cell-a", Executor: mapping}
	if err := reconcile(&cell, ctx, name); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("lost installer response must remain unknown: %v", err)
	}
	journal, err := fixture.p.read()
	if err != nil {
		t.Fatal(err)
	}
	retained := journal.Operations[request.ID]
	if retained.Stage != "installer-started" || len(fixture.creates) != 1 {
		t.Fatal("did not retain exactly one uncertain installer start")
	}
	if err := os.WriteFile(fixture.p.directory(retained)+"/installer.log", []byte("FLEET-INSTALLED:"+retained.Receipt+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture.failStart = false
	restarted := coordination.Cell{API: api, Namespace: "private", Name: "cell-a", Executor: mapping}
	if err := reconcile(&restarted, ctx, name); err != nil {
		t.Fatalf("cell cannot resume exact retained request after installer completion: %v", err)
	}
	if len(fixture.creates) != 2 {
		t.Fatalf("want one installer and one disk boot, got %d starts", len(fixture.creates))
	}
	updated, err := api.Resource(coordination.Resource).Namespace("private").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(updated.Object, "status", "observation", "result", "phase")
	if phase != "Succeeded" {
		t.Fatalf("recovered terminal observation missing: %q", phase)
	}
}

func handoff(t *testing.T, intent coordination.Intent) *unstructured.Unstructured {
	t.Helper()
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(encoded, &spec); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": coordination.Resource.GroupVersion().String(),
		"kind":       coordination.Kind,
		"metadata":   map[string]interface{}{"name": coordination.Name(intent.Request.ID), "namespace": "private"},
		"spec":       spec,
	}}
}

// A cell restarted while the retained installer guest is still running must
// observe it, never boot a second installer, and never publish an observation.
// A different handoff for the same device must not act while it is unfinished.
func TestAdversarialCellRestartDuringRunningInstallerAndCompetingHandoff(t *testing.T) {
	fixture := newVMFixture(t)
	fixture.failStart = true
	request := vmRequest("running-install")
	cleanup := simulator.Request{ID: "competing-cleanup", Kind: "cleanup", AssetID: request.AssetID, BindingID: request.BindingID, Epoch: request.Epoch, FenceToken: request.FenceToken}
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), handoff(t, coordination.Intent{Cell: "cell-a", Request: request}), handoff(t, coordination.Intent{Cell: "cell-a", Request: cleanup}))
	mapping := &coordination.LocalAssetMapping{API: api, Namespace: "private", Cell: "cell-a", ScopedAsset: request.AssetID, PhysicalAsset: AssetID, Backend: fixture.p}
	name := coordination.Name(request.ID)
	cell := coordination.Cell{API: api, Namespace: "private", Name: "cell-a", Executor: mapping}
	if err := reconcile(&cell, context.Background(), name); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("lost start must be unknown: %v", err)
	}
	journal, err := fixture.p.read()
	if err != nil {
		t.Fatal(err)
	}
	fixture.present = domain(journal.Operations[request.ID])
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		restarted := coordination.Cell{API: api, Namespace: "private", Name: "cell-a", Executor: mapping}
		err := reconcile(&restarted, ctx, name)
		cancel()
		if !errors.Is(err, executor.ErrOutcomeUnknown) {
			t.Fatalf("running installer must remain unknown: %v", err)
		}
	}
	if err := reconcile(&cell, context.Background(), coordination.Name(cleanup.ID)); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("competing handoff acted during unfinished install: %v", err)
	}
	if len(fixture.creates) != 1 {
		t.Fatalf("restart started another guest: %v", fixture.creates)
	}
	journal, err = fixture.p.read()
	if err != nil {
		t.Fatal(err)
	}
	if journal.Operations[cleanup.ID] != nil || journal.Operations[request.ID].Stage != "installer-started" {
		t.Fatal("unfinished install was superseded")
	}
	for _, id := range []string{request.ID, cleanup.ID} {
		obj, err := api.Resource(coordination.Resource).Namespace("private").Get(context.Background(), coordination.Name(id), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, found, _ := unstructured.NestedMap(obj.Object, "status"); found {
			t.Fatalf("unknown outcome published for %s", id)
		}
	}
}

// reconcile composes the site worker (Execute) and observer (Publish) roles.
func reconcile(c *coordination.Cell, ctx context.Context, name string) error {
	if err := c.Execute(ctx, name); err != nil {
		return err
	}
	return c.Publish(ctx, name)
}
