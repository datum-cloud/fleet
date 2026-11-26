package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/delivery"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
	"go.datum.net/fleet/internal/site/simwork"
	"go.datum.net/fleet/internal/site/siteapi"
	"go.datum.net/fleet/internal/tenancy"
)

// The customer power flow: the Metal provider's power requests, custody,
// the simulator site and the power projector, against a real API server.

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
	allocator  *allocation.Allocator
	loop       *delivery.Loop
	store      *site.Store
	enrollment fixture.Enrollment
	class      *fleetv1.MachineClass
	profile    *fleetv1.MachineProfile
}

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, inventory.AddToScheme, fleetv1.AddToScheme, ops.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

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
	}, ErrorIfCRDPathMissing: true}
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
		Kinds: map[string]site.Kind{"DeviceInstallation": simwork.Installation{}, "DeviceWipe": simwork.Wipe{}, "DevicePowerOperation": simwork.Power{}}, Poll: 20 * time.Millisecond}
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
	sites := &delivery.Sites{Sites: map[string]delivery.Site{siteName: remote}, Devices: map[string][]string{}}
	coordinator := &custody.Coordinator{Client: admin, Registry: registry, Sites: sites, Config: custody.Config{Namespace: namespace, Principals: custody.Principals{
		custody.CreatorMetalProvider: {{Username: providerUser}}, custody.CreatorReleaseController: {{Username: releaseUser}},
	}}}
	e.loop = &delivery.Loop{Client: admin, Coordinator: coordinator, Namespace: namespace,
		Courier: &delivery.Courier{Client: admin, Registry: registry, Coordinator: coordinator, Sites: sites, Namespace: namespace}}
	e.allocator = &allocation.Allocator{
		Provider: admin, ProviderCreator: ops.Creator{Username: providerUser, UID: uid(801)},
		Releaser: admin, ReleaseCreator: ops.Creator{Username: releaseUser, UID: uid(802)},
		Custody: coordinator, Namespace: namespace,
		Cleaning:      func(string) (string, error) { return policyDigest, nil },
		BootInterface: "primary", TargetDisks: []string{"disk-0"},
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
	e.discover(name)
	e.until("custody is Verified", func() bool {
		state := e.custody(name)
		return state != nil && state.Spec.State == ops.CustodyVerified
	})
	return device
}

// discover writes fixture discovery for one Device, with its simulated
// server's power, as the simulator site does.
func (e *env) discover(name string) {
	e.t.Helper()
	if err := fixture.DiscoverPower(e.ctx, e.admin, fixture.Enrollment{namespace + "/" + name: e.enrollment[namespace+"/"+name]}, time.Now(), simwork.Observe(e.store)); err != nil {
		e.t.Fatal(err)
	}
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

func machine(n int) allocation.Machine {
	return allocation.Machine{Consumer: tenancy.Project(fmt.Sprintf("consumer-%d", n)), ConsumerUID: types.UID(uid(500 + n)), Namespace: "default", Name: "allocation", UID: types.UID(uid(600 + n))}
}

func (e *env) request(n int) allocation.Request {
	return allocation.Request{Machine: machine(n), Class: e.class, Profile: e.profile, Authority: fleetv1.AllocationAuthority{Principal: "fixture", Decision: fmt.Sprintf("fixture-%d", n)}}
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
func (e *env) release(bound *fleetv1.MachineAllocation, owner allocation.Machine) {
	e.t.Helper()
	e.until("the allocation is Released", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(bound), bound); err != nil {
			e.t.Fatal(err)
		}
		released, err := e.allocator.Release(e.ctx, bound, owner)
		if err != nil {
			e.t.Logf("release: %v", err)
		}
		return released
	})
}

const staffUser = "staff-example@example.com"

// powered is one allocated Machine on a simulated server.
type powered struct {
	e          *env
	n          int
	device     string
	deviceUID  string
	allocation *fleetv1.MachineAllocation
	machine    *fleetv1.Machine
	owner      allocation.Machine
}

