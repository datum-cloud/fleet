package registration

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var casRetriesExhausted = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "fleet_registration_cas_retries_exhausted_total",
	Help: "Registration admission requests denied after exhausting the ledger CAS retry budget.",
})

func MetricsHandler() http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(casRetriesExhausted)
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
