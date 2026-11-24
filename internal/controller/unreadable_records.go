package controller

import (
	"context"
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ReasonUnreadablePrivateRecord is the Machine condition reason used when a
// private lifecycle record attributed to the Machine cannot be decoded. The
// Machine is not allocated, installed, or released through that record until
// an operator removes it.
const ReasonUnreadablePrivateRecord = "UnreadablePrivateRecord"

// ErrRecordUnreadable identifies a private lifecycle record that does not
// decode into the current record shape. It wraps ErrRecordInvalid, so every
// caller that fails closed on an invalid record keeps doing so.
var ErrRecordUnreadable = fmt.Errorf("%w: private lifecycle record cannot be decoded and must be removed", ErrRecordInvalid)

var undecodablePrivateRecords = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "fleet_private_lifecycle_record_undecodable_total",
	Help: "Private lifecycle records that failed to decode, counted once per record per lifecycle call. Labeled by record type only.",
}, []string{"record_type"})

func init() {
	metrics.Registry.MustRegister(undecodablePrivateRecords)
}

// recordMayBelongTo attributes a private record that failed to decode without
// trusting its body. encodeRecord writes the owning claim UID to the
// claim-uid label of every asset, installation, and custody record, so the
// label is the attribution. A record without that label cannot be attributed
// and may belong to any claim, so it is treated as belonging to the caller:
// the caller fails closed rather than proceeding as if the claim had no
// record, which could re-allocate its capacity or double-bind it.
func recordMayBelongTo(secret *corev1.Secret, claimUID types.UID) bool {
	owner := secret.Labels[claimUIDLabel]
	return owner == "" || types.UID(owner) == claimUID
}

type undecodableSeenKey struct{}

type undecodableSeen struct {
	mu    sync.Mutex
	names map[string]struct{}
}

// withUndecodableReporting scopes reporting to one lifecycle call, so a record
// seen by several scans in that call is logged and counted once. Nested calls
// share the outer scope.
func withUndecodableReporting(ctx context.Context) context.Context {
	if _, ok := ctx.Value(undecodableSeenKey{}).(*undecodableSeen); ok {
		return ctx
	}
	return context.WithValue(ctx, undecodableSeenKey{}, &undecodableSeen{names: map[string]struct{}{}})
}

// ReportUndecodableRecord logs and counts one private record that failed to
// decode. The log carries the Secret identity and attributed claim UID, never
// record contents. recordType must be a fixed record kind, not a value read
// from the Secret.
func ReportUndecodableRecord(ctx context.Context, recordType string, secret *corev1.Secret, err error) {
	if seen, ok := ctx.Value(undecodableSeenKey{}).(*undecodableSeen); ok {
		seen.mu.Lock()
		_, dup := seen.names[secret.Name]
		seen.names[secret.Name] = struct{}{}
		seen.mu.Unlock()
		if dup {
			return
		}
	}
	undecodablePrivateRecords.WithLabelValues(recordType).Inc()
	log.FromContext(ctx).Error(err, "private lifecycle record cannot be decoded; remove it to unblock its claim", "recordType", recordType, "secret", secret.Namespace+"/"+secret.Name, "claimUID", secret.Labels[claimUIDLabel])
}
