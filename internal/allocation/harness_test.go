package allocation

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/delivery"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
	"go.datum.net/fleet/internal/site/simwork"
	"go.datum.net/fleet/internal/site/siteapi"
)

const (
	namespace    = "fleet-provider"
	providerUser = "system:serviceaccount:fleet-system:metal-provider"
	releaseUser  = "system:serviceaccount:fleet-system:release-controller"
	siteName     = "site-example-a"
	siteToken    = "synthetic-site-token"
	keyDigest    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	imageRef     = "debian-12@sha256:9f4ad8d8b8a6f6fc64d2cf2c7e70b9184b74f089adf8c36d84f4fcb99892f3cb"
	policyDigest = "sha256:0c2b77e1d9a4000000000000000000000000000000000000000000000000cafe"
)

func uid(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// env is a real API server with the metal, inventory and custody CRDs, one
// simulator site behind its HTTP API, and the coordinator loop.
type env struct {
	t          *testing.T
	ctx        context.Context
	admin      client.Client
	allocator  *Allocator
	loop       *delivery.Loop
	store      *site.Store
	runner     *site.Runner
	enrollment fixture.Enrollment
	class      *fleetv1.MachineClass
	profile    *fleetv1.MachineProfile
}

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, inventory.AddToScheme, fleetv1.AddToScheme, ops.AddToScheme, networkingv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func startEnv(t *testing.T) *env {
	t.Helper()
	return startEnvWith(t)
}

// startEnvWith starts the environment with extra CRDs, such as NSO's.
func startEnvWith(t *testing.T, crds ...*apiextensionsv1.CustomResourceDefinition) *env {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS required")
	}
	ctrllog.SetLogger(logr.Discard())
	root := filepath.Join("..", "..")
	environment := &envtest.Environment{CRDDirectoryPaths: []string{
		filepath.Join(root, "config", "base", "crd"),
		filepath.Join(root, "config", "components", "inventory", "crds"),
		filepath.Join(root, "config", "components", "device-operations", "crds"),
		filepath.Join(root, "internal", "bootaddress", "testdata"),
	}, CRDs: crds, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = environment.Stop() })
	admin, err := client.New(config, client.Options{Scheme: scheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e := &env{t: t, ctx: ctx, admin: admin, enrollment: fixture.Enrollment{}}
	e.create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.store, err = site.OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &site.Runner{Site: siteName, Store: e.store, Results: site.NewResults(e.store), Access: fixture.Access{Enrollment: e.enrollment},
		Kinds: map[string]site.Kind{"DeviceInstallation": simwork.Installation{}, "DeviceWipe": simwork.Wipe{}}, Poll: 20 * time.Millisecond}
	e.runner = runner
	dispatcher := &siteapi.Dispatcher{Runner: runner, Context: ctx}
	server := httptest.NewServer(siteapi.Handler(dispatcher, e.store, runner.Results, func() (string, error) { return siteToken, nil }))
	t.Cleanup(func() { cancel(); server.Close(); dispatcher.Wait() })
	remote, err := siteapi.NewClient(server.URL, func() (string, error) { return siteToken, nil }, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := custody.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	sites := &delivery.Sites{Sites: map[string]delivery.Site{siteName: remote}, Devices: map[string][]string{}, Methods: map[string][]custody.OfferedMethod{siteName: custody.NetworkBootOnly}}
	coordinator := &custody.Coordinator{Client: admin, Registry: registry, Sites: sites, Config: custody.Config{Namespace: namespace, Principals: custody.Principals{
		custody.CreatorMetalProvider: {{Username: providerUser}}, custody.CreatorReleaseController: {{Username: releaseUser}},
	}}}
	e.loop = &delivery.Loop{Client: admin, Coordinator: coordinator, Namespace: namespace,
		Courier: &delivery.Courier{Client: admin, Registry: registry, Coordinator: coordinator, Sites: sites, Namespace: namespace}}
	e.allocator = &Allocator{
		Provider: admin, ProviderCreator: ops.Creator{Username: providerUser, UID: uid(801)},
		Releaser: admin, ReleaseCreator: ops.Creator{Username: releaseUser, UID: uid(802)},
		Custody: coordinator, Namespace: namespace,
		Cleaning:      func(string) (string, error) { return policyDigest, nil },
		BootInterface: "primary", TargetDisks: []string{"disk-0"},
		DeliveryMethods: custody.NetworkBootOnly,
	}
	e.class = &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "class-example"}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: "profile-example"}}}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: namespace, Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "example"}}}}}}
	e.create(e.class)
	e.profile = &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "profile-example"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 2}, Memory: fleetv1.MemoryRequirement{Capacity: "2Gi"}}}
	e.create(e.profile)
	return e
}

