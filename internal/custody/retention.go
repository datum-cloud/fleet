package custody

import (
	"context"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SweepRetention deletes operations that finished more than
// Config.Retention ago. An operation custody still names, as active or as
// its recovery hold, is kept, and so is a succeeded retirement until
// inventory retires the Device. Platform audit records keep the history after
// that.
func (c *Coordinator) SweepRetention(ctx context.Context) (int, error) {
	cfg := c.config()
	cutoff := c.now().Add(-cfg.Retention)
	referenced := map[types.UID]bool{}
	var custodies ops.DeviceCustodyList
	if err := c.Client.List(ctx, &custodies, client.InNamespace(cfg.Namespace)); err != nil {
		return 0, err
	}
	for _, custody := range custodies.Items {
		if active := custody.Spec.ActiveOperation; active != nil {
			referenced[types.UID(active.UID)] = true
		}
		if hold := custody.Spec.RecoveryHold; hold != nil {
			referenced[types.UID(hold.UID)] = true
		}
	}
	deleted := 0
	for _, kind := range c.Registry.Kinds() {
		list := kind.NewList()
		if err := c.Client.List(ctx, list, client.InNamespace(cfg.Namespace)); err != nil {
			return deleted, err
		}
		for _, operation := range kind.Items(list) {
			status := operation.OperationStatus()
			if !status.Phase.Ended() || status.FinishedAt == nil || !status.FinishedAt.Time.Before(cutoff) || referenced[operation.GetUID()] {
				continue
			}
			// A succeeded retirement is the only record of it until
			// inventory retires the Device, and rebuild reads it.
			if kind.Retires != nil && status.Phase == ops.PhaseSucceeded && kind.Retires(operation) {
				device, err := c.device(ctx, operation.Envelope().DeviceRef.Name)
				if err != nil {
					return deleted, err
				}
				if device != nil && inventory.LifecycleOf(device) != inventory.DeviceRetired {
					continue
				}
			}
			uid := operation.GetUID()
			if err := c.Client.Delete(ctx, operation, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
				return deleted, err
			}
			deleted++
		}
	}
	return deleted, nil
}
