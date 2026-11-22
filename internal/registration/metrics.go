package registration

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var casRetriesExhausted = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "fleet_registration_cas_retries_exhausted_total",
	Help: "Registration admission requests denied after exhausting the ledger CAS retry budget.",
})

// Ledger gauges carry no labels: every replica reads the same single CAS root,
// and the values must not expose authority, provider scope or reservation data.
var (
	ledgerReadableDesc = prometheus.NewDesc("fleet_registration_ledger_readable",
		"1 when this scrape read and verified the registration root and every sealed segment; 0 when the ledger was unavailable and the other ledger gauges are omitted.", nil, nil)
	ledgerTailBytesDesc = prometheus.NewDesc("fleet_registration_ledger_tail_bytes",
		"Encoded bytes in the mutable ledger tail.", nil, nil)
	ledgerTailHeadroomDesc = prometheus.NewDesc("fleet_registration_ledger_tail_headroom_bytes",
		"Bytes left before the mutable tail budget stops registration growth and a signed expansion is required.", nil, nil)
	ledgerSealedSegmentsDesc = prometheus.NewDesc("fleet_registration_ledger_sealed_segments",
		"Immutable ledger segments sealed by signed expansion.", nil, nil)
	ledgerRemainingSegmentsDesc = prometheus.NewDesc("fleet_registration_ledger_remaining_segments",
		"Segments still available to signed expansion; at 0 expansion is refused.", nil, nil)
	ledgerEpochDesc = prometheus.NewDesc("fleet_registration_ledger_epoch",
		"Ledger routing epoch; advances once per completed expansion.", nil, nil)
	ledgerExpansionPendingDesc = prometheus.NewDesc("fleet_registration_ledger_expansion_pending",
		"1 while a retained signed expansion has not completed; admission is non-admitting until it does.", nil, nil)
	activationVerifiedDesc = prometheus.NewDesc("fleet_registration_activation_verified",
		"1 when the retained signed activation and the installed fence currently verify; 0 when registration is non-admitting.", nil, nil)
)

// statusTimeout matches /status so a slow ledger read cannot hold a scrape open
// past the server's write timeout.
const statusTimeout = 5 * time.Second

// MetricsHandler exposes the CAS-exhaustion counter and any additional
// collectors on a fresh registry, so nothing is published through the global
// default registry.
func MetricsHandler(collectors ...prometheus.Collector) http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(casRetriesExhausted)
	registry.MustRegister(collectors...)
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// metricsHandler reads the ledger once per scrape, bound to the scrape's own
// request context, using the same verified read as /status.
func (handler *Handler) metricsHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		MetricsHandler(ledgerCollector{handler: handler, ctx: request.Context()}).ServeHTTP(writer, request)
	})
}

type ledgerCollector struct {
	handler *Handler
	ctx     context.Context
}

func (collector ledgerCollector) Describe(descriptions chan<- *prometheus.Desc) {
	for _, description := range []*prometheus.Desc{ledgerReadableDesc, ledgerTailBytesDesc, ledgerTailHeadroomDesc, ledgerSealedSegmentsDesc, ledgerRemainingSegmentsDesc, ledgerEpochDesc, ledgerExpansionPendingDesc, activationVerifiedDesc} {
		descriptions <- description
	}
}

func (collector ledgerCollector) Collect(metrics chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(collector.ctx, statusTimeout)
	defer cancel()
	status, err := collector.handler.Status(ctx)
	if err != nil {
		// An unreadable ledger is unknown, not empty: emitting zero headroom or
		// zero segments would be a fabricated value, so only readability is set.
		metrics <- prometheus.MustNewConstMetric(ledgerReadableDesc, prometheus.GaugeValue, 0)
		return
	}
	gauge := func(description *prometheus.Desc, value float64) {
		metrics <- prometheus.MustNewConstMetric(description, prometheus.GaugeValue, value)
	}
	gauge(ledgerReadableDesc, 1)
	gauge(ledgerTailBytesDesc, float64(status.TailBytes))
	gauge(ledgerTailHeadroomDesc, float64(status.HeadroomBytes))
	gauge(ledgerSealedSegmentsDesc, float64(MaxSegments-status.RemainingSegments))
	gauge(ledgerRemainingSegmentsDesc, float64(status.RemainingSegments))
	gauge(ledgerEpochDesc, float64(status.Epoch))
	gauge(ledgerExpansionPendingDesc, boolGauge(status.Pending))
	gauge(activationVerifiedDesc, boolGauge(status.ActivationVerified))
}

func boolGauge(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
