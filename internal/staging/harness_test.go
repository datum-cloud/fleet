package staging

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"go.datum.net/fleet/internal/allocation"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/delivery"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
	"go.datum.net/fleet/internal/site/simwork"
	"go.datum.net/fleet/internal/site/siteapi"
)

const (
	providerNamespace = "fleet-provider"
	providerUser      = "system:serviceaccount:fleet-system:metal-provider"
	releaseUser       = "system:serviceaccount:fleet-system:release-controller"
	siteName          = "site-example-a"
	siteToken         = "synthetic-site-token"
	policyDigest      = "sha256:0c2b77e1d9a4000000000000000000000000000000000000000000000000cafe"
	locationName      = "us-central-1"
	className         = "central-metal"
	profileName       = "general-purpose-24c-128g"
)

func uid(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// stack is a real API server holding the provider project: the metal,
// inventory and custody CRDs, simulator Devices behind one site's HTTP API,
// and the custody coordinator loop. The tests' same-project consumer lives in
// it too; cross-project consumers are separate clients.
type stack struct {
	t         *testing.T
	ctx       context.Context
	admin     client.Client
	allocator *allocation.Allocator
	loop      *delivery.Loop
	servers   fixture.Enrollment
	sites     *delivery.Sites
	class     *fleetv1.MachineClass
	profile   *fleetv1.MachineProfile
}

func stackScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, inventory.AddToScheme, fleetv1.AddToScheme, ops.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func startStack(t *testing.T) *stack {
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
	}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = environment.Stop() })
	admin, err := client.New(config, client.Options{Scheme: stackScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &stack{t: t, ctx: ctx, admin: admin}
	s.create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: providerNamespace}})

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := fixture.Enrollment{}
	runner := &site.Runner{Site: siteName, Store: store, Results: site.NewResults(store), Access: fixture.Access{Enrollment: enrollment},
		Kinds: map[string]site.Kind{"DeviceInstallation": simwork.Installation{}, "DeviceWipe": simwork.Wipe{}}, Poll: 20 * time.Millisecond}
	dispatcher := &siteapi.Dispatcher{Runner: runner, Context: ctx}
	server := httptest.NewServer(siteapi.Handler(dispatcher, store, runner.Results, custody.NetworkBootOnly, func() (string, error) { return siteToken, nil }))
	t.Cleanup(func() { cancel(); server.Close(); dispatcher.Wait() })
	remote, err := siteapi.NewClient(server.URL, func() (string, error) { return siteToken, nil }, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := custody.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	sites := &delivery.Sites{Sites: map[string]delivery.Site{siteName: remote}, Devices: map[string][]string{}}
	coordinator := &custody.Coordinator{Client: admin, Registry: registry, Sites: sites, Config: custody.Config{Namespace: providerNamespace, Principals: custody.Principals{
		custody.CreatorMetalProvider: {{Username: providerUser}}, custody.CreatorReleaseController: {{Username: releaseUser}},
	}}}
	s.loop = &delivery.Loop{Client: admin, Coordinator: coordinator, Namespace: providerNamespace,
		Courier: &delivery.Courier{Client: admin, Registry: registry, Coordinator: coordinator, Sites: sites, Namespace: providerNamespace}}
	s.allocator = &allocation.Allocator{
		Provider: admin, ProviderCreator: ops.Creator{Username: providerUser, UID: uid(801)},
		Releaser: admin, ReleaseCreator: ops.Creator{Username: releaseUser, UID: uid(802)},
		Custody: coordinator, Namespace: providerNamespace,
		Cleaning:      func(string) (string, error) { return policyDigest, nil },
		BootInterface: "primary", TargetDisks: []string{"disk-0"},
		Sites: sites,
	}
	s.servers = enrollment
	s.sites = sites
	s.class = &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: className}, Spec: fleetv1.MachineClassSpec{ProfilePolicy: &fleetv1.ProfilePolicy{AllowedProfiles: []fleetv1.LocalReference{{Name: profileName}}}, Access: &fleetv1.AccessPolicy{}, Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: providerNamespace, Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "example"}}}}}}
	s.create(s.class)
	s.profile = &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: profileName}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 24}, Memory: fleetv1.MemoryRequirement{Capacity: "128Gi"}}}
	s.create(s.profile)
	return s
}

func (s *stack) create(object client.Object) {
	s.t.Helper()
	if err := s.admin.Create(s.ctx, object); err != nil {
		s.t.Fatal(err)
	}
}

