package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const registrationServiceAccount = "system:serviceaccount:fleet-system:fleet-registration"

func applyManifest(t *testing.T, ctx context.Context, api client.Client, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var object unstructured.Unstructured
		err := decoder.Decode(&object.Object)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(object.Object) == 0 {
			continue
		}
		if err := api.Create(ctx, &object); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(path, err)
		}
	}
}

func eventually(t *testing.T, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", what, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRegistrationServiceAccountIsConfinedToLedgerNamespace runs the ledger
// protocol as the registration service account with only the shipped Role,
// RoleBinding and policies, and proves that identity cannot read the
// manager's private records.
func TestRegistrationServiceAccountIsConfinedToLedgerNamespace(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	environment := &envtest.Environment{}
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
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, name := range []string{"fleet-system", "fleet-system-private"} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join("..", "..", "config", "base", "registration")
	for _, manifest := range []string{"namespace.yaml", "rbac.yaml", "protection.yaml"} {
		applyManifest(t, ctx, admin, filepath.Join(base, manifest))
	}
	managerRecord := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "fleet-synthetic-private-record", Namespace: "fleet-system-private"}, Data: map[string][]byte{"record": []byte("synthetic")}}
	if err := admin.Create(ctx, managerRecord); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(Ledger{Authority: "milo", Projects: []string{"provider"}})
	if err := admin.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: LedgerNamespace}, Data: map[string][]byte{"ledger.json": encoded}}); err != nil {
		t.Fatal(err)
	}

	// The real identity: the service account and the groups the API server
	// gives it, without system:masters.
	serviceConfig := rest.CopyConfig(config)
	serviceConfig.Impersonate = rest.ImpersonationConfig{UserName: registrationServiceAccount, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:fleet-system", "system:authenticated"}}
	service, err := client.New(serviceConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := fixture(t)
	handler.Store = Store{Client: service, Namespace: LedgerNamespace, Authority: "milo", Projects: []string{"provider"}, Cache: NewSegmentCache()}
	key := controlFixture(t, handler)
	// Policy enforcement is eventually consistent and can briefly lapse while
	// the API server reloads a new policy, so require a sustained run of
	// denials before any service write.
	eventually(t, "ledger protection policy did not engage consistently", func() error {
		for range 40 {
			err := admin.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: LedgerNamespace}}, client.DryRunAll)
			if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
				return fmt.Errorf("root deletion not denied: %v", err)
			}
			err = admin.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: LedgerNamespace}}, client.DryRunAll)
			if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
				return fmt.Errorf("unrelated Secret not denied: %v", err)
			}
			time.Sleep(25 * time.Millisecond)
		}
		return nil
	})
	eventually(t, "service account cannot read the root through the shipped Role", func() error {
		_, _, err := handler.Store.Read(ctx)
		return err
	})

	// (a) Every ledger operation works with least privilege: read, write,
	// signed expansion (segment create plus root CAS), activation, and reads
	// through the sealed segment.
	secret, ledger, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ledger.DeviceFences = append(ledger.DeviceFences, DeviceFence{Project: "provider", UID: "sealed", Aliases: []string{"uuid:sealed"}})
	if err := handler.Store.Write(ctx, secret, ledger); err != nil {
		t.Fatal("least-privilege write", err)
	}
	if err := handler.ApplyControl(ctx, controlFor(t, handler, key, "expand")); err != nil {
		t.Fatal("least-privilege expansion", err)
	}
	secret, ledger, err = handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ledger.DeviceFences = append(ledger.DeviceFences, DeviceFence{Project: "provider", UID: "tail", Aliases: []string{"uuid:tail"}})
	if err := handler.Store.Write(ctx, secret, ledger); err != nil {
		t.Fatal("least-privilege tail write", err)
	}
	if err := handler.ApplyControl(ctx, controlFor(t, handler, key, "activate")); err != nil {
		t.Fatal("least-privilege activation", err)
	}
	if err := handler.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	_, route, err := handler.Store.root(ctx)
	if err != nil || route.Epoch != 1 || len(route.Segments) != 1 {
		t.Fatalf("expansion did not commit: %+v %v", route, err)
	}
	_, ledger, err = handler.Store.Read(ctx)
	if err != nil || len(ledger.DeviceFences) != 2 {
		t.Fatalf("least-privilege read lost reservations: %+v %v", ledger, err)
	}

	// (b) The same identity is denied the manager's private records.
	forbidden := func(what string, err error) {
		t.Helper()
		if !apierrors.IsForbidden(err) {
			t.Fatalf("%s: expected Forbidden, got %v", what, err)
		}
	}
	forbidden("get manager private record", service.Get(ctx, client.ObjectKeyFromObject(managerRecord), &corev1.Secret{}))
	forbidden("get root name in manager namespace", service.Get(ctx, client.ObjectKey{Namespace: "fleet-system-private", Name: LedgerName}, &corev1.Secret{}))
	forbidden("list manager private records", service.List(ctx, &corev1.SecretList{}, client.InNamespace("fleet-system-private")))
	forbidden("create in manager namespace", service.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SegmentPrefix + "elsewhere", Namespace: "fleet-system-private"}}))
	forbidden("list ledger secrets", service.List(ctx, &corev1.SecretList{}, client.InNamespace(LedgerNamespace)))
	forbidden("list secrets cluster-wide", service.List(ctx, &corev1.SecretList{}))
	segment := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: route.Segments[0].Name, Namespace: LedgerNamespace}}
	forbidden("delete segment", service.Delete(ctx, segment))
	forbidden("delete root", service.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: LedgerNamespace}}))
	// Even an administrator cannot park other Secrets beside the ledger.
	if err := admin.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: LedgerNamespace}}); !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
		t.Fatalf("ledger namespace accepted an unrelated Secret: %v", err)
	}
	if err := admin.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: LedgerNamespace}}); !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
		t.Fatalf("ledger namespace deletion bypass: %v", err)
	}
}
