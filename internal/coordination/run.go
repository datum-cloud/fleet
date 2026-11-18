package coordination

import (
	"context"
	"errors"
	"time"

	"go.datum.net/fleet/internal/executor"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CellLabel selects the handoffs delivered for one cell.
const CellLabel = "coordination.fleet.datumapis.com/cell"

// Observe refreshes journal-derived observations for this cell's delivered
// handoffs on a fixed interval. Execution, retry and replacement belong to the
// Kubernetes Job controller and KRO; this loop only republishes terminal
// results so management can check freshness. A pending or invalid handoff is
// reported to onError, which must not log handoff contents.
func (c *Cell) Observe(ctx context.Context, interval time.Duration, onError func(string, error)) error {
	for {
		list, err := c.API.Resource(Resource).Namespace(c.Namespace).List(ctx, metav1.ListOptions{LabelSelector: CellLabel + "=" + c.Name})
		if err != nil {
			onError("", err)
		} else {
			for _, obj := range list.Items {
				if err := c.Publish(ctx, obj.GetName()); err != nil && !errors.Is(err, executor.ErrOutcomeUnknown) {
					onError(obj.GetName(), err)
				}
			}
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
