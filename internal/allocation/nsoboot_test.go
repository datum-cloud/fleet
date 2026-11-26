package allocation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	"go.datum.net/fleet/internal/network/nso"
	"go.datum.net/fleet/internal/site/simwork"
)

const (
	bootNetworkName = "boot"
	ipamNamespace   = "ipam-provider"
	customerNetwork = "customer-a"
	detachFinalizer = "networking.datumapis.com/physical-detach"
	sharedPort      = "switch-example-a/ethernet7"
	siteLocation    = "location-example-a"
)

// nsoModule is the pinned NSO module.
const nsoModule = "go.datum.net/network-services-operator"

// nsoModuleDir finds the NSO module pinned in Fleet's go.mod in the module
// cache, without running the go command. It skips the test when the module's
// sources are not in the cache.
func nsoModuleDir(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require ")); len(fields) >= 2 && fields[0] == nsoModule {
			version = fields[1]
		}
	}
	cache := os.Getenv("GOMODCACHE")
	if cache == "" {
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Skip("no module cache to find the NSO module")
			}
			gopath = filepath.Join(home, "go")
		}
		cache = filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")
	}
	// The module path is lower case, so it needs no case escaping.
	dir := filepath.Join(cache, filepath.FromSlash(nsoModule)+"@"+version)
	if _, err := os.Stat(filepath.Join(dir, "config", "crd", "bases")); version == "" || err != nil {
		t.Skipf("NSO module %s@%s is not in the module cache", nsoModule, version)
	}
	return dir
}

// nsoCRDs loads NSO's Network, NetworkInterfaceClaim and NetworkInterface
// CRDs from the pinned module, with the assumed Physical attachment mode
// added.
func nsoCRDs(t *testing.T) []*apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	dir := filepath.Join(nsoModuleDir(t), "config", "crd", "bases")
	var crds []*apiextensionsv1.CustomResourceDefinition
	for _, name := range []string{"networks", "networkinterfaceclaims", "networkinterfaces"} {
		data, err := os.ReadFile(filepath.Join(dir, "networking.datumapis.com_"+name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(data, crd); err != nil {
			t.Fatal(err)
		}
		for _, version := range crd.Spec.Versions {
			if spec, ok := version.Schema.OpenAPIV3Schema.Properties["spec"]; ok {
				if mode, ok := spec.Properties["attachmentMode"]; ok {
					mode.Enum = append(mode.Enum, apiextensionsv1.JSON{Raw: []byte(`"` + string(nso.PhysicalMode) + `"`)})
					spec.Properties["attachmentMode"] = mode
					version.Schema.OpenAPIV3Schema.Properties["spec"] = spec
				}
			}
		}
		crds = append(crds, crd)
	}
	return crds
}

// realizer stands in for NSO's physical realizer as Fleet assumes it (see
// nso/assumed.go). It derives each port's membership from the claims that
// name it: the boot network only while a live boot attachment claim names
// the port, otherwise the networks of the live claims that name it. Each
// change is one assignment, recorded in history. It reports what is lost
// before what is gained, keeps a deleting claim until its port left the
// claim's network, and holds one IPAM allocation per boot address.
type realizer struct {
	api     client.Client
	mu      sync.Mutex
	ports   map[string][]string
	history map[string][][]string
	next    int
}

func newRealizer(api client.Client) *realizer {
	return &realizer{api: api, ports: map[string][]string{}, history: map[string][][]string{}}
}

func (r *realizer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
		_ = r.pass(ctx)
	}
}

func isBoot(claim *networkingv1.NetworkInterfaceClaim) bool {
	return claim.Namespace == namespace && claim.Spec.Network.Name == bootNetworkName
}

func portOf(claim *networkingv1.NetworkInterfaceClaim) string {
	point := nso.AttachmentPointOf(claim)
	if point.Switch == "" || point.Port == "" {
		return ""
	}
	return point.Switch + "/" + point.Port
}

