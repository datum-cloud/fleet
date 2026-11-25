package allocation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/simwork"
)

// ipam binds each IPAM claim as it is created, as IPAM's API does, and
// records the names created, in order.
type ipam struct {
	client.Client
	mu      sync.Mutex
	next    int
	creates []string
}

func (c *ipam) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if err := c.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	c.mu.Lock()
	c.creates = append(c.creates, object.GetName())
	c.next++
	n := c.next
	c.mu.Unlock()
	claim, ok := object.(*unstructured.Unstructured)
	if !ok || claim.GroupVersionKind() != bootaddress.GVK {
		return nil
	}
	claim.Object["status"] = map[string]any{"phase": "Bound", "address": fmt.Sprintf("198.51.100.%d", 10+n)}
	return c.Client.Status().Update(ctx, claim)
}

func (c *ipam) created() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.creates...)
}

// syncedSite is a simulated site whose DHCP Fleet synchronizes: it requires
// a boot address, then runs the simulator's worker on the rest.
type syncedSite struct{ inner site.Kind }

func (k syncedSite) strip(work *site.Work) (*site.Work, bool) {
	var inputs map[string]json.RawMessage
	if json.Unmarshal(work.Inputs, &inputs) != nil || inputs["bootNetwork"] == nil {
		return nil, false
	}
	delete(inputs, "bootNetwork")
	copied := *work
	copied.Inputs, _ = json.Marshal(inputs)
	return &copied, true
}

func (k syncedSite) Check(ctx context.Context, work *site.Work) *custody.Refusal {
	stripped, ok := k.strip(work)
	if !ok {
		return &custody.Refusal{Reason: custody.ReasonBootNetworkRequired, Message: "a synchronized site needs a boot address"}
	}
	return k.inner.Check(ctx, stripped)
}

func (k syncedSite) Run(ctx context.Context, work *site.Work) (any, error) {
	stripped, _ := k.strip(work)
	return k.inner.Run(ctx, stripped)
}

// TestBootAddressesFollowTheirOperations claims each installation's and
// wipe's boot address before the operation exists, carries it into the
// admitted inputs, and releases a claim only once its operation ended and
// is not held.
func TestBootAddressesFollowTheirOperations(t *testing.T) {
	e := startEnv(t)
	e.runner.Kinds["DeviceInstallation"] = syncedSite{simwork.Installation{}}
	e.runner.Kinds["DeviceWipe"] = syncedSite{simwork.Wipe{}}
	recorder := &ipam{Client: e.admin}
	e.allocator.Provider, e.allocator.Releaser = recorder, recorder
	e.allocator.BootAddresses = &bootaddress.Claimer{Class: "provisioning"}
	device := e.server(1)

	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	operation, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: e.installation(1, "debian-12", "")})
	if err != nil {
		t.Fatal(err)
	}
	network := operation.Spec.BootNetwork
	if network == nil || network.AddressClaim.Name != bootaddress.ClaimName(operation.Name) || network.AddressClaim.UID == "" || network.Address == "" {
		t.Fatalf("boot network = %+v", network)
	}
	creates := recorder.created()
	if len(creates) != 3 || creates[0] != allocation.Name || creates[1] != network.AddressClaim.Name || creates[2] != operation.Name {
		t.Fatalf("creates = %v, want the claim before the operation", creates)
	}
	e.until("the installation succeeds", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
			t.Fatal(err)
		}
		if operation.Status.Phase.Ended() && operation.Status.Phase != ops.PhaseSucceeded {
			t.Fatalf("installation ended %s: %s", operation.Status.Phase, operation.Status.Message)
		}
		return operation.Status.Phase == ops.PhaseSucceeded
	})
	if state := e.custody(device.Name); state.Spec.LastResult == nil || state.Spec.LastResult.Name != operation.Name {
		t.Fatalf("custody = %+v", state.Spec)
	}
	// Asking again returns the existing operation and claims nothing new.
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: e.installation(1, "debian-12", "")}); err != nil || len(recorder.created()) != 3 {
		t.Fatalf("repeat install: %v, creates %v", err, recorder.created())
	}

	claims := e.allocator.installClaims()
	held := &ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: "DeviceInstallation", Name: operation.Name}}
	// A claim for an operation not yet created stays: its requester may
	// still create the operation.
	if _, err := claims.Claim(e.ctx, allocation.Name, allocation.Name+"-install-pending"); err != nil {
		t.Fatal(err)
	}
	if none, err := e.allocator.releaseBootAddresses(e.ctx, allocation, held, false); err != nil || none {
		t.Fatalf("release under a hold: none=%v %v", none, err)
	}
	if remaining, _ := claims.Operations(e.ctx, allocation.Name); len(remaining) != 2 {
		t.Fatalf("a held or uncreated operation lost its claim: %v", remaining)
	}
	if none, err := e.allocator.releaseBootAddresses(e.ctx, allocation, nil, false); err != nil || none {
		t.Fatalf("release without a hold: none=%v %v", none, err)
	}
	if remaining, _ := claims.Operations(e.ctx, allocation.Name); len(remaining) != 1 || remaining[0] != allocation.Name+"-install-pending" {
		t.Fatalf("remaining = %v, want only the uncreated operation's claim", remaining)
	}

	e.release(allocation, machine(1))
	if allocation.Status.Wipe == nil {
		t.Fatal("release did not wipe")
	}
	wipe := &ops.DeviceWipe{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: allocation.Status.Wipe.Name}, wipe); err != nil {
		t.Fatal(err)
	}
	if wipe.Spec.BootNetwork == nil || wipe.Spec.BootInterface != "primary" || wipe.Spec.BootNetwork.Address == network.Address {
		t.Fatalf("wipe boot network = %+v on %q, want its own address", wipe.Spec.BootNetwork, wipe.Spec.BootInterface)
	}
	for _, claimer := range []*bootaddress.Claimer{claims, e.allocator.wipeClaims()} {
		if remaining, err := claimer.Operations(e.ctx, allocation.Name); err != nil || len(remaining) != 0 {
			t.Fatalf("claims left after release: %v, %v", remaining, err)
		}
	}
	if allocation.Status.Phase != fleetv1.AllocationReleased {
		t.Fatalf("phase = %s", allocation.Status.Phase)
	}
}