func (e *env) create(object client.Object) {
	e.t.Helper()
	if err := e.admin.Create(e.ctx, object); err != nil {
		e.t.Fatal(err)
	}
}

// server adds a synthetic Device, its enrollment at the site, its capacity
// and fixture discovery.
func (e *env) server(n int) *inventory.Device {
	e.t.Helper()
	name := fmt.Sprintf("server-example-%03d", n)
	mac := fmt.Sprintf("02:00:00:00:00:%02x", n)
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: inventory.DeviceSpec{
		ClaimRef:   inventory.LocalReference{Name: "claim-" + name},
		Identity:   inventory.DeviceIdentity{Model: "example", Serial: "serial-" + name, SystemUUID: uid(100 + n)},
		BMC:        &inventory.BMC{Endpoint: fmt.Sprintf("https://[2001:db8::%x]", n)},
		Interfaces: []inventory.Interface{{Name: "primary", MACAddress: mac}},
	}}
	e.create(device)
	key := fmt.Sprintf("%064x", n)
	e.enrollment[namespace+"/"+name] = fixture.Target{BMCEndpoint: device.Spec.BMC.Endpoint, SystemUUID: uid(100 + n), BootMACAddress: mac, BMCPublicKeySHA256: key, Disks: []string{"disk-0"}}
	e.loop.Courier.Sites.Devices[name] = []string{siteName}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "capacity-" + name, Namespace: namespace, Labels: map[string]string{"pool": "example"}},
		Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: name}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "example-cleaning"}, ReclaimPolicy: "Retain"}}
	e.create(capacity)
	capacity.Status.Hardware = fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 2}, Memory: fleetv1.HardwareMemory{Capacity: "2Gi"}}
	for _, kind := range []string{"Prepared", "Healthy", "Available"} {
		capacity.Status.Conditions = append(capacity.Status.Conditions, metav1.Condition{Type: kind, Status: metav1.ConditionTrue, Reason: "Fixture", ObservedGeneration: capacity.Generation, LastTransitionTime: metav1.Now()})
	}
	if err := e.admin.Status().Update(e.ctx, capacity); err != nil {
		e.t.Fatal(err)
	}
	if err := fixture.Discover(e.ctx, e.admin, fixture.Enrollment{namespace + "/" + name: e.enrollment[namespace+"/"+name]}, time.Now()); err != nil {
		e.t.Fatal(err)
	}
	e.until("custody is Verified", func() bool {
		state := e.custody(name)
		return state != nil && state.Spec.State == ops.CustodyVerified
	})
	return device
}

// until runs coordinator passes until done holds.
func (e *env) until(what string, done func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting until %s", what)
		}
		if err := e.loop.Pass(e.ctx); err != nil {
			e.t.Logf("pass: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) custody(name string) *ops.DeviceCustody {
	state := &ops.DeviceCustody{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: name}, state); err != nil {
		return nil
	}
	return state
}

func machine(n int) Machine {
	return Machine{Project: fmt.Sprintf("consumer-%d", n), ProjectUID: types.UID(uid(500 + n)), Namespace: "default", Name: "allocation", UID: types.UID(uid(600 + n))}
}

func (e *env) request(n int) Request {
	return Request{Machine: machine(n), Class: e.class, Profile: e.profile, Authority: fleetv1.AllocationAuthority{Principal: "fixture", Decision: fmt.Sprintf("fixture-%d", n)}}
}

func (e *env) installation(n int, name, predecessor string) *fleetv1.Installation {
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(uid(700 + n))},
		Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: "allocation"}, ImageRef: fleetv1.LocalReference{Name: imageRef}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}}}
	if predecessor != "" {
		installation.Spec.ExpectedPredecessor = fleetv1.InstallationPredecessor{Type: "Installation", Name: predecessor}
	}
	return installation
}

// release repeats Release while the coordinator runs, until it is Released.
func (e *env) release(allocation *fleetv1.MachineAllocation, owner Machine) {
	e.t.Helper()
	e.until("the allocation is Released", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
			e.t.Fatal(err)
		}
		released, err := e.allocator.Release(e.ctx, allocation, owner)
		if err != nil {
			e.t.Logf("release: %v", err)
		}
		return released
	})
}