func (r *realizer) membership(port string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ports[port])
}

func (r *realizer) changes(port string) [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.history[port])
}

func (r *realizer) pass(ctx context.Context) error {
	var claims networkingv1.NetworkInterfaceClaimList
	if err := r.api.List(ctx, &claims); err != nil {
		return err
	}
	boot, customer := map[string]bool{}, map[string][]string{}
	for i := range claims.Items {
		claim := &claims.Items[i]
		port := portOf(claim)
		if port == "" || claim.DeletionTimestamp != nil {
			continue
		}
		if isBoot(claim) {
			boot[port] = true
		} else if !slices.Contains(customer[port], claim.Spec.Network.Name) {
			customer[port] = append(customer[port], claim.Spec.Network.Name)
		}
	}
	r.mu.Lock()
	seen := map[string]bool{}
	for port := range r.ports {
		seen[port] = true
	}
	for port := range boot {
		seen[port] = true
	}
	for port := range customer {
		seen[port] = true
	}
	for port := range seen {
		var want []string
		switch {
		case boot[port]:
			want = []string{bootNetworkName}
		default:
			want = slices.Clone(customer[port])
			sort.Strings(want)
		}
		if current := r.ports[port]; !slices.Equal(current, want) {
			r.ports[port] = want
			r.history[port] = append(r.history[port], want)
		}
	}
	r.mu.Unlock()
	// Report losses before gains, so no reader sees a port on both.
	for _, gains := range []bool{false, true} {
		for i := range claims.Items {
			if err := r.report(ctx, &claims.Items[i], gains); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				return err
			}
		}
	}
	return nil
}

// report updates one claim: on the first pass only claims that left their
// port's network, on the second the rest.
func (r *realizer) report(ctx context.Context, claim *networkingv1.NetworkInterfaceClaim, gains bool) error {
	port := portOf(claim)
	if port == "" {
		return nil
	}
	on := slices.Contains(r.membership(port), claim.Spec.Network.Name)
	if claim.DeletionTimestamp != nil {
		if gains || on {
			return nil
		}
		// Detached: release the boot address, then let the claim go.
		if isBoot(claim) {
			if err := r.dropAllocation(ctx, claim.Name); err != nil {
				return err
			}
		}
		claim.Finalizers = slices.DeleteFunc(claim.Finalizers, func(f string) bool { return f == detachFinalizer })
		return r.api.Update(ctx, claim)
	}
	if !slices.Contains(claim.Finalizers, detachFinalizer) {
		claim.Finalizers = append(claim.Finalizers, detachFinalizer)
		return r.api.Update(ctx, claim)
	}
	programmed := apiMeta.IsStatusConditionTrue(claim.Status.Conditions, networkingv1.NetworkInterfaceClaimProgrammed)
	if gains != on || (programmed == on && len(claim.Status.Conditions) > 0) {
		return nil
	}
	if isBoot(claim) && len(claim.Status.Addresses) == 0 {
		r.mu.Lock()
		r.next++
		address := fmt.Sprintf("198.51.100.%d", 50+r.next)
		r.mu.Unlock()
		allocation := &unstructured.Unstructured{}
		allocation.SetGroupVersionKind(bootaddress.AllocationGVK)
		allocation.SetNamespace(ipamNamespace)
		allocation.SetName("alloc-" + strings.ReplaceAll(address, ".", "-"))
		allocation.Object["spec"] = map[string]any{"claimRef": map[string]any{"name": nso.BootAddressIPClaim(claim.Name)}, "address": address}
		if err := r.api.Create(ctx, allocation); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		claim.Status.Addresses = []networkingv1.NetworkInterfaceAddress{{Family: networkingv1.IPv4Protocol, Address: address + "/24", Gateway: "198.51.100.1", Primary: true}}
	}
	status := map[bool]metav1.ConditionStatus{true: metav1.ConditionTrue, false: metav1.ConditionFalse}
	for kind, value := range map[string]bool{networkingv1.NetworkInterfaceClaimAllocated: true, networkingv1.NetworkInterfaceClaimPrepared: true, networkingv1.NetworkInterfaceClaimProgrammed: on} {
		apiMeta.SetStatusCondition(&claim.Status.Conditions, metav1.Condition{Type: kind, Status: status[value], Reason: "TestRealizer", ObservedGeneration: claim.Generation})
	}
	return r.api.Status().Update(ctx, claim)
}

