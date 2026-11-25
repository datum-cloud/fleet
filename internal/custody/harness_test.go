package custody

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

func jsonMarshal(value any) ([]byte, error)    { return json.Marshal(value) }
func jsonUnmarshal(data []byte, out any) error { return json.Unmarshal(data, out) }

const (
	providerUser = "system:serviceaccount:fleet-system:metal-provider"
	releaseUser  = "system:serviceaccount:fleet-system:release-controller"
	staffUser    = "staff-example@example.com"
	testSite     = "site-example-a"
	testKeyA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	imageDigest  = "sha256:9f4ad8d8b8a6000000000000000000000000000000000000000000000000beef"
	policyDigest = "sha256:0c2b77e1d9a4000000000000000000000000000000000000000000000000cafe"
)

func testPrincipals() Principals {
	return Principals{CreatorMetalProvider: {{Username: providerUser}}, CreatorReleaseController: {{Username: releaseUser}}}
}

// clock is an injectable, advanceable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeSites is the site-fence source for Increment A.
type fakeSites struct {
	mu      sync.Mutex
	serving map[string][]string
	fences  map[string]int64
	// seen, when set for a Device, is reported as the site's seen fence in
	// place of its stopped fence, as a site whose older worker has not yet
	// stopped would report.
	seen map[string]int64
}

func newFakeSites() *fakeSites {
	return &fakeSites{serving: map[string][]string{}, fences: map[string]int64{}, seen: map[string]int64{}}
}

func (s *fakeSites) Serving(device *inventory.Device) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sites, ok := s.serving[device.Name]; ok {
		return append([]string(nil), sites...)
	}
	return []string{testSite}
}

// Fence reports the stored fence as both seen and stopped, unless a seen
// fence was set for the Device.
func (s *fakeSites) Fence(_ context.Context, site string, device *inventory.Device) (SiteFence, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fence, ok := s.fences[site+"/"+device.Name]
	seen := fence
	if value, set := s.seen[device.Name]; set {
		seen = value
	}
	return SiteFence{Seen: seen, Stopped: fence}, ok, nil
}

// reportSeen makes the site report a seen fence above its stopped fence.
func (s *fakeSites) reportSeen(device string, seen int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[device] = seen
}

// report stores the fence a site reports for a Device.
func (s *fakeSites) report(device string, fence int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fences[testSite+"/"+device] = fence
}

func (s *fakeSites) forget(device string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fences, testSite+"/"+device)
}

type env struct {
	t      *testing.T
	config *rest.Config
	scheme *runtime.Scheme
	admin  client.Client
	clock  *clock
	sites  *fakeSites
	coord  *Coordinator
	ctx    context.Context
}

func newScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, inventory.AddToScheme, metal.AddToScheme, ops.AddToScheme, addTestKinds, admissionregistrationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// startEnv starts a real API server with the metal, inventory, custody and
// test-only CRDs.
func startEnv(t *testing.T) *env {
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
		filepath.Join("testdata", "crds"),
	}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := newScheme(t)
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, config: config, scheme: scheme, admin: admin, clock: &clock{now: time.Now().UTC().Truncate(time.Second)}, sites: newFakeSites(), ctx: context.Background()}
	e.coord = e.coordinator(admin)
	return e
}

// sub returns the environment for a subtest, with its own clock and
// coordinator, so helpers fail the subtest and clock moves stay in it.
func (e *env) sub(t *testing.T) *env {
	copied := *e
	copied.t = t
	copied.clock = &clock{now: time.Now().UTC().Truncate(time.Second)}
	copied.coord = copied.coordinator(copied.admin)
	return &copied
}

func (e *env) coordinator(api client.Client) *Coordinator {
	return &Coordinator{Client: api, Registry: testRegistry(e.t), Sites: e.sites, Config: Config{Principals: testPrincipals()}, Now: e.clock.Now}
}

// device creates a discovered Device ready for operations.
func (e *env) device(name, uuid, key string) *inventory.Device {
	e.t.Helper()
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: inventory.DeviceSpec{
		ClaimRef:   inventory.LocalReference{Name: "claim-" + name},
		Identity:   inventory.DeviceIdentity{Model: "example", Serial: "serial-" + name, SystemUUID: uuid},
		BMC:        &inventory.BMC{Endpoint: "https://[2001:db8::10]"},
		Interfaces: []inventory.Interface{{Name: "primary", MACAddress: macFor(name)}},
	}}
	if err := e.admin.Create(e.ctx, device); err != nil {
		e.t.Fatal(err)
	}
	e.discover(device, key, e.clock.Now())
	return device
}

var macs sync.Map