// allocatePowered creates a Machine that asks for power Off, allocates it a
// Device, and projects its observed power.
func (e *env) allocatePowered(n int) *powered {
	e.t.Helper()
	e.server(n)
	object := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("machine-%d", n), Namespace: "default"}, Spec: fleetv1.MachineSpec{
		MachineClassRef: fleetv1.LocalReference{Name: e.class.Name}, MachineProfileRef: fleetv1.LocalReference{Name: e.profile.Name},
		LocationRef: fleetv1.LocalReference{Name: "location-example"}, PowerState: "Off",
	}}
	e.create(object)
	owner := machine(n)
	owner.Name, owner.UID = object.Name, object.UID
	request := e.request(n)
	request.Machine = owner
	allocation, err := e.allocator.Allocate(e.ctx, request)
	if err != nil {
		e.t.Fatal(err)
	}
	// The allocator may choose any free Device in the pool, such as one an
	// earlier subtest released, so the Device is the one the allocation
	// names.
	p := &powered{e: e, n: n, device: allocation.Spec.DeviceRef.Name, deviceUID: allocation.Labels[fleetv1.LabelDeviceUID], allocation: allocation, machine: object, owner: owner}
	// A reused Device's last operation, such as its wipe, finished after its
	// last discovery read, so its power is unknown until discovery reads it
	// again, as the simulator site does every minute.
	time.Sleep(1100 * time.Millisecond)
	e.discover(p.device)
	p.project()
	return p
}

// project runs the power projector, the only writer of Machine power, and
// reads the Machine back.
func (p *powered) project() string {
	p.e.t.Helper()
	if err := ProjectPower(p.e.ctx, p.e.admin, p.e.admin, namespace, p.owner); err != nil {
		p.e.t.Fatal(err)
	}
	if err := p.e.admin.Get(p.e.ctx, client.ObjectKeyFromObject(p.machine), p.machine); err != nil {
		p.e.t.Fatal(err)
	}
	if p.machine.Status.Power == nil {
		return ""
	}
	return p.machine.Status.Power.State
}

// want sets the Machine's powerState, which raises its generation.
func (p *powered) want(state string) {
	p.e.t.Helper()
	if err := p.e.admin.Get(p.e.ctx, client.ObjectKeyFromObject(p.machine), p.machine); err != nil {
		p.e.t.Fatal(err)
	}
	p.machine.Spec.PowerState = state
	if err := p.e.admin.Update(p.e.ctx, p.machine); err != nil {
		p.e.t.Fatal(err)
	}
}

func (p *powered) power() *ops.DevicePowerOperation {
	p.e.t.Helper()
	if err := p.e.admin.Get(p.e.ctx, client.ObjectKeyFromObject(p.machine), p.machine); err != nil {
		p.e.t.Fatal(err)
	}
	operation, err := p.e.allocator.Power(p.e.ctx, p.allocation, p.owner, p.machine)
	if err != nil {
		p.e.t.Fatal(err)
	}
	return operation
}

func (p *powered) server() simwork.Server {
	server, err := simwork.ReadServer(p.e.store, namespace+"/"+p.device)
	if err != nil {
		p.e.t.Fatal(err)
	}
	return server
}

func (p *powered) setServer(change func(*simwork.Server)) {
	p.e.t.Helper()
	server := p.server()
	change(&server)
	if err := simwork.WriteServer(p.e.store, namespace+"/"+p.device, server); err != nil {
		p.e.t.Fatal(err)
	}
}

func (e *env) ended(operation ops.Operation) ops.Phase {
	e.t.Helper()
	e.until(operation.GetName()+" ends", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
			e.t.Fatal(err)
		}
		return operation.OperationStatus().Phase.Ended()
	})
	return operation.OperationStatus().Phase
}