func (r *realizer) dropAllocation(ctx context.Context, claim string) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(bootaddress.AllocationGVK.GroupVersion().WithKind("IPAllocationList"))
	if err := r.api.List(ctx, list, client.InNamespace(ipamNamespace)); err != nil {
		return err
	}
	for i := range list.Items {
		if name, _, _ := unstructured.NestedString(list.Items[i].Object, "spec", "claimRef", "name"); name == nso.BootAddressIPClaim(claim) {
			if err := r.api.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// sampler reads every claim naming the shared port, as any reader of NSO
// would, and records a violation when a boot attachment and a customer
// attachment are both programmed on it.
type sampler struct {
	mu         sync.Mutex
	violations []string
	samples    int
}

func (s *sampler) run(ctx context.Context, api client.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
		var claims networkingv1.NetworkInterfaceClaimList
		if api.List(ctx, &claims) != nil {
			continue
		}
		var boot, customer []string
		for i := range claims.Items {
			claim := &claims.Items[i]
			if portOf(claim) != sharedPort || !apiMeta.IsStatusConditionTrue(claim.Status.Conditions, networkingv1.NetworkInterfaceClaimProgrammed) {
				continue
			}
			if isBoot(claim) {
				boot = append(boot, claim.Name)
			} else {
				customer = append(customer, claim.Name)
			}
		}
		s.mu.Lock()
		s.samples++
		if len(boot) > 0 && len(customer) > 0 {
			s.violations = append(s.violations, fmt.Sprintf("boot %v and customer %v", boot, customer))
		}
		s.mu.Unlock()
	}
}

// nsoEnv is the environment with NSO's CRDs, the NSO boot source, NSO's
// admission check, a cabled server, and synchronized sites.
func nsoEnv(t *testing.T) (*env, *inventory.Device) {
	t.Helper()
	e := startEnvWith(t, nsoCRDs(t)...)
	e.create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ipamNamespace}})
	e.runner.Kinds["DeviceInstallation"] = syncedSite{simwork.Installation{}}
	e.runner.Kinds["DeviceWipe"] = syncedSite{simwork.Wipe{}}
	check, err := nso.NewBootAttachmentCheck(e.admin.Scheme(), "", bootNetworkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.loop.Coordinator.Registry.RegisterBootAddressClaim(nso.BootClaimGroup, nso.BootClaimKind, check.Verify); err != nil {
		t.Fatal(err)
	}
	e.allocator.BootAddresses = nso.BootAttachments{Network: bootNetworkName, IPAM: e.admin, IPAMNamespace: ipamNamespace, Location: siteLocation}
	device := e.server(1)
	e.create(&inventory.Cable{ObjectMeta: metav1.ObjectMeta{Name: "cable-example-1", Namespace: namespace}, Spec: inventory.CableSpec{
		PhysicalMetadata: inventory.PhysicalMetadata{DisplayName: "Example"}, Medium: "Copper",
		Lanes: []inventory.CableLane{{ID: "one", A: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: device.Name}, ComponentID: "primary", Position: 1}, B: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: "switch-example-a"}, ComponentID: "ethernet7", Position: 1}}},
	}})
	return e, device
}

// nsoDown answers every NSO claim read and write as an outage.
type nsoDown struct{ client.Client }

