package allocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// writer is one requester's client: it may delete only the claims of its
// own operation kind.
type writer struct {
	client.Client
	kind string
}

func (w writer) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	if claim, ok := object.(*unstructured.Unstructured); ok && claim.GroupVersionKind() == bootaddress.GVK && claim.GetLabels()[bootaddress.KindLabel] != w.kind {
		return fmt.Errorf("the %s requester deleted a %s claim", w.kind, claim.GetLabels()[bootaddress.KindLabel])
	}
	return w.Client.Delete(ctx, object, options...)
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
	e.allocator.Provider, e.allocator.Releaser = writer{recorder, "DeviceInstallation"}, writer{recorder, "DeviceWipe"}
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
	if network == nil || network.AddressClaim.Name != bootaddress.ClaimName(operation.Name) || network.Address == "" {
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
	// A claim for an operation not yet created stays: its requester may
	// still create the operation.
	if _, err := claims.Claim(e.ctx, allocation.Name, allocation.Name+"-install-pending"); err != nil {
		t.Fatal(err)
	}
	hold := func(held *ops.RecoveryHold) {
		t.Helper()
		state := e.custody(device.Name)
		state.Spec.RecoveryHold = held
		if err := e.admin.Update(e.ctx, state); err != nil {
			t.Fatal(err)
		}
	}
	remaining := func() []string {
		t.Helper()
		names, err := claims.Operations(e.ctx, allocation.Name)
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	held := ops.RecoveryHold{OperationReference: ops.OperationReference{Kind: "DeviceInstallation", Name: operation.Name, UID: string(operation.UID)}, Fence: operation.Status.Fence, Since: metav1.Now(), Reason: "Synthetic"}
	hold(&held)
	if none, err := e.allocator.releaseBootAddresses(e.ctx, allocation, false); err != nil || none || len(remaining()) != 2 {
		t.Fatalf("release under a hold: none=%v %v, remaining %v", none, err, remaining())
	}
	// A retry that also ended unknown holds the original as its retry name.
	retry := held
	retry.Name, retry.RetryName = operation.Name+"-retry-1", operation.Name
	hold(&retry)
	if none, err := e.allocator.releaseBootAddresses(e.ctx, allocation, false); err != nil || none || len(remaining()) != 2 {
		t.Fatalf("release under a retry's hold: none=%v %v, remaining %v", none, err, remaining())
	}
	hold(nil)
	if none, err := e.allocator.releaseBootAddresses(e.ctx, allocation, false); err != nil || none {
		t.Fatalf("release without a hold: none=%v %v", none, err)
	}
	if names := remaining(); len(names) != 1 || names[0] != allocation.Name+"-install-pending" {
		t.Fatalf("remaining = %v, want only the uncreated operation's claim", names)
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
	for _, claimer := range []bootaddress.Source{claims, e.allocator.wipeClaims()} {
		if remaining, err := claimer.Operations(e.ctx, allocation.Name); err != nil || len(remaining) != 0 {
			t.Fatalf("claims left after release: %v, %v", remaining, err)
		}
	}
	if allocation.Status.Phase != fleetv1.AllocationReleased {
		t.Fatalf("phase = %s", allocation.Status.Phase)
	}
}

// exhausted refuses every IPAM claim as IPAM does when its pool is full.
type exhausted struct{ client.Client }

func (c exhausted) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if claim, ok := object.(*unstructured.Unstructured); ok && claim.GroupVersionKind() == bootaddress.GVK {
		return &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Code: http.StatusInsufficientStorage, Reason: "InsufficientStorage",
			Details: &metav1.StatusDetails{Name: "lab-provisioning", Causes: []metav1.StatusCause{{Type: "Exhausted"}}},
		}}
	}
	return c.Client.Create(ctx, object, options...)
}

// TestAnExhaustedPoolSendsNothing refuses the installation with IPAM's
// reason and creates no operation, so nothing reaches the site.
func TestAnExhaustedPoolSendsNothing(t *testing.T) {
	e := startEnv(t)
	e.allocator.Provider = exhausted{e.admin}
	e.allocator.BootAddresses = &bootaddress.Claimer{Class: "provisioning"}
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	installation := e.installation(1, "debian-12", "")
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation}); !errors.Is(err, bootaddress.ErrExhausted) || !strings.Contains(err.Error(), "lab-provisioning") {
		t.Fatalf("install with a full pool: %v", err)
	}
	operations := &ops.DeviceInstallationList{}
	if err := e.admin.List(e.ctx, operations, client.InNamespace(e.allocator.Namespace)); err != nil || len(operations.Items) != 0 {
		t.Fatalf("operations = %d, %v", len(operations.Items), err)
	}
}
