package allocation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/network"
	"go.datum.net/fleet/internal/network/nso"
)

const physical networkingv1.NetworkInterfaceAttachmentMode = "Physical"

// nsoCRDs loads NSO's Network, NetworkInterfaceClaim and NetworkInterface
// CRDs from the pinned module. With proposed, it adds the Physical
// attachment mode Fleet asks NSO for, as a stand-in for that NSO change.
func nsoCRDs(t *testing.T, proposed bool) []*apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "go.datum.net/network-services-operator").Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "config", "crd", "bases")
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
		if proposed {
			for _, version := range crd.Spec.Versions {
				if spec, ok := version.Schema.OpenAPIV3Schema.Properties["spec"]; ok {
					if mode, ok := spec.Properties["attachmentMode"]; ok {
						mode.Enum = append(mode.Enum, apiextensionsv1.JSON{Raw: []byte(`"Physical"`)})
						spec.Properties["attachmentMode"] = mode
						version.Schema.OpenAPIV3Schema.Properties["spec"] = spec
					}
				}
			}
		}
		crds = append(crds, crd)
	}
	return crds
}

// TestNSOHasNoPhysicalAttachmentMode records the gap: the pinned NSO admits
// only container and hypervisor attachment modes, so a physical claim is
// refused until NSO adds one.
func TestNSOHasNoPhysicalAttachmentMode(t *testing.T) {
	for _, crd := range nsoCRDs(t, false) {
		if crd.Spec.Names.Kind != "NetworkInterfaceClaim" {
			continue
		}
		modes := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["attachmentMode"].Enum
		var names []string
		for _, mode := range modes {
			names = append(names, strings.Trim(string(mode.Raw), `"`))
		}
		if slices.Contains(names, string(physical)) || len(names) == 0 {
			t.Fatalf("NSO attachment modes = %v; update the server networking enhancement", names)
		}
		return
	}
	t.Fatal("no NetworkInterfaceClaim CRD")
}

type allow struct{ principal string }

func (a allow) AuthorizeNetworkUse(_ context.Context, principal string, _ fleetv1.NamespacedReference) error {
	if principal != a.principal {
		return errors.New("denied")
	}
	return nil
}