func (c nsoDown) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if _, ok := object.(*networkingv1.NetworkInterfaceClaim); ok {
		return apierrors.NewServiceUnavailable("nso is down")
	}
	return c.Client.Get(ctx, key, object, opts...)
}

func (c nsoDown) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if _, ok := object.(*networkingv1.NetworkInterfaceClaim); ok {
		return apierrors.NewServiceUnavailable("nso is down")
	}
	return c.Client.Create(ctx, object, opts...)
}

// TestNSOBootWaitsForTheAttachment creates no installation and binds no
// address while NSO is unavailable or has not programmed the port, and
// creates it once NSO has.
func TestNSOBootWaitsForTheAttachment(t *testing.T) {
	e, _ := nsoEnv(t)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	installations := func() int {
		t.Helper()
		var list ops.DeviceInstallationList
		if err := e.admin.List(e.ctx, &list, client.InNamespace(namespace)); err != nil {
			t.Fatal(err)
		}
		return len(list.Items)
	}
	request := Installation{Installation: e.installation(1, "debian-12", "")}
	healthy := e.allocator.Provider
	e.allocator.Provider = nsoDown{healthy}
	if _, err := e.allocator.Install(e.ctx, allocation, request); err == nil || errors.Is(err, bootaddress.ErrPending) || installations() != 0 {
		t.Fatalf("install while NSO is down: %v, %d installations", err, installations())
	}
	e.allocator.Provider = healthy
	// NSO is up, but its realizer has not programmed the port.
	if _, err := e.allocator.Install(e.ctx, allocation, request); !errors.Is(err, bootaddress.ErrPending) || installations() != 0 {
		t.Fatalf("install before the port is programmed: %v, %d installations", err, installations())
	}
	r := newRealizer(e.admin)
	go r.run(e.ctx)
	var operation *ops.DeviceInstallation
	e.until("the installation is created", func() bool {
		operation, err = e.allocator.Install(e.ctx, allocation, request)
		if err != nil && !errors.Is(err, bootaddress.ErrPending) {
			t.Fatal(err)
		}
		return err == nil
	})
	if operation.Spec.BootNetwork == nil || operation.Spec.BootNetwork.AddressClaim != nso.BootReference(bootaddress.ClaimName(operation.Name)) {
		t.Fatalf("boot network = %+v", operation.Spec.BootNetwork)
	}
}

// installed creates the installation once its boot NIC is attached and
// waits until it succeeds through NSO's admission check.
func (e *env) installed(allocation *fleetv1.MachineAllocation, request Installation) *ops.DeviceInstallation {
	e.t.Helper()
	var operation *ops.DeviceInstallation
	e.until("the installation is created", func() bool {
		var err error
		operation, err = e.allocator.Install(e.ctx, allocation, request)
		if err != nil && !errors.Is(err, bootaddress.ErrPending) {
			e.t.Fatal(err)
		}
		return err == nil
	})
	e.until("the installation succeeds", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
			e.t.Fatal(err)
		}
		if operation.Status.Phase.Ended() && operation.Status.Phase != ops.PhaseSucceeded {
			e.t.Fatalf("installation ended %s %s: %s", operation.Status.Phase, operation.Status.Reason, operation.Status.Message)
		}
		return operation.Status.Phase == ops.PhaseSucceeded
	})
	return operation
}

