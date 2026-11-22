package registration

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type faultClient struct {
	client.Client
	createAfter  bool
	createBefore bool
	updateAt     int
	updates      int
	updateAfter  bool
	crash        bool
}

func (fault *faultClient) fail() error {
	if fault.crash {
		os.Exit(23)
	}
	return context.DeadlineExceeded
}

func (fault *faultClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if fault.createBefore {
		return fault.fail()
	}
	if err := fault.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	if fault.createAfter {
		return fault.fail()
	}
	return nil
}

func (fault *faultClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	fault.updates++
	if fault.updates == fault.updateAt && !fault.updateAfter {
		return fault.fail()
	}
	if err := fault.Client.Update(ctx, object, options...); err != nil {
		return err
	}
	if fault.updates == fault.updateAt && fault.updateAfter {
		return fault.fail()
	}
	return nil
}

func TestRegistrationExpansionWorker(t *testing.T) {
	configPath := os.Getenv("FLEET_TEST_CONFIG")
	if configPath == "" {
		return
	}
	config, err := clientcmd.BuildConfigFromFlags("", configPath)
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(os.Getenv("FLEET_TEST_PUBLIC"))
	if err != nil {
		t.Fatal(err)
	}
	public, err := ParseControlKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Client: api, Namespace: os.Getenv("FLEET_TEST_NAMESPACE"), Authority: "milo", Projects: []string{"provider"}, ControlKey: public}
	fault := &faultClient{Client: api, crash: true}
	switch os.Getenv("FLEET_TEST_CRASH") {
	case "freeze":
		fault.updateAt, fault.updateAfter = 1, true
	case "segment-before":
		fault.createBefore = true
	case "segment-after":
		fault.createAfter = true
	case "switch-before":
		fault.updateAt = 2
	case "switch-after":
		fault.updateAt, fault.updateAfter = 2, true
	default:
		if err := store.ResumeExpansion(context.Background()); err != nil {
			t.Fatal(err)
		}
		return
	}
	store.Client = fault
	encoded, err = os.ReadFile(os.Getenv("FLEET_TEST_REQUEST"))
	if err != nil {
		t.Fatal(err)
	}
	control, err := ParseControl(encoded)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Store: store}
	if err := handler.ApplyControl(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fault boundary was not reached")
}