// recover creates a staff DeviceRecovery for the Device's current hold.
func (p *powered) recover(decision ops.RecoveryDecision, evidence ops.RecoveryEvidence) *ops.DeviceRecovery {
	p.e.t.Helper()
	hold := p.e.custody(p.device).Spec.RecoveryHold
	if hold == nil {
		p.e.t.Fatal("no hold to recover")
	}
	if evidence.Summary == "" {
		evidence = ops.RecoveryEvidence{ObservedAt: metav1.NewTime(time.Now().UTC()), Summary: "staff decided"}
	}
	recovery := &ops.DeviceRecovery{ObjectMeta: metav1.ObjectMeta{GenerateName: p.device + "-recovery-", Namespace: namespace, Labels: map[string]string{ops.LabelDeviceUID: p.deviceUID}}, Spec: ops.DeviceRecoverySpec{
		OperationEnvelope: ops.OperationEnvelope{Creator: ops.Creator{Username: staffUser, UID: uid(900)}, DeviceRef: ops.DeviceReference{Name: p.device},
			Authority: ops.Authority{ServiceRequest: &ops.ServiceRequest{Reason: "settle the customer's power"}}},
		Decision: decision, Hold: &ops.HeldOperationReference{Kind: hold.Kind, Name: hold.Name, Fence: hold.Fence}, Evidence: evidence,
	}}
	p.e.create(recovery)
	return recovery
}

// holdPower runs a power change the server ignores, which ends unknown and
// holds the Device.
func (p *powered) holdPower(state string) *ops.DevicePowerOperation {
	p.e.t.Helper()
	p.setServer(func(s *simwork.Server) { s.Unresponsive = true })
	p.want(state)
	held := p.power()
	if held == nil {
		p.e.t.Fatal("no power operation was created")
	}
	if phase := p.e.ended(held); phase != ops.PhaseOutcomeUnknown {
		p.e.t.Fatalf("held operation ended %s, want OutcomeUnknown", phase)
	}
	return held
}

