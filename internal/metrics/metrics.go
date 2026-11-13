// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	SessionAdmissions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fleet_machine_management_session_admissions_total", Help: "Machine-management session admission decisions."}, []string{"outcome"})
	ActiveSessions    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "fleet_machine_management_active_sessions", Help: "Active location gateway sessions."})
	RevocationLatency = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "fleet_machine_management_revocation_latency_seconds", Help: "Time from public deletion to location teardown outcome.", Buckets: prometheus.DefBuckets})
	BMCReachability   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fleet_machine_management_bmc_requests_total", Help: "BMC adapter request outcomes."}, []string{"outcome"})
	Operations        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fleet_machine_management_operations_total", Help: "Durably journaled mutation outcomes."}, []string{"outcome"})
	UnknownOperations = prometheus.NewGauge(prometheus.GaugeOpts{Name: "fleet_machine_management_unknown_operations", Help: "Operations with an unknown physical outcome."})
)

func init() {
	controllermetrics.Registry.MustRegister(SessionAdmissions, ActiveSessions, RevocationLatency, BMCReachability, Operations, UnknownOperations)
}