func macFor(name string) string {
	value, _ := macs.LoadOrStore(name, fmt.Sprintf("02:00:00:00:%02x:%02x", len(name), int(name[len(name)-1])))
	return value.(string)
}

// discover records a successful discovery, as BMC discovery would.
func (e *env) discover(device *inventory.Device, key string, at time.Time) {
	e.t.Helper()
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(device), device); err != nil {
		e.t.Fatal(err)
	}
	observed := metav1.NewTime(at)
	device.Status.Discovery = &inventory.DeviceDiscovery{
		TLS:       &inventory.ObservedTLS{ObservedAt: observed, Endpoint: device.Spec.BMC.Endpoint, PublicKeySHA256: key},
		Inventory: &inventory.ObservedInventory{ObservedAt: observed, Endpoint: device.Spec.BMC.Endpoint},
	}
	device.Status.Conditions = []metav1.Condition{{Type: inventory.ConditionIdentityMatched, Status: metav1.ConditionTrue, Reason: "Matched", ObservedGeneration: device.Generation, LastTransitionTime: observed}}
	if err := e.admin.Status().Update(e.ctx, device); err != nil {
		e.t.Fatal(err)
	}
}

// settle reconciles until nothing changes.
func (e *env) settle(name string) Outcome {
	e.t.Helper()
	return settleWith(e.t, e.ctx, e.coord, name)
}

func settleWith(t *testing.T, ctx context.Context, coord *Coordinator, name string) Outcome {
	t.Helper()
	for range 60 {
		outcome, err := coord.Reconcile(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if !outcome.Changed {
			return outcome
		}
	}
	t.Fatalf("reconcile of %s did not settle", name)
	return Outcome{}
}

func (e *env) custody(name string) *ops.DeviceCustody {
	e.t.Helper()
	custody := &ops.DeviceCustody{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: "default", Name: name}, custody); err != nil {
		e.t.Fatal(err)
	}
	return custody
}

// verified creates a Device and brings its custody to Verified, holder None.
func (e *env) verified(name, uuid, key string) *inventory.Device {
	e.t.Helper()
	device := e.device(name, uuid, key)
	e.settle(name)
	e.sites.report(name, 0)
	e.settle(name)
	if custody := e.custody(name); custody.Spec.State != ops.CustodyVerified || custody.Spec.Holder.Type != ops.HolderNone {
		e.t.Fatalf("custody did not verify: %+v", custody.Spec)
	}
	return device
}

func envelope(device *inventory.Device, user string, authority ops.Authority) ops.OperationEnvelope {
	return ops.OperationEnvelope{Creator: ops.Creator{Username: user, UID: user + "-uid"}, DeviceRef: ops.DeviceReference{Name: device.Name, UID: string(device.UID)}, Authority: authority}
}

func service(reason string) ops.Authority {
	return ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: reason}}
}

func allocated(name string) ops.Authority {
	return ops.Authority{AllocationRef: &ops.LocalReference{Name: name}}
}

func (e *env) create(object client.Object) {
	e.t.Helper()
	if err := e.admin.Create(e.ctx, object); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) staffOp(device *inventory.Device, input string) *DeviceTestStaffOperation {
	e.t.Helper()
	operation := &DeviceTestStaffOperation{ObjectMeta: metav1.ObjectMeta{GenerateName: device.Name + "-staff-", Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, staffUser, service("qualification")), Input: input}}
	e.create(operation)
	return operation
}

func (e *env) providerOp(device *inventory.Device, allocation, input string) *DeviceTestProviderOperation {
	e.t.Helper()
	operation := &DeviceTestProviderOperation{ObjectMeta: metav1.ObjectMeta{Name: allocation + "-test-" + input, Namespace: "default"}, Spec: testSpec{OperationEnvelope: envelope(device, providerUser, allocated(allocation)), Input: input}}
	e.create(operation)
	return operation
}

func (e *env) installation(device *inventory.Device, allocation, request string) *ops.DeviceInstallation {
	e.t.Helper()
	operation := &ops.DeviceInstallation{ObjectMeta: metav1.ObjectMeta{Name: InstallationName(allocation, request), Namespace: "default"}, Spec: ops.DeviceInstallationSpec{
		OperationEnvelope: envelope(device, providerUser, allocated(allocation)), RequestUID: request,
		Image: ops.ImageInput{Name: "debian-12", Digest: imageDigest}, BootInterface: "primary", TargetDisks: []string{"disk-0"},
	}}
	e.create(operation)
	return operation
}