func TestCustomerPower(t *testing.T) {
	e := startEnv(t)

	t.Run("power under an allocation follows the Machine's powerState", func(t *testing.T) {
		p := e.allocatePowered(11)
		// The server starts off and the Machine asks for off: nothing is due.
		if observed := p.project(); observed != "Off" {
			t.Fatalf("observed power = %q", observed)
		}
		if operation := p.power(); operation != nil {
			t.Fatalf("an operation was created with nothing to change: %s", operation.Name)
		}
		p.want("On")
		// Before the projector reports power, nothing is due: every Machine
		// defaults to Off, and a server is not forced to a state before its
		// power is known.
		unobserved := p.machine.DeepCopyObject().(*fleetv1.Machine)
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(p.machine), unobserved); err != nil {
			t.Fatal(err)
		}
		for _, state := range []*fleetv1.PowerStatus{nil, {State: "Unknown"}} {
			unobserved.Status.Power = state
			if operation, err := e.allocator.Power(e.ctx, p.allocation, p.owner, unobserved); err != nil || operation != nil {
				t.Fatalf("power %v: operation %v, %v", state, operation, err)
			}
		}
		operation := p.power()
		if operation == nil || operation.Name != custody.PowerName(p.allocation.Name, p.machine.Generation) || operation.Spec.Action != ops.PowerOn {
			t.Fatalf("operation = %+v", operation)
		}
		if phase := e.ended(operation); phase != ops.PhaseSucceeded {
			t.Fatalf("power ended %s: %s", phase, operation.Status.Message)
		}
		if simwork.State(p.server()) != ops.PowerStateOn {
			t.Fatalf("server = %+v", p.server())
		}
		// The projector reports the proven state before discovery reads it
		// again, and with it nothing more is due.
		if observed := p.project(); observed != "On" {
			t.Fatalf("observed power after the operation = %q", observed)
		}
		if again := p.power(); again != nil {
			t.Fatalf("a second operation was created: %s", again.Name)
		}
		// An installation that finishes after discovery's read proves no
		// power, so power is unknown until discovery reads it again.
		install, err := e.allocator.Install(e.ctx, p.allocation, allocation.Installation{Installation: e.installation(11, "debian-12", "")})
		if err != nil {
			t.Fatal(err)
		}
		if phase := e.ended(install); phase != ops.PhaseSucceeded {
			t.Fatalf("installation ended %s", phase)
		}
		if observed := p.project(); observed != "Unknown" {
			t.Fatalf("observed power after a later installation = %q", observed)
		}
		time.Sleep(1100 * time.Millisecond)
		e.discover(p.device)
		if observed := p.project(); observed != "On" {
			t.Fatalf("observed power after discovery read it again = %q", observed)
		}
	})

	t.Run("only the Machine that owns the allocation can request its power", func(t *testing.T) {
		p := e.allocatePowered(17)
		p.want("On")
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(p.machine), p.machine); err != nil {
			t.Fatal(err)
		}
		// Each case differs from the owner in one place, so each ownership
		// check has a case that only it refuses.
		otherMachine := func(change func(*fleetv1.Machine)) *fleetv1.Machine {
			copied := p.machine.DeepCopyObject().(*fleetv1.Machine)
			change(copied)
			return copied
		}
		otherAllocation := func(change func(*fleetv1.MachineAllocation)) *fleetv1.MachineAllocation {
			copied := p.allocation.DeepCopyObject().(*fleetv1.MachineAllocation)
			change(copied)
			return copied
		}
		for name, c := range map[string]struct {
			allocation *fleetv1.MachineAllocation
			machine    *fleetv1.Machine
		}{
			"a same-name Machine created again":               {p.allocation, otherMachine(func(m *fleetv1.Machine) { m.UID = types.UID(uid(699)) })},
			"a Machine in another namespace":                  {p.allocation, otherMachine(func(m *fleetv1.Machine) { m.Namespace = "other" })},
			"a Machine with another name":                     {p.allocation, otherMachine(func(m *fleetv1.Machine) { m.Name = "machine-other" })},
			"an allocation named for another Machine":         {otherAllocation(func(a *fleetv1.MachineAllocation) { a.Name = "alloc-0000000000000000" }), p.machine},
			"an allocation for another consumer":              {otherAllocation(func(a *fleetv1.MachineAllocation) { a.Spec.ConsumerRef = tenancy.Project("consumer-other") }), p.machine},
			"an allocation recording another Machine":         {otherAllocation(func(a *fleetv1.MachineAllocation) { a.Spec.MachineRef.Name = "machine-other" }), p.machine},
			"an allocation labeled with another Machine UID":  {otherAllocation(func(a *fleetv1.MachineAllocation) { a.Labels[fleetv1.LabelMachineUID] = uid(698) }), p.machine},
			"an allocation labeled with another consumer UID": {otherAllocation(func(a *fleetv1.MachineAllocation) { a.Labels[fleetv1.LabelConsumerUID] = uid(598) }), p.machine},
		} {
			if operation, err := e.allocator.Power(e.ctx, c.allocation, p.owner, c.machine); !errors.Is(err, allocation.ErrConflict) || operation != nil {
				t.Errorf("%s: operation %v, %v", name, operation, err)
			}
		}
		var list ops.DevicePowerOperationList
		if err := e.admin.List(e.ctx, &list, client.InNamespace(namespace)); err != nil {
			t.Fatal(err)
		}
		for _, operation := range list.Items {
			if reference := operation.Spec.Authority.AllocationRef; reference != nil && reference.Name == p.allocation.Name {
				t.Fatalf("a refused request created %s", operation.Name)
			}
		}
		if operation := p.power(); operation == nil {
			t.Fatal("the owning Machine's request created no operation")
		}
	})

	t.Run("a power change waits behind a running installation", func(t *testing.T) {
		p := e.allocatePowered(12)
		install, err := e.allocator.Install(e.ctx, p.allocation, allocation.Installation{Installation: e.installation(12, "debian-12", "")})
		if err != nil {
			t.Fatal(err)
		}
		p.want("On")
		operation := p.power()
		if phase := e.ended(operation); phase != ops.PhaseSucceeded {
			t.Fatalf("power ended %s: %s", phase, operation.Status.Message)
		}
		if phase := e.ended(install); phase != ops.PhaseSucceeded {
			t.Fatalf("installation ended %s", phase)
		}
		// Custody admitted the installation first, and power only after it
		// finished.
		if operation.Status.Fence <= install.Status.Fence || operation.Status.AdmittedAt.Before(install.Status.FinishedAt) {
			t.Fatalf("power admitted at fence %d %v, installation fence %d finished %v", operation.Status.Fence, operation.Status.AdmittedAt, install.Status.Fence, install.Status.FinishedAt)
		}
	})

	t.Run("a newer power request replaces a pending one", func(t *testing.T) {
		p := e.allocatePowered(13)
		install, err := e.allocator.Install(e.ctx, p.allocation, allocation.Installation{Installation: e.installation(13, "debian-12", "")})
		if err != nil {
			t.Fatal(err)
		}
		p.want("On")
		older := p.power()
		// Newer by at least the API server's one-second timestamp.
		time.Sleep(1100 * time.Millisecond)
		p.setServer(func(s *simwork.Server) { s.Power = "On" })
		e.discover(p.device)
		p.project()
		p.want("Off")
		newer := p.power()
		if newer == nil || newer.Name == older.Name || newer.Spec.Action != ops.PowerForceOff {
			t.Fatalf("newer = %+v", newer)
		}
		if phase := e.ended(older); phase != ops.PhaseCancelled || older.Status.Reason != custody.ReasonSuperseded {
			t.Fatalf("older ended %s %s", phase, older.Status.Reason)
		}
		if phase := e.ended(newer); phase != ops.PhaseSucceeded {
			t.Fatalf("newer ended %s: %s", phase, newer.Status.Message)
		}
		if phase := e.ended(install); phase != ops.PhaseSucceeded {
			t.Fatalf("installation ended %s", phase)
		}
	})

	t.Run("an intent that returns to the observed state replaces the request that has not run", func(t *testing.T) {
		p := e.allocatePowered(16)
		install, err := e.allocator.Install(e.ctx, p.allocation, allocation.Installation{Installation: e.installation(16, "debian-12", "")})
		if err != nil {
			t.Fatal(err)
		}
		p.want("On")
		queued := p.power()
		time.Sleep(1100 * time.Millisecond)
		// The customer changes their mind before the queued On runs. The
		// server is still observed off, and the queued On would undo the
		// intent, so a ForceOff replaces it.
		p.want("Off")
		reverted := p.power()
		if reverted == nil || reverted.Spec.Action != ops.PowerForceOff {
			t.Fatalf("reverted = %+v", reverted)
		}
		if phase := e.ended(queued); phase != ops.PhaseCancelled || queued.Status.Reason != custody.ReasonSuperseded {
			t.Fatalf("queued ended %s %s", phase, queued.Status.Reason)
		}
		if phase := e.ended(reverted); phase != ops.PhaseSucceeded {
			t.Fatalf("reverted ended %s: %s", phase, reverted.Status.Message)
		}
		if phase := e.ended(install); phase != ops.PhaseSucceeded {
			t.Fatalf("installation ended %s", phase)
		}
		if simwork.State(p.server()) != ops.PowerStateOff {
			t.Fatalf("server = %+v", p.server())
		}
		// With the intent met and nothing pending, nothing more is due.
		if again := p.power(); again != nil && !again.Status.Phase.Ended() {
			t.Fatalf("another operation is due: %s", again.Name)
		}
	})

	t.Run("a power hold reports Unknown, moves into release, and settles with fresh discovery", func(t *testing.T) {
		p := e.allocatePowered(14)
		held := p.holdPower("On")
		hold := e.custody(p.device).Spec.RecoveryHold
		if hold == nil || hold.Name != held.Name || e.custody(p.device).Spec.Holder.Type != ops.HolderAllocation {
			t.Fatalf("custody = %+v", e.custody(p.device).Spec)
		}
		if observed := p.project(); observed != "Unknown" {
			t.Fatalf("observed power while held = %q", observed)
		}
		// The customer's power is unknown, so nothing more is created.
		p.want("Off")
		if operation := p.power(); operation != nil {
			t.Fatalf("an operation was created during the hold: %s", operation.Name)
		}
		// Release starts, the hold moves with it, and the wipe waits.
		for range 5 {
			released, err := e.allocator.Release(e.ctx, p.allocation, p.owner)
			if err != nil && !errors.Is(err, allocation.ErrReleased) {
				t.Logf("release: %v", err)
			}
			if released {
				t.Fatal("the allocation released while its Device was held")
			}
			if err := e.loop.Pass(e.ctx); err != nil {
				t.Logf("pass: %v", err)
			}
		}
		state := e.custody(p.device)
		if state.Spec.Holder.Type != ops.HolderRelease || state.Spec.RecoveryHold == nil || state.Spec.RecoveryHold.Name != held.Name {
			t.Fatalf("custody during release = %+v", state.Spec)
		}
		// Staff settle the hold with discovery read after it began.
		time.Sleep(1100 * time.Millisecond)
		e.discover(p.device)
		// Staff cite the discovery read: its time and the state it read.
		read := &inventory.Device{}
		if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: p.device}, read); err != nil {
			t.Fatal(err)
		}
		settle := p.recover(ops.DecisionSettle, ops.RecoveryEvidence{ObservedAt: read.Status.Discovery.Inventory.ObservedAt, Summary: "discovery reads the server off",
			Data: map[string]string{custody.SettlePowerState: "Off"}})
		if phase := e.ended(settle); phase != ops.PhaseSucceeded {
			t.Fatalf("settle ended %s %s: %s", phase, settle.Status.Reason, settle.Status.Message)
		}
		p.setServer(func(s *simwork.Server) { s.Unresponsive = false })
		e.release(p.allocation, p.owner)
	})

	t.Run("after a Retry the provider repeats the held action, and only that action", func(t *testing.T) {
		p := e.allocatePowered(15)
		held := p.holdPower("On")
		// Until staff apply a Retry, nothing is created, even though
		// powerState still asks for the held action.
		if operation := p.power(); operation != nil {
			t.Fatalf("a retry was created before staff applied a Retry: %s", operation.Name)
		}
		time.Sleep(1100 * time.Millisecond)
		e.discover(p.device)
		retry := p.recover(ops.DecisionRetry, ops.RecoveryEvidence{})
		if phase := e.ended(retry); phase != ops.PhaseSucceeded {
			t.Fatalf("retry ended %s %s: %s", phase, retry.Status.Reason, retry.Status.Message)
		}
		// A changed powerState no longer maps to the held action: no retry.
		p.want("Off")
		if operation := p.power(); operation != nil {
			t.Fatalf("a retry was created for another action: %s", operation.Name)
		}
		p.want("On")
		p.setServer(func(s *simwork.Server) { s.Unresponsive = false })
		repeated := p.power()
		hold := e.custody(p.device).Spec.RecoveryHold
		if repeated == nil || repeated.Name != custody.RetryName(*hold, hold.Retries) || !reflect.DeepEqual(repeated.Spec, held.Spec) {
			t.Fatalf("retry operation = %+v", repeated)
		}
		if phase := e.ended(repeated); phase != ops.PhaseSucceeded {
			t.Fatalf("retried power ended %s: %s", phase, repeated.Status.Message)
		}
		if state := e.custody(p.device); state.Spec.RecoveryHold != nil {
			t.Fatalf("hold after the retry succeeded = %+v", state.Spec.RecoveryHold)
		}
		if observed := p.project(); observed != "On" {
			t.Fatalf("observed power after the retry = %q", observed)
		}
	})
}