func TestRealLosslessExpansionAndProcessRecovery(t *testing.T) {
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
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	newHandler := func(t *testing.T, namespace string) (*Handler, ed25519.PrivateKey) {
		handler, _ := fixture(t)
		secret, ledger, err := handler.Store.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ledger.Entries = []Entry{{Operation: "admitted-not-persisted", Project: "provider", ProjectUID: "project-1", NamespaceUID: "namespace-1", CapacityUID: "public-uid", DeviceUID: "device-1", DeviceVersion: "v1", Aliases: []string{"uuid:reserved"}, Input: json.RawMessage(`{"spec":{"allocationPolicy":"Disabled","reclaimPolicy":"Retain"}}`), Outcome: "Unknown"}}
		ledger.DeviceFences = []DeviceFence{{Project: "provider", UID: "legacy", Version: "old", Aliases: []string{"legacy-alias"}}, {Project: "provider", UID: "legacy", Version: "pending"}, {Project: "provider", UID: "deleted", Deleted: true, Aliases: []string{"never-release"}}}
		secret.Data["ledger.json"], _ = json.Marshal(ledger)
		secret.Namespace, secret.UID, secret.ResourceVersion = namespace, "", ""
		if err := api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
			t.Fatal(err)
		}
		if err := api.Create(ctx, secret); err != nil {
			t.Fatal(err)
		}
		handler.Store.Client, handler.Store.Namespace = api, namespace
		return handler, controlFixture(t, handler)
	}
	kubeconfig := clientcmdapi.Config{Clusters: map[string]*clientcmdapi.Cluster{"test": {Server: config.Host, CertificateAuthorityData: config.CAData}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {ClientCertificateData: config.CertData, ClientKeyData: config.KeyData}}, Contexts: map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}}, CurrentContext: "test"}
	kubeBytes, err := clientcmd.Write(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	kubePath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubePath, kubeBytes, 0600); err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"freeze", "segment-before", "segment-after", "switch-before", "switch-after"} {
		t.Run(boundary, func(t *testing.T) {
			handler, key := newHandler(t, boundary)
			stale, before, err := handler.Store.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			control := controlFor(t, handler, key, "expand")
			encoded, _ := json.Marshal(control)
			requestPath := filepath.Join(t.TempDir(), "request")
			if err := os.WriteFile(requestPath, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			publicDER, _ := x509.MarshalPKIXPublicKey(handler.Store.ControlKey)
			publicPath := filepath.Join(t.TempDir(), "public")
			if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0600); err != nil {
				t.Fatal(err)
			}
			runWorker := func(crash string) error {
				command := exec.Command(os.Args[0], "-test.run=^TestRegistrationExpansionWorker$")
				command.Env = append(os.Environ(), "FLEET_TEST_CONFIG="+kubePath, "FLEET_TEST_NAMESPACE="+handler.Store.Namespace, "FLEET_TEST_PUBLIC="+publicPath, "FLEET_TEST_REQUEST="+requestPath, "FLEET_TEST_CRASH="+crash)
				output, err := command.CombinedOutput()
				if err != nil && crash == "" {
					t.Log(string(output))
				}
				return err
			}
			var exit *exec.ExitError
			if err := runWorker(boundary); !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("expected injected process crash: %v", err)
			}
			if handler.Store.Write(ctx, stale, before) == nil {
				t.Fatal("pre-freeze writer committed")
			}
			if err := runWorker(""); err != nil {
				t.Fatal("restart", err)
			}
			_, after, err := handler.Store.Read(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("restart lost reservations: %v\n%+v", err, after)
			}
			_, route, err := handler.Store.root(ctx)
			if err != nil || route.Pending != nil || route.Epoch != 1 || len(route.Segments) != 1 {
				t.Fatalf("unfinished expansion: %+v %v", route, err)
			}
			var segments corev1.SecretList
			if err := api.List(ctx, &segments, client.InNamespace(handler.Store.Namespace)); err != nil || len(segments.Items) != 2 {
				t.Fatalf("orphan segments: %d %v", len(segments.Items), err)
			}
		})
	}
	t.Run("full-legacy-sealing", func(t *testing.T) {
		handler, key := newHandler(t, "full-legacy")
		secret, before, err := handler.Store.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		before.DeviceFences = append(before.DeviceFences, DeviceFence{Project: "provider", UID: "large", Aliases: []string{""}})
		encoded, _ := json.Marshal(before)
		before.DeviceFences[len(before.DeviceFences)-1].Aliases[0] = strings.Repeat("x", SegmentBudget-len(encoded))
		secret.Data["ledger.json"], _ = json.Marshal(before)
		if err := api.Update(ctx, secret); err != nil {
			t.Fatal(err)
		}
		if err := handler.ApplyControl(ctx, controlFor(t, handler, key, "expand")); err != nil {
			t.Fatal(err)
		}
		_, after, err := handler.Store.Read(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("full legacy data lost", err)
		}
		status, err := handler.Status(ctx)
		if err != nil || status.HeadroomBytes < TailBudget-1000 || status.Epoch != 1 || status.ActivationVerified {
			t.Fatalf("bad expanded bootstrap status: %+v %v", status, err)
		}
	})
	t.Run("multi-replica-and-growth", func(t *testing.T) {
		handler, key := newHandler(t, "replicas")
		_, before, _ := handler.Store.Read(ctx)
		control := controlFor(t, handler, key, "expand")
		var workers sync.WaitGroup
		for range 8 {
			workers.Add(1)
			go func() { defer workers.Done(); replica := *handler; _ = replica.ApplyControl(ctx, control) }()
		}
		workers.Wait()
		if err := handler.Store.ResumeExpansion(ctx); err != nil {
			t.Fatal(err)
		}
		if err := handler.ApplyControl(ctx, control); err != nil {
			t.Fatal("lost completion response cannot be reconciled", err)
		}
		secret, after, err := handler.Store.Read(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("concurrent migration lost history", err)
		}
		for attempt := 1; attempt < MaxSegments; attempt++ {
			after.DeviceFences = append(after.DeviceFences, DeviceFence{Project: "provider", UID: types.UID(fmt.Sprint("new-", attempt)), Aliases: []string{strings.Repeat("a", TailBudget-1000)}})
			if err := handler.Store.Write(ctx, secret, after); err != nil {
				t.Fatal(err)
			}
			secret, after, _ = handler.Store.Read(ctx)
			tooLarge := after
			tooLarge.DeviceFences = append(append([]DeviceFence(nil), after.DeviceFences...), DeviceFence{Project: "provider", UID: "oversize", Aliases: []string{strings.Repeat("b", 2000)}})
			if !errors.Is(handler.Store.Write(ctx, secret, tooLarge), ErrGrowthStopped) {
				t.Fatal("tail headroom not enforced")
			}
			if err := handler.ApplyControl(ctx, controlFor(t, handler, key, "expand")); err != nil {
				t.Fatal(err)
			}
			secret, after, err = handler.Store.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
		}
		if !errors.Is(handler.ApplyControl(ctx, controlFor(t, handler, key, "expand")), ErrGrowthStopped) {
			t.Fatal("unbounded expansion")
		}
		if after.Entries[0].Outcome != "Unknown" || len(after.DeviceFences) != len(before.DeviceFences)+MaxSegments-1 {
			t.Fatal("growth changed custody")
		}
		_, route, _ := handler.Store.root(ctx)
		segment := &corev1.Secret{}
		if err := api.Get(ctx, client.ObjectKey{Namespace: handler.Store.Namespace, Name: route.Segments[0].Name}, segment); err != nil {
			t.Fatal(err)
		}
		segment.Data["ledger.json"] = []byte("{}")
		if err := api.Update(ctx, segment); !apierrors.IsInvalid(err) {
			t.Fatal("segment mutable", err)
		}
		if err := api.Delete(ctx, segment); err != nil {
			t.Fatal(err)
		}
		if _, _, err := handler.Store.Read(ctx); err == nil {
			t.Fatal("missing segment treated as free")
		}
		segment.UID, segment.ResourceVersion = "", ""
		if err := api.Create(ctx, segment); err != nil {
			t.Fatal(err)
		}
		if _, _, err := handler.Store.Read(ctx); err == nil {
			t.Fatal("replacement segment adopted")
		}
	})
	t.Run("uncertain-write-and-read-only", func(t *testing.T) {
		handler, key := newHandler(t, "uncertain")
		if err := handler.ApplyControl(ctx, controlFor(t, handler, key, "expand")); err != nil {
			t.Fatal(err)
		}
		secret, ledger, _ := handler.Store.Read(ctx)
		ledger.DeviceFences = append(ledger.DeviceFences, DeviceFence{Project: "provider", UID: "uncertain", Aliases: []string{"retained"}})
		store := handler.Store
		store.Client = &faultClient{Client: api, updateAt: 1, updateAfter: true}
		if !errors.Is(store.Write(ctx, secret, ledger), context.DeadlineExceeded) {
			t.Fatal("write should be uncertain")
		}
		secret, observed, err := handler.Store.Read(ctx)
		if err != nil || !reflect.DeepEqual(ledger, observed) {
			t.Fatal("persist timeout released state", err)
		}
		store.Client = &faultClient{Client: api, updateAt: 1}
		observed.DeviceFences = append(observed.DeviceFences, DeviceFence{Project: "provider", UID: "denied"})
		if store.Write(ctx, secret, observed) == nil {
			t.Fatal("read-only storage admitted")
		}
		_, retained, err := handler.Store.Read(ctx)
		if err != nil || !reflect.DeepEqual(ledger, retained) {
			t.Fatal("read-only failure changed history", err)
		}
		retained.DeviceFences = nil
		if handler.Store.Write(ctx, secret, retained) == nil {
			t.Fatal("alias removal accepted")
		}
		role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: handler.Store.Namespace}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}}}
		if err := api.Create(ctx, role); err != nil {
			t.Fatal(err)
		}
		binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: handler.Store.Namespace}, Subjects: []rbacv1.Subject{{Kind: "User", Name: "ledger-reader"}}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader"}}
		if err := api.Create(ctx, binding); err != nil {
			t.Fatal(err)
		}
		readOnlyConfig := rest.CopyConfig(config)
		readOnlyConfig.Impersonate = rest.ImpersonationConfig{UserName: "ledger-reader", Groups: []string{"system:authenticated"}}
		readOnly, err := client.New(readOnlyConfig, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		store.Client = readOnly
		secret, observed, err = store.Read(ctx)
		if err != nil {
			t.Fatal("read-only observation should work", err)
		}
		observed.DeviceFences = append(observed.DeviceFences, DeviceFence{Project: "provider", UID: "forbidden"})
		if err := store.Write(ctx, secret, observed); !apierrors.IsForbidden(err) {
			t.Fatal("read-only storage wrote", err)
		}
	})
}
