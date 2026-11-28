package registration

import (
	"context"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/capacity"
	"go.datum.net/fleet/internal/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const stageTwoPolicyDigest = "sha256:0c2b77e1d9a4000000000000000000000000000000000000000000000000cafe"

// TestStageTwoEnablesRegisteredCapacity runs the stage-2 rules through the
// packaged webhook against a real API server: enabling, the frozen fields of
// enabled capacity, duplicates, the projector's status writes, and removing
// orphaned capacity.
func TestStageTwoEnablesRegisteredCapacity(t *testing.T) {
	api := startAPI(t)
	ctx := context.Background()
	api.installHandler(&Handler{Projects: []string{"datum-cloud"}, Resolve: func(context.Context, string) (Project, error) {
		return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "datum-cloud", UID: "project-1"}, Client: api.admin}, nil
	}, Authorize: Authorize, Cleaning: capacity.Fixed(map[string]string{"tenant-reuse": stageTwoPolicyDigest}), Projectors: []string{"capacity-projector"}})
	operator := api.actor("operator", "system:masters")
	projector := api.actor("capacity-projector", "system:masters")
	register := func(device *inventory.Device) *metal.MachineCapacity {
		t.Helper()
		registered := api.registration(device)
		if err := operator.Create(ctx, registered); err != nil {
			t.Fatal(err)
		}
		return registered
	}
	current := func(registered *metal.MachineCapacity) *metal.MachineCapacity {
		t.Helper()
		out := &metal.MachineCapacity{}
		if err := api.admin.Get(ctx, client.ObjectKeyFromObject(registered), out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	edit := func(registered *metal.MachineCapacity, policy, cleaning, pool string) error {
		next := current(registered)
		next.Spec.AllocationPolicy, next.Spec.CleaningPolicyRef.Name = policy, cleaning
		if next.Labels == nil {
			next.Labels = map[string]string{}
		}
		if pool == "" {
			delete(next.Labels, PoolLabel)
		} else {
			next.Labels[PoolLabel] = pool
		}
		return operator.Update(ctx, next)
	}

	t.Run("enabling needs a resolving cleaning policy and a pool", func(t *testing.T) {
		registered := register(api.device("enable", "default", "enable-serial", "a1111111-1111-1111-1111-111111111111"))
		// The CRD already refuses Enabled capacity without a cleaning policy.
		if err := edit(registered, "Enabled", "", "general"); err == nil {
			t.Fatal("capacity was enabled without a cleaning policy")
		}
		requireDenied(t, edit(registered, "Enabled", "invented-policy", "general"), `cleaning policy "invented-policy" does not resolve`)
		requireDenied(t, edit(registered, "Enabled", "tenant-reuse", ""), "capacity-pool label")
		// While Disabled, the three fields change freely.
		if err := edit(registered, "Disabled", "tenant-reuse", "general"); err != nil {
			t.Fatalf("a Disabled edit: %v", err)
		}
		if err := edit(registered, "Enabled", "tenant-reuse", "general"); err != nil {
			t.Fatalf("enabling: %v", err)
		}
		// Enabled capacity changes only to Disabled.
		requireDenied(t, edit(registered, "Enabled", "tenant-reuse", "other-pool"), "changes only to Disabled")
		requireDenied(t, edit(registered, "Enabled", "other-policy", "general"), "changes only to Disabled")
		requireDenied(t, edit(registered, "Disabled", "tenant-reuse", "other-pool"), "changes only to Disabled")
		if err := edit(registered, "Disabled", "tenant-reuse", "general"); err != nil {
			t.Fatalf("disabling: %v", err)
		}
		if err := edit(registered, "Disabled", "tenant-reuse", "other-pool"); err != nil {
			t.Fatalf("moving pools while Disabled: %v", err)
		}
	})

	t.Run("a duplicate Device blocks enabling", func(t *testing.T) {
		registered := register(api.device("original", "default", "twin-serial", "b1111111-1111-1111-1111-111111111111"))
		api.device("late-twin", "default", "TWIN-SERIAL", "b2222222-2222-2222-2222-222222222222")
		requireDenied(t, edit(registered, "Enabled", "tenant-reuse", "general"), "shares recorded hardware identity")
	})

	t.Run("only the projector writes registered capacity status", func(t *testing.T) {
		registered := register(api.device("status", "default", "status-serial", "c1111111-1111-1111-1111-111111111111"))
		edited := current(registered)
		edited.Status.Conditions = []metav1.Condition{{Type: "Available", Status: metav1.ConditionTrue, Reason: "Edited", LastTransitionTime: metav1.Now()}}
		requireDenied(t, operator.Status().Update(ctx, edited), "only the capacity projector")
		projected := current(registered)
		projected.Status.Conditions = []metav1.Condition{{Type: "Available", Status: metav1.ConditionFalse, Reason: "NotHealthy", LastTransitionTime: metav1.Now()}}
		if err := projector.Status().Update(ctx, projected); err != nil {
			t.Fatalf("the projector's status write: %v", err)
		}
	})

	t.Run("orphaned capacity is removed only when nothing uses it", func(t *testing.T) {
		device := api.device("orphan", "default", "orphan-serial", "d1111111-1111-1111-1111-111111111111")
		registered := register(device)
		requireDenied(t, operator.Delete(ctx, current(registered)), "removed only after its Device is deleted or Retired")
		if err := edit(registered, "Enabled", "tenant-reuse", "general"); err != nil {
			t.Fatal(err)
		}
		if err := api.admin.Delete(ctx, device); err != nil {
			t.Fatal(err)
		}
		requireDenied(t, operator.Delete(ctx, current(registered)), "disable the capacity before deleting it")
		if err := edit(registered, "Disabled", "tenant-reuse", "general"); err != nil {
			t.Fatal(err)
		}
		allocation := &metal.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: "alloc-orphan", Namespace: "default", Labels: map[string]string{
			metal.LabelMachineUID: "e2222222-2222-2222-2222-222222222222", metal.LabelConsumerUID: "e1111111-1111-1111-1111-111111111111", metal.LabelDeviceUID: string(device.UID)}}, Spec: metal.MachineAllocationSpec{
			ConsumerRef:        metal.TypedReference{APIGroup: "resourcemanager.miloapis.com", Kind: "Project", Name: "consumer-example"},
			MachineRef:         metal.AllocatedMachineReference{Namespace: "default", Name: "machine"},
			MachineCapacityRef: metal.NamespacedReference{Namespace: "default", Name: registered.Name},
			DeviceRef:          metal.AllocatedDeviceReference{Name: device.Name},
			Accepted: metal.AcceptedContract{MachineClassRef: metal.ContentReference{Name: "class", Generation: 1, Digest: stageTwoPolicyDigest}, MachineProfileRef: metal.ContentReference{Name: "profile", Generation: 1, Digest: stageTwoPolicyDigest},
				CPU: metal.CPURequirement{Architecture: "amd64", Cores: 2}, Memory: metal.MemoryRequirement{Capacity: "2Gi"}},
			Authority:            metal.AllocationAuthority{Principal: "fixture", Decision: "fixture"},
			CleaningPolicyDigest: stageTwoPolicyDigest, BootInterface: "primary", TargetDisks: []string{"disk-0"},
		}}
		if err := api.admin.Create(ctx, allocation); err != nil {
			t.Fatal(err)
		}
		allocation.Status.Phase = metal.AllocationReleasing
		if err := api.admin.Status().Update(ctx, allocation); err != nil {
			t.Fatal(err)
		}
		requireDenied(t, operator.Delete(ctx, current(registered)), "still names the capacity")
		allocation.Status.Phase = metal.AllocationReleased
		if err := api.admin.Status().Update(ctx, allocation); err != nil {
			t.Fatal(err)
		}
		if err := operator.Delete(ctx, current(registered)); err != nil {
			t.Fatalf("removing orphaned capacity: %v", err)
		}
	})

	t.Run("a Retired Device's Disabled capacity can be removed", func(t *testing.T) {
		device := api.device("retiring", "default", "retiring-serial", "f1111111-1111-1111-1111-111111111111")
		registered := register(device)
		api.retire(device)
		if err := operator.Delete(ctx, current(registered)); err != nil {
			t.Fatalf("removing a Retired Device's capacity: %v", err)
		}
	})
}