// server adds a synthetic Device, its enrollment at the site, its capacity
// and fixture discovery, and waits until custody is Verified. The capacity
// is named capacity-<device>.
func (s *stack) server(n int) *inventory.Device {
	s.t.Helper()
	name := fmt.Sprintf("server-example-%03d", n)
	mac := fmt.Sprintf("02:00:00:00:00:%02x", n)
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: providerNamespace}, Spec: inventory.DeviceSpec{
		ClaimRef:   inventory.LocalReference{Name: "claim-" + name},
		Identity:   inventory.DeviceIdentity{Model: "example", Serial: "serial-" + name, SystemUUID: uid(100 + n)},
		BMC:        &inventory.BMC{Endpoint: fmt.Sprintf("https://[2001:db8::%x]", n)},
		Interfaces: []inventory.Interface{{Name: "primary", MACAddress: mac}},
	}}
	s.create(device)
	target := fixture.Target{BMCEndpoint: device.Spec.BMC.Endpoint, SystemUUID: uid(100 + n), BootMACAddress: mac, BMCPublicKeySHA256: fmt.Sprintf("%064x", n), Disks: []string{"disk-0"}}
	s.servers[providerNamespace+"/"+name] = target
	s.sites.Devices[name] = []string{siteName}
	capacity := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "capacity-" + name, Namespace: providerNamespace, Labels: map[string]string{"pool": "example"}},
		Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: name}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "example-cleaning"}, ReclaimPolicy: "Retain"}}
	s.create(capacity)
	capacity.Status.Hardware = fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64", Cores: 24, Threads: 48, Sockets: 2}, Memory: fleetv1.HardwareMemory{Capacity: "128Gi"}}
	for _, kind := range []string{"Prepared", "Healthy", "Available"} {
		capacity.Status.Conditions = append(capacity.Status.Conditions, metav1.Condition{Type: kind, Status: metav1.ConditionTrue, Reason: "Fixture", ObservedGeneration: capacity.Generation, LastTransitionTime: metav1.Now()})
	}
	if err := s.admin.Status().Update(s.ctx, capacity); err != nil {
		s.t.Fatal(err)
	}
	if err := fixture.Discover(s.ctx, s.admin, fixture.Enrollment{providerNamespace + "/" + name: target}, time.Now()); err != nil {
		s.t.Fatal(err)
	}
	s.until("custody is Verified", func() bool {
		state := s.custody(name)
		return state != nil && state.Spec.State == ops.CustodyVerified
	})
	return device
}

// until runs coordinator passes until done holds.
func (s *stack) until(what string, done func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting until %s", what)
		}
		if err := s.loop.Pass(s.ctx); err != nil {
			s.t.Logf("pass: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *stack) custody(name string) *ops.DeviceCustody {
	state := &ops.DeviceCustody{}
	if err := s.admin.Get(s.ctx, client.ObjectKey{Namespace: providerNamespace, Name: name}, state); err != nil {
		return nil
	}
	return state
}

var consumerProject = controller.ProjectIdentity{AuthorityID: "staging", RouteName: "consumer-example", UID: types.UID(uid(11))}

// handler is the staging handler for same-project claims: the consumer shares
// the provider's API server.
func (s *stack) handler() Handler {
	return Handler{Public: s.admin, Allocator: s.allocator, Project: consumerProject, Location: locationName, Now: time.Now}
}

// claim creates a powered-off Machine in the default namespace.
func (s *stack) claim(name string) *fleetv1.Machine {
	s.t.Helper()
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: className}, MachineProfileRef: fleetv1.LocalReference{Name: profileName}, LocationRef: fleetv1.LocalReference{Name: locationName}, PowerState: "Off"}}
	s.create(claim)
	return claim
}

func (s *stack) get(claim *fleetv1.Machine) *fleetv1.Machine {
	s.t.Helper()
	var current fleetv1.Machine
	if err := s.admin.Get(s.ctx, client.ObjectKeyFromObject(claim), &current); err != nil {
		s.t.Fatal(err)
	}
	return &current
}

func projectRequest(project controller.ProjectIdentity, claim *fleetv1.Machine) manager.ProjectRequest {
	return manager.ProjectRequest{AuthorityID: project.AuthorityID, ProjectUID: project.UID, Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}
}

// drive handles n events for the claim and fails on any retry error.
func drive(t *testing.T, ctx context.Context, handler manager.Handler, project controller.ProjectIdentity, claim *fleetv1.Machine, n int) {
	t.Helper()
	for range n {
		if out := handler.ReconcileEvent(ctx, projectRequest(project, claim)); out.RetryError != nil {
			t.Fatal(out.RetryError)
		}
	}
}

func machineIdentity(project controller.ProjectIdentity, claim *fleetv1.Machine) allocation.Machine {
	return allocation.Machine{Project: project.RouteName, ProjectUID: project.UID, Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}
}