// TestOneWriterPerPortThroughHandover runs a first install, the handover to
// the customer, a held reinstall, its handover, and the release's wipe on a
// boot NIC shared with a customer network. Fleet writes only claims. The
// port is never on the boot network and a customer network at once, each
// move is one step, the Machine is never NetworkReady while a boot
// attachment holds the port, a held operation keeps its claim, and the wipe's
// boot attachment is made only after the customer's claims are gone.
func TestOneWriterPerPortThroughHandover(t *testing.T) {
	e, device := nsoEnv(t)
	r := newRealizer(e.admin)
	go r.run(e.ctx)
	s := &sampler{}
	go s.run(e.ctx, e.admin)
	// The release controller's client records what the customer's claims and
	// the release gates were when the wipe's boot attachment claim was made.
	var wipeAttach []string
	e.allocator.Releaser = recordingCreates{Client: e.admin, onClaim: func(claim *networkingv1.NetworkInterfaceClaim) {
		var claims networkingv1.NetworkInterfaceClaimList
		_ = e.admin.List(e.ctx, &claims, client.InNamespace("default"))
		gates := e.custody(device.Name).Spec.Holder.OpenGates
		wipeAttach = append(wipeAttach, fmt.Sprintf("customer claims %d, Network gate open %v", len(claims.Items), slices.Contains(gates, ops.GateNetwork)))
	}}
	owner := machine(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	m := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: owner.Name, Generation: 1}}
	interfaces := []string{"primary"}
	// networkReady is what the Machine reports: not ready while a boot
	// attachment holds its boot NIC, otherwise ready once NSO programmed the
	// customer's claim.
	var ready []string
	networkReady := func() *metav1.Condition {
		t.Helper()
		err := e.allocator.BootNIC(e.ctx, allocation, interfaces)
		if condition := BootNICCondition(m, err); condition != nil {
			return condition
		}
		var claims networkingv1.NetworkInterfaceClaimList
		if err := e.admin.List(e.ctx, &claims, client.InNamespace("default")); err != nil {
			t.Fatal(err)
		}
		for _, claim := range claims.Items {
			if apiMeta.IsStatusConditionTrue(claim.Status.Conditions, networkingv1.NetworkInterfaceClaimProgrammed) {
				ready = append(ready, fmt.Sprintf("membership %v", r.membership(sharedPort)))
				return &metav1.Condition{Type: "NetworkReady", Status: metav1.ConditionTrue, Reason: "Programmed"}
			}
		}
		return &metav1.Condition{Type: "NetworkReady", Status: metav1.ConditionFalse, Reason: "NetworkPending"}
	}
	handedOver := func(what string) {
		t.Helper()
		e.until(what, func() bool {
			if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
				t.Fatal(err)
			}
			if _, err := e.allocator.HandOver(e.ctx, allocation); err != nil {
				t.Logf("hand over: %v", err)
			}
			return networkReady().Status == metav1.ConditionTrue
		})
	}

	// First install on the shared boot NIC; the customer's claim, made
	// while it runs as #198's attachment would, stays unprogrammed.
	first := e.installed(allocation, Installation{Installation: e.installation(1, "debian-12", "")})
	customerClaim := &networkingv1.NetworkInterfaceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "fleet-customer-primary", Labels: map[string]string{bootaddress.AllocationLabel: allocation.Name}},
		Spec: networkingv1.NetworkInterfaceClaimSpec{Network: networkingv1.LocalNetworkRef{Name: customerNetwork}, InterfaceName: "primary", AttachmentMode: nso.PhysicalMode, IPFamilies: []networkingv1.IPFamily{networkingv1.IPv4Protocol}}}
	nso.SetAttachmentPoint(customerClaim, nso.AttachmentPoint{MACAddress: "02:00:00:00:00:01", Switch: "switch-example-a", Port: "ethernet7"})
	e.create(customerClaim)
	time.Sleep(200 * time.Millisecond)
	if condition := networkReady(); condition.Status != metav1.ConditionFalse || condition.Reason != "BootNetworkAttached" {
		t.Fatalf("NetworkReady while the install's boot attachment holds the port = %+v", condition)
	}
	handedOver("the port is handed to the customer after the first install")

	// A reinstall moves the port back to the boot network for the install.
	second := e.installed(allocation, Installation{Installation: e.installation(2, "debian-12-second", "debian-12"), Current: "debian-12"})
	if condition := networkReady(); condition.Status != metav1.ConditionFalse {
		t.Fatalf("NetworkReady during the reinstall = %+v", condition)
	}
	// Held: the claim stays, and so does the port, until the hold is settled.
	state := e.custody(device.Name)
	state.Spec.RecoveryHold = &ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: "DeviceInstallation", Name: second.Name, UID: string(second.UID)}, Fence: second.Status.Fence, Since: metav1.Now(), Reason: "Synthetic"}
	if err := e.admin.Update(e.ctx, state); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := e.allocator.HandOver(e.ctx, allocation); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if condition := networkReady(); condition.Status != metav1.ConditionFalse || condition.Reason != "RecoveryHold" {
		t.Fatalf("NetworkReady while the reinstall is held = %+v", condition)
	}
	if names, err := e.allocator.installClaims().Operations(e.ctx, allocation.Name); err != nil || !slices.Contains(names, second.Name) {
		t.Fatalf("a held operation's boot attachment was released: %v, %v", names, err)
	}
	state = e.custody(device.Name)
	state.Spec.RecoveryHold = nil
	if err := e.admin.Update(e.ctx, state); err != nil {
		t.Fatal(err)
	}
	handedOver("the port is handed to the customer after the hold is settled")

	// Release: the customer's claims go first, as the Network gate requires,
	// then the wipe attaches the port to the boot network.
	if err := e.admin.Delete(e.ctx, customerClaim); err != nil {
		t.Fatal(err)
	}
	e.until("the customer's claim is gone", func() bool {
		return apierrors.IsNotFound(e.admin.Get(e.ctx, client.ObjectKeyFromObject(customerClaim), &networkingv1.NetworkInterfaceClaim{}))
	})
	e.release(allocation, owner)
	if allocation.Status.Wipe == nil {
		t.Fatal("release did not wipe")
	}
	e.until("the wipe's boot attachment is gone", func() bool { return len(r.membership(sharedPort)) == 0 })

	if len(wipeAttach) != 1 || wipeAttach[0] != "customer claims 0, Network gate open false" {
		t.Fatalf("the wipe's boot attachment was made with %v", wipeAttach)
	}
	want := [][]string{{bootNetworkName}, {customerNetwork}, {bootNetworkName}, {customerNetwork}, nil, {bootNetworkName}, nil}
	if got := r.changes(sharedPort); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("port membership went %v, want %v: each move one step", got, want)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.violations) != 0 || s.samples < 100 {
		t.Fatalf("%d of %d samples had both on the port: %v", len(s.violations), s.samples, s.violations)
	}
	for _, sample := range ready {
		if sample != "membership ["+customerNetwork+"]" {
			t.Fatalf("NetworkReady was reported with %s", sample)
		}
	}
	for _, operation := range []string{first.Name, second.Name, allocation.Status.Wipe.Name} {
		if !strings.HasPrefix(operation, allocation.Name) {
			t.Fatalf("operation %s is not the allocation's", operation)
		}
	}
	if allocation.Status.Phase != fleetv1.AllocationReleased {
		t.Fatalf("phase = %s", allocation.Status.Phase)
	}
	var left networkingv1.NetworkInterfaceClaimList
	if err := e.admin.List(e.ctx, &left); err != nil || len(left.Items) != 0 {
		t.Fatalf("claims left after release: %d, %v", len(left.Items), err)
	}
	if gone, err := bootaddress.Unallocated(e.ctx, e.admin, ipamNamespace, nso.BootAddressIPClaim(bootaddress.ClaimName(allocation.Status.Wipe.Name))); err != nil || !gone {
		t.Fatalf("the wipe's address is still allocated: %v", err)
	}
}

// recordingCreates calls onClaim before each NSO claim it creates.
type recordingCreates struct {
	client.Client
	onClaim func(*networkingv1.NetworkInterfaceClaim)
}

func (c recordingCreates) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if claim, ok := object.(*networkingv1.NetworkInterfaceClaim); ok {
		c.onClaim(claim)
	}
	return c.Client.Create(ctx, object, opts...)
}
