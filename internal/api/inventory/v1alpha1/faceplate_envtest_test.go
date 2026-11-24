package v1alpha1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestFaceplateLayoutOnTheAPIServer installs the CRDs into a disposable API
// server, which proves it accepts the rules' estimated cost, then checks each
// layout rule and the worst-case runtime cost against the real server.
func TestFaceplateLayoutOnTheAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required for disposable faceplate layout validation")
	}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(inventoryRoot, "crds")}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fleet-system"}}); err != nil {
		t.Fatal(err)
	}
	serial := 0
	create := func(model *DeviceType) error {
		serial++
		model.ResourceVersion = ""
		model.Name = fmt.Sprintf("layout-%d", serial)
		return api.Create(ctx, model)
	}

	valid := layoutSwitch()
	if err := create(valid); err != nil {
		t.Fatal(err)
	}
	var stored DeviceType
	if err := api.Get(ctx, client.ObjectKeyFromObject(valid), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Faceplate == nil || len(stored.Spec.Faceplate.Banks) != 5 || stored.Spec.ComponentTemplates[0].Layout == nil || stored.Spec.ComponentTemplates[0].Layout.Span != 0 || stored.Spec.ModelShape.Width != ChassisFullWidth {
		t.Fatalf("layout did not round-trip unchanged: %+v", stored.Spec)
	}
	if stored.Spec.Faceplate.Banks[0].Offset != nil {
		t.Fatal("the server invented an offset for a flowed bank")
	}
	// Removing the layout is an ordinary catalog edit.
	stored.Spec.Faceplate = nil
	for i := range stored.Spec.ComponentTemplates {
		stored.Spec.ComponentTemplates[i].Layout = nil
	}
	if err := api.Update(ctx, &stored); err != nil {
		t.Fatalf("clearing the layout rejected: %v", err)
	}

	if err := create(worstCaseLayout(128)); err != nil {
		t.Fatalf("worst-case layout within the cap rejected: %v", err)
	}

	for name, test := range map[string]struct {
		mutate  func(*DeviceType)
		message string
	}{
		"bank on the other face":   {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Face = ChassisRear }, "own face"},
		"layout without a face":    {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Face = "" }, "own face"},
		"unknown bank":             {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Layout.Bank = "missing" }, "own face"},
		"row outside the grid":     {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Layout.Row = 3 }, "inside its bank"},
		"column outside the grid":  {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Layout.Column = 13 }, "inside its bank"},
		"span outside the grid":    {func(m *DeviceType) { m.Spec.ComponentTemplates[56].Layout.Span = 2 }, "inside its bank"},
		"two templates in a cell":  {func(m *DeviceType) { m.Spec.ComponentTemplates[1].Layout = m.Spec.ComponentTemplates[0].Layout }, "overlap"},
		"span over a neighbour":    {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Layout.Span = 2 }, "overlap"},
		"offsets on part of face":  {func(m *DeviceType) { m.Spec.Faceplate.Banks[1].Offset = ptr(0.5) }, "offsets"},
		"offset missing on a bank": {func(m *DeviceType) { m.Spec.Faceplate.Banks[3].Offset = nil }, "offsets"},
		"over the layout cap": {func(m *DeviceType) {
			*m = *worstCaseLayout(129)
		}, "at most 128 templates"},
	} {
		t.Run(name, func(t *testing.T) {
			model := layoutSwitch()
			test.mutate(model)
			err := create(model)
			if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("want an invalid error containing %q, got %v", test.message, err)
			}
			if strings.Contains(err.Error(), "cost") {
				t.Fatalf("rejected by CEL cost, not by the rule: %v", err)
			}
		})
	}

	device := syntheticDevice()
	device.Spec.ModelShape = &ModelShape{Width: ChassisHalfWidth}
	if err := api.Create(ctx, &device); err != nil {
		t.Fatalf("half-width Device rejected: %v", err)
	}
	wide := syntheticDevice()
	wide.Name, wide.Spec.Identity.Serial, wide.Spec.Identity.SystemUUID = "wide", "SYNTHETIC-0002", "00000000-0000-4000-8000-000000000002"
	wide.Spec.ModelShape = &ModelShape{Width: "Double"}
	if err := api.Create(ctx, &wide); !apierrors.IsInvalid(err) {
		t.Fatalf("unknown width accepted: %v", err)
	}
}