func (e *env) wipe(device *inventory.Device, allocation string, step int32) *ops.DeviceWipe {
	e.t.Helper()
	operation := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: WipeName(allocation, step), Namespace: "default"}, Spec: ops.DeviceWipeSpec{
		OperationEnvelope: envelope(device, releaseUser, allocated(allocation)), Step: step, CleaningPolicyDigest: policyDigest, TargetDisks: []string{"disk-0"},
	}}
	e.create(operation)
	return operation
}

// namedWipe creates a wipe with an explicit name, such as a retry's.
func (e *env) namedWipe(device *inventory.Device, allocation, name string, step int32) *ops.DeviceWipe {
	e.t.Helper()
	operation := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: ops.DeviceWipeSpec{
		OperationEnvelope: envelope(device, releaseUser, allocated(allocation)), Step: step, CleaningPolicyDigest: policyDigest, TargetDisks: []string{"disk-0"},
	}}
	e.create(operation)
	return operation
}

func (e *env) recovery(device *inventory.Device, spec ops.DeviceRecoverySpec) *ops.DeviceRecovery {
	e.t.Helper()
	spec.OperationEnvelope = envelope(device, staffUser, service("recovery"))
	if spec.Evidence.Summary == "" {
		spec.Evidence = ops.RecoveryEvidence{ObservedAt: metav1.NewTime(e.clock.Now()), Summary: "observed by staff"}
	}
	operation := &ops.DeviceRecovery{ObjectMeta: metav1.ObjectMeta{GenerateName: device.Name + "-recovery-", Namespace: "default"}, Spec: spec}
	e.create(operation)
	return operation
}

// status reads an operation's status.
func (e *env) status(operation ops.Operation) ops.OperationStatus {
	e.t.Helper()
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
		e.t.Fatal(err)
	}
	return *operation.OperationStatus()
}

func (e *env) requirePhase(operation ops.Operation, phase ops.Phase, reason string) ops.OperationStatus {
	e.t.Helper()
	status := e.status(operation)
	if status.Phase != phase || (reason != "" && status.Reason != reason) {
		e.t.Fatalf("%s: phase %s reason %s (%s), want %s %s", operation.GetName(), status.Phase, status.Reason, status.Message, phase, reason)
	}
	return status
}

func reference(kind string, operation ops.Operation) ops.OperationReference {
	return ops.OperationReference{Kind: kind, Name: operation.GetName(), UID: string(operation.GetUID())}
}

// result reports a site result for the active operation.
func (e *env) result(device string, phase ops.Phase, evidence string) error {
	e.t.Helper()
	active := e.custody(device).Spec.ActiveOperation
	if active == nil {
		e.t.Fatalf("%s has no active operation", device)
	}
	var raw []byte
	if evidence != "" {
		raw = []byte(evidence)
	}
	return e.coord.AcceptResult(e.ctx, SiteResult{Site: testSite, Device: device, Operation: active.OperationReference, Fence: active.Fence, Phase: phase, Evidence: raw})
}

// allocation creates a MachineCapacity for the Device and a MachineAllocation
// naming it, as the allocator will in Increment C.
func (e *env) allocation(device *inventory.Device, name, phase string) *metal.MachineAllocation {
	e.t.Helper()
	capacityName := "capacity-" + name
	capacity := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: capacityName, Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	if err := e.admin.Create(e.ctx, capacity); err != nil && !apierrors.IsAlreadyExists(err) {
		e.t.Fatal(err)
	}
	allocation := &metal.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: metal.MachineAllocationSpec{
		MachineRef:         metal.AllocatedMachineReference{Project: "consumer-example", ProjectUID: uid(900), Namespace: "default", Name: "machine-" + name, UID: uid(901)},
		MachineCapacityRef: metal.NamespacedReference{Namespace: "default", Name: capacityName},
		DeviceRef:          metal.AllocatedDeviceReference{Name: device.Name, UID: string(device.UID)},
		Accepted: metal.AcceptedContract{MachineClassRef: metal.UIDReference{Name: "class-example", UID: uid(902)}, MachineProfileRef: metal.UIDReference{Name: "profile-example", UID: uid(903)},
			CPU: metal.CPURequirement{Architecture: "amd64", Cores: 2}, Memory: metal.MemoryRequirement{Capacity: "2Gi"}},
		Authority:            metal.AllocationAuthority{Principal: "fixture", Decision: "fixture-" + name},
		CleaningPolicyDigest: policyDigest,
		BootInterface:        "primary",
		TargetDisks:          []string{"disk-0"},
	}}
	e.create(allocation)
	if phase != "" {
		allocation.Status.Phase = phase
		if err := e.admin.Status().Update(e.ctx, allocation); err != nil {
			e.t.Fatal(err)
		}
	}
	return allocation
}

func uid(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
