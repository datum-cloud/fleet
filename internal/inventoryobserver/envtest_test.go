package inventoryobserver

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	inventoryv1 "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

var observerRoot = filepath.Join("..", "..", "config", "inventory-observer")

// TestObserverAgainstAPIServer runs the observer as its packaged service
// account against a real API server: the CRD's validation, the status
// subresource, and the packaged Role all apply as they would in a cluster.
func TestObserverAgainstAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set")
	}
	env := &envtest.Environment{CRDInstallOptions: envtest.CRDInstallOptions{Paths: []string{filepath.Join(observerRoot, "crd.yaml")}}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, inventoryv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{namespace, "other"} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, object := range packagedRBAC(t) {
		if err := admin.Create(ctx, object); err != nil {
			t.Fatalf("create %s %s: %v", object.GetKind(), object.GetName(), err)
		}
	}
	if err := admin.Create(ctx, observation("server-01", 1234, "server-01.rack-01.example")); err != nil {
		t.Fatal(err)
	}
	other := observation("elsewhere", 1234, "")
	other.Namespace = "other"
	if err := admin.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	user, err := env.AddUser(envtest.User{Name: "system:serviceaccount:fleet-system:fleet-inventory-observer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	observerClient, err := client.New(user.Config(), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	results, err := (&Observer{Client: observerClient, Namespace: namespace, NetBox: syntheticSource()}).RefreshAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Succeeded() {
		t.Fatalf("results = %+v", results)
	}
	stored := get(t, admin, "server-01")
	if stored.Status.Facts == nil || stored.Status.Facts.Serial != "SYNTHETIC-0001" || stored.Status.TrustProfile != inventoryv1.TrustProfileObservedOnly {
		t.Fatalf("status not persisted: %+v", stored.Status)
	}

	t.Run("observer cannot create, delete, or edit spec", func(t *testing.T) {
		if err := observerClient.Create(ctx, observation("created-by-observer", 1, "")); !apierrors.IsForbidden(err) {
			t.Errorf("create err = %v, want Forbidden", err)
		}
		if err := observerClient.Delete(ctx, stored.DeepCopy()); !apierrors.IsForbidden(err) {
			t.Errorf("delete err = %v, want Forbidden", err)
		}
		edited := stored.DeepCopy()
		edited.Labels = map[string]string{"example": "observer"}
		if err := observerClient.Update(ctx, edited); !apierrors.IsForbidden(err) {
			t.Errorf("spec/metadata update err = %v, want Forbidden", err)
		}
	})

	t.Run("observer cannot read outside its namespace or other kinds", func(t *testing.T) {
		var list inventoryv1.InventoryObservationList
		if err := observerClient.List(ctx, &list, client.InNamespace("other")); !apierrors.IsForbidden(err) {
			t.Errorf("list other namespace err = %v, want Forbidden", err)
		}
		var secrets corev1.SecretList
		if err := observerClient.List(ctx, &secrets, client.InNamespace(namespace)); !apierrors.IsForbidden(err) {
			t.Errorf("list secrets err = %v, want Forbidden", err)
		}
	})

	t.Run("source identity is immutable", func(t *testing.T) {
		retarget := get(t, admin, "server-01")
		retarget.Spec.Source.ID = 1235
		if err := admin.Update(ctx, retarget); !apierrors.IsInvalid(err) {
			t.Errorf("retarget err = %v, want Invalid", err)
		}
	})

	t.Run("status write with a stale resourceVersion conflicts", func(t *testing.T) {
		stale := get(t, admin, "server-01")
		fresh := stale.DeepCopy()
		fresh.Labels = map[string]string{"example": "concurrent"}
		if err := admin.Update(ctx, fresh); err != nil {
			t.Fatal(err)
		}
		result := (&Observer{Client: observerClient, Namespace: namespace, NetBox: syntheticSource()}).refresh(ctx, stale)
		if !apierrors.IsConflict(result.WriteErr) {
			t.Errorf("stale status write err = %v, want Conflict", result.WriteErr)
		}
	})

	t.Run("status cannot claim a stronger trust profile", func(t *testing.T) {
		current := get(t, admin, "server-01")
		current.Status.TrustProfile = "ServerWitnessedBMC-v1"
		if err := observerClient.Status().Update(ctx, current); !apierrors.IsInvalid(err) {
			t.Errorf("trust escalation err = %v, want Invalid", err)
		}
	})
}

func packagedRBAC(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(observerRoot, "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		if object != nil {
			out = append(out, &unstructured.Unstructured{Object: object})
		}
	}
}
