package allocation

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/cleaning"
	"go.datum.net/fleet/internal/custody"
)

func (e *env) policy() *ops.CleaningPolicy {
	e.t.Helper()
	policy := &ops.CleaningPolicy{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: "example-cleaning"}, policy); err != nil {
		e.t.Fatal(err)
	}
	return policy
}

// TestAllocationPinsItsCleaningPolicy pins a copy of the capacity's policy
// and its digest, keeps it when the policy is deleted, and wipes under it.
func TestAllocationPinsItsCleaningPolicy(t *testing.T) {
	e := startEnv(t)
	e.server(1)
	policy := e.policy()

	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := cleaning.Digest(policy.Spec)
	if err != nil {
		t.Fatal(err)
	}
	pinned := allocation.Spec.CleaningPolicy
	if pinned.Name != policy.Name || pinned.UID != string(policy.UID) || pinned.Digest != digest || pinned.Spec.Rules[0].Name != "nvme-ssd" {
		t.Fatalf("pinned = %+v", pinned)
	}

	// The spec cannot change, so a pinned digest always names what a
	// provider can still read.
	changed := policy.DeepCopyObject().(*ops.CleaningPolicy)
	changed.Spec.Assurance = ops.AssurancePurge
	if err := e.admin.Update(e.ctx, changed); !apierrors.IsInvalid(err) {
		t.Fatalf("updating a policy spec = %v, want Invalid", err)
	}

	// Release does not need the policy to exist.
	if err := e.admin.Delete(e.ctx, policy); err != nil {
		t.Fatal(err)
	}
	e.release(allocation, machine(1))
	wipe := &ops.DeviceWipe{}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: namespace, Name: custody.WipeName(allocation.Name, 1)}, wipe); err != nil {
		t.Fatal(err)
	}
	if wipe.Spec.Policy != pinned.CleaningPolicyReference {
		t.Fatalf("wipe policy = %+v, want %+v", wipe.Spec.Policy, pinned.CleaningPolicyReference)
	}
}

// TestCapacityWithoutAUsableCleaningPolicyIsNotAllocated skips capacity
// whose policy is missing or invalid, and claims nothing.
func TestCapacityWithoutAUsableCleaningPolicyIsNotAllocated(t *testing.T) {
	cases := map[string]func(e *env){
		"missing policy": func(e *env) {
			if err := e.admin.Delete(e.ctx, e.policy()); err != nil {
				e.t.Fatal(err)
			}
		},
		"invalid policy": func(e *env) {
			if err := e.admin.Delete(e.ctx, e.policy()); err != nil {
				e.t.Fatal(err)
			}
			// The CRD admits it; only the webhook, absent here, would refuse
			// Overwrite with Canary. The allocator checks again at pin time.
			invalid := examplePolicy(namespace)
			invalid.Spec.Rules[0].Verify = ops.VerifySpec{Mode: ops.VerifyCanary, Samples: 64}
			e.create(invalid)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := startEnv(t)
			device := e.server(1)
			setup(e)
			if _, err := e.allocator.Allocate(e.ctx, e.request(1)); !errors.Is(err, ErrInsufficientCapacity) {
				t.Fatalf("allocate = %v, want ErrInsufficientCapacity", err)
			}
			if found, err := e.allocator.Get(e.ctx, machine(1)); err != nil || found != nil {
				t.Fatalf("allocation = %v, %v", found, err)
			}
			if holder := e.custody(device.Name).Spec.Holder.Type; holder != ops.HolderNone {
				t.Fatalf("holder = %s, want None", holder)
			}
		})
	}
}