// TestMachineInterfacesAttachThroughNSO checks a Machine's interfaces
// before allocation, names each interface's attachment point from
// inventory, creates one NSO claim per interface, reports what NSO and a
// test realizer report, recovers a claim whose UID was not yet recorded, and
// closes the Network and Address gates only once every claim is gone.
func TestMachineInterfacesAttachThroughNSO(t *testing.T) {
	e := startEnvWith(t, nsoCRDs(t, true)...)
	adapter := nso.Adapter{Client: e.admin, Mode: physical, Authorizer: allow{principal: "fixture"}}
	e.allocator.Network = adapter
	device := e.server(1)
	e.create(&inventory.Cable{ObjectMeta: metav1.ObjectMeta{Name: "cable-example-1", Namespace: namespace}, Spec: inventory.CableSpec{
		PhysicalMetadata: inventory.PhysicalMetadata{DisplayName: "Example"}, Medium: "Copper",
		Lanes: []inventory.CableLane{{ID: "one", A: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: device.Name}, ComponentID: "primary", Position: 1}, B: inventory.CableEndpoint{DeviceRef: &inventory.LocalReference{Name: "switch-example-a"}, ComponentID: "ethernet7", Position: 1}}},
	}})
	makeNetwork := func(name string, ready bool) {
		t.Helper()
		current := &networkingv1.Network{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: networkingv1.NetworkSpec{IPAM: networkingv1.NetworkIPAM{Mode: networkingv1.NetworkIPAMModeAuto}, IPFamilies: []networkingv1.IPFamily{networkingv1.IPv4Protocol}}}
		e.create(current)
		if ready {
			apiMeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: networkingv1.NetworkReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: current.Generation})
			if err := e.admin.Status().Update(e.ctx, current); err != nil {
				t.Fatal(err)
			}
		}
	}
	makeNetwork("customer-a", true)
	makeNetwork("customer-unready", false)
	machine := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "attached"}, Spec: fleetv1.MachineSpec{
		MachineClassRef: fleetv1.LocalReference{Name: "class-example"}, MachineProfileRef: fleetv1.LocalReference{Name: "profile-example"}, LocationRef: fleetv1.LocalReference{Name: "local"}, PowerState: "Off",
		NetworkInterfaces: []fleetv1.NetworkInterfaceRequest{{Name: "primary", Network: fleetv1.NamespacedReference{Name: "customer-a"}, IPFamilies: []string{"IPv4"}}},
	}}
	e.create(machine)

	// Checks run before capacity is reserved.
	unready := *machine
	unready.Spec.NetworkInterfaces = []fleetv1.NetworkInterfaceRequest{{Name: "primary", Network: fleetv1.NamespacedReference{Name: "customer-unready"}}}
	unsupported := *e.allocator
	unsupported.Network = nil
	noMode := *e.allocator
	noMode.Network = nso.Adapter{Client: e.admin, Authorizer: allow{principal: "fixture"}}
	for name, check := range map[string]struct {
		allocator *Allocator
		machine   *fleetv1.Machine
		principal string
		want      error
	}{
		"another principal": {e.allocator, machine, "intruder", network.ErrUnauthorized},
		"unready network":   {e.allocator, &unready, "fixture", network.ErrNotReady},
		"no network owner":  {&unsupported, machine, "fixture", network.ErrUnsupported},
		"no physical mode":  {&noMode, machine, "fixture", network.ErrUnsupported},
	} {
		if err := check.allocator.CheckNetwork(e.ctx, check.machine, check.principal); !errors.Is(err, check.want) {
			t.Errorf("%s: %v, want %v", name, err, check.want)
		}
	}
	if err := e.allocator.CheckNetwork(e.ctx, machine, "fixture"); err != nil {
		t.Fatal(err)
	}

	owner := Machine{Project: "consumer-1", ProjectUID: "00000000-0000-4000-8000-000000000501", Namespace: machine.Namespace, Name: machine.Name, UID: machine.UID}
	request := e.request(1)
	request.Machine = owner
	allocation, err := e.allocator.Allocate(e.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	status, ready, err := e.allocator.Attach(e.ctx, allocation, machine)
	if err != nil || ready || len(status.Interfaces) != 1 {
		t.Fatalf("attach before NSO reports: %+v ready=%v %v", status, ready, err)
	}
	recorded := allocation.Status.Network.Interfaces[0]
	want := fleetv1.AttachmentPoint{MACAddress: "02:00:00:00:00:01", Switch: "switch-example-a", Port: "ethernet7"}
	if recorded.Attachment != want || recorded.Claim.Name != nso.ClaimName(allocation.Name, "primary") || recorded.Claim.UID == "" {
		t.Fatalf("recorded = %+v", recorded)
	}
	claim := &networkingv1.NetworkInterfaceClaim{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: "default", Name: recorded.Claim.Name}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.AttachmentMode != physical || claim.Spec.Network.Name != "customer-a" || claim.Labels[nso.MachineUIDLabel] != string(machine.UID) || string(claim.UID) != recorded.Claim.UID {
		t.Fatalf("claim = %+v", claim)
	}

	// A crash after the create, before the UID was recorded, finds the
	// same claim by name.
	allocation.Status.Network.Interfaces[0].Claim.UID = ""
	if err := e.admin.Status().Update(e.ctx, allocation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.allocator.Attach(e.ctx, allocation, machine); err != nil || allocation.Status.Network.Interfaces[0].Claim.UID != recorded.Claim.UID {
		t.Fatalf("recovery: %v, %+v", err, allocation.Status.Network.Interfaces[0])
	}

	// NSO allocates and binds, and the test realizer programs the port.
	iface := &networkingv1.NetworkInterface{ObjectMeta: metav1.ObjectMeta{Name: "iface-example", Namespace: "default"}, Spec: networkingv1.NetworkInterfaceSpec{
		Network: networkingv1.LocalNetworkRef{Name: "customer-a"}, ClaimRef: &networkingv1.NetworkInterfaceClaimRef{Name: claim.Name}, InterfaceName: "primary", AttachmentMode: physical, MTU: 8800,
	}}
	e.create(iface)
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
		t.Fatal(err)
	}
	claim.Status.NetworkInterfaceRef = &networkingv1.LocalNetworkInterfaceRef{Name: iface.Name}
	claim.Status.Addresses = []networkingv1.NetworkInterfaceAddress{{Family: networkingv1.IPv4Protocol, Address: "198.51.100.20/24", Gateway: "198.51.100.1", Primary: true}}
	for _, kind := range []string{networkingv1.NetworkInterfaceClaimAllocated, networkingv1.NetworkInterfaceClaimPrepared, networkingv1.NetworkInterfaceClaimProgrammed} {
		apiMeta.SetStatusCondition(&claim.Status.Conditions, metav1.Condition{Type: kind, Status: metav1.ConditionTrue, Reason: "Test", ObservedGeneration: claim.Generation})
	}
	if err := e.admin.Status().Update(e.ctx, claim); err != nil {
		t.Fatal(err)
	}
	status, ready, err = e.allocator.Attach(e.ctx, allocation, machine)
	if err != nil || !ready {
		t.Fatalf("attach after programming: ready=%v %v", ready, err)
	}
	got := status.Interfaces[0]
	if got.MACAddress != want.MACAddress || got.MTU != 8800 || len(got.Addresses) != 1 || got.Addresses[0].Address != "198.51.100.20/24" || got.Network.Name != "customer-a" {
		t.Fatalf("status = %+v", got)
	}
	if condition := NetworkCondition(machine, ready, err); condition.Status != metav1.ConditionTrue {
		t.Fatalf("NetworkReady = %+v", condition)
	}

	// NSO holds the claim until the realizer detaches the port: the
	// Network and Address gates stay open while it exists.
	claim.Finalizers = append(claim.Finalizers, "networking.datumapis.com/detach")
	if err := e.admin.Update(e.ctx, claim); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
			t.Fatal(err)
		}
		if _, err := e.allocator.Release(e.ctx, allocation, owner); err != nil {
			t.Fatal(err)
		}
		_ = e.loop.Pass(e.ctx)
	}
	gates := e.custody(device.Name).Spec.Holder.OpenGates
	if !slices.Contains(gates, ops.GateNetwork) || !slices.Contains(gates, ops.GateAddress) {
		t.Fatalf("gates = %v while the claim exists", gates)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(claim), claim); err != nil || claim.DeletionTimestamp == nil {
		t.Fatalf("release did not delete the claim: %v", err)
	}
	claim.Finalizers = nil
	if err := e.admin.Update(e.ctx, claim); err != nil {
		t.Fatal(err)
	}
	e.release(allocation, owner)
}
