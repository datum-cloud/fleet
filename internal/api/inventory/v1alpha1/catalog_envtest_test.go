package v1alpha1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestCatalogAPIConcurrencyAndStrictFields(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required for disposable catalog API validation")
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
	manufacturer, model := catalogRecords()
	device := syntheticDevice()
	for _, record := range []client.Object{manufacturer, model, &device} {
		if err := api.Create(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	stale := device.DeepCopyObject().(*Device)
	device.Spec.DeviceTypeRef = &LocalReference{Name: model.Name}
	if err := api.Update(ctx, &device); err != nil {
		t.Fatal(err)
	}
	stale.Spec.DeviceTypeRef = &LocalReference{Name: "different"}
	if err := api.Update(ctx, stale); !apierrors.IsConflict(err) {
		t.Fatalf("stale writer did not conflict: %v", err)
	}
	device.Spec.DeviceTypeRef = nil
	if err := api.Update(ctx, &device); !apierrors.IsInvalid(err) {
		t.Fatalf("reference removal accepted: %v", err)
	}
	model.Spec.DisplayName = "Edited catalog only"
	if err := api.Update(ctx, model); err != nil {
		t.Fatal(err)
	}
	var unchanged Device
	if err := api.Get(ctx, client.ObjectKeyFromObject(&device), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Spec.Identity != syntheticDevice().Spec.Identity || unchanged.Spec.DeviceTypeRef.Name != model.Name || len(unchanged.Spec.Components) != 0 {
		t.Fatal("catalog edit changed physical instance")
	}
	core, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	invalid := object(t, syntheticDevice())
	invalid["metadata"].(map[string]any)["name"] = "invalid-reference"
	invalid["spec"].(map[string]any)["deviceTypeRef"] = map[string]any{"name": model.Name, "uid": "not-a-spec-field", "namespace": "other"}
	body, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	err = core.RESTClient().Post().AbsPath("/apis/fleet.datumapis.com/v1alpha1/namespaces/fleet-system/devices").Param("fieldValidation", "Strict").Body(body).Do(ctx).Error()
	if !apierrors.IsBadRequest(err) {
		t.Fatalf("strict API did not reject unknown reference fields: %v", err)
	}
	for _, entry := range []struct {
		resource string
		record   any
	}{
		{"manufacturers", manufacturer},
		{"devicetypes", model},
		{"devices", syntheticDevice()},
	} {
		for _, retired := range []string{"provenance", "sourceIdentity"} {
			t.Run(entry.resource+"/"+retired, func(t *testing.T) {
				candidate := object(t, entry.record)
				candidate["metadata"] = map[string]any{"name": "retired-field", "namespace": "fleet-system"}
				spec := candidate["spec"].(map[string]any)
				if entry.resource == "devices" && retired == "sourceIdentity" {
					spec = spec["identity"].(map[string]any)
				}
				spec[retired] = map[string]any{"system": "synthetic", "id": "1"}
				body, err := json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
				err = core.RESTClient().Post().AbsPath("/apis/fleet.datumapis.com/v1alpha1/namespaces/fleet-system/"+entry.resource).Param("fieldValidation", "Strict").Body(body).Do(ctx).Error()
				if !apierrors.IsBadRequest(err) {
					t.Fatalf("strict API accepted retired field: %v", err)
				}
			})
		}
	}
}
