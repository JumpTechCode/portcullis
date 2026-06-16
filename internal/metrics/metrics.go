// Package metrics exposes the gateway's Prometheus instrumentation on /metrics.
//
// All collectors are registered on a dedicated *prometheus.Registry created per
// [Metrics] rather than on the global default registry. Using a private
// registry avoids duplicate-registration panics, keeps test runs isolated, and
// gives callers explicit control over what is exposed.
//
// It reports tool-call counts keyed by (client, downstream, decision), call
// latency histograms by downstream, downstream health and circuit-breaker
// state, stdio pool gauges and exhaustion fast-fails, and a fail-closed counter
// for security records that could not be audited.
//
// The exported update methods take plain string labels so the package depends
// on no other internal package; callers are responsible for label discipline
// (bounded, low-cardinality label values).
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Breaker state encoding for the portcullis_breaker_state gauge. The numeric
// encoding is part of the exposed contract; dashboards and alerts depend on it.
const (
	// BreakerClosed indicates the breaker is closed: calls flow normally.
	BreakerClosed = 0
	// BreakerHalfOpen indicates the breaker is half-open: a single probe is
	// permitted to test recovery.
	BreakerHalfOpen = 1
	// BreakerOpen indicates the breaker is open: calls fast-fail.
	BreakerOpen = 2
)

// Metrics is the gateway's Prometheus metrics surface. It owns a private
// registry and the collectors registered on it, and provides typed update
// methods so callers maintain label discipline. A Metrics is safe for
// concurrent use: every method delegates to a concurrency-safe collector.
type Metrics struct {
	registry *prometheus.Registry

	calls             *prometheus.CounterVec
	latency           *prometheus.HistogramVec
	downstreamUp      *prometheus.GaugeVec
	breakerState      *prometheus.GaugeVec
	poolInUse         *prometheus.GaugeVec
	poolExhausted     *prometheus.CounterVec
	auditUnrecordable prometheus.Counter
}

// New builds a Metrics with a fresh registry and all collectors constructed and
// registered. Each call returns an independent instance with its own registry,
// so repeated construction never panics on duplicate registration.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "portcullis_calls_total",
			Help: "Tool calls processed by the gateway, labelled by client, downstream, and policy decision.",
		}, []string{"client", "downstream", "decision"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "portcullis_call_latency_seconds",
			Help:    "Latency of downstream tool calls in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"downstream"}),
		downstreamUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "portcullis_downstream_up",
			Help: "Downstream health: 1 if the downstream is healthy, 0 otherwise.",
		}, []string{"downstream"}),
		breakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "portcullis_breaker_state",
			Help: "Circuit-breaker state by downstream: 0 closed, 1 half-open, 2 open.",
		}, []string{"downstream"}),
		poolInUse: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "portcullis_pool_in_use",
			Help: "Stdio subprocesses currently checked out of the pool by downstream.",
		}, []string{"downstream"}),
		poolExhausted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "portcullis_pool_exhausted_total",
			Help: "Stdio subprocess pool exhaustion fast-fails by downstream.",
		}, []string{"downstream"}),
		auditUnrecordable: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "portcullis_audit_unrecordable_total",
			Help: "Security records that could not be audited (fail-closed).",
		}),
	}

	m.registry.MustRegister(
		m.calls,
		m.latency,
		m.downstreamUp,
		m.breakerState,
		m.poolInUse,
		m.poolExhausted,
		m.auditUnrecordable,
	)
	return m
}

// Registry returns the private registry the collectors are registered on. It is
// the source for the [Metrics.Handler] and is exposed so callers can gather or
// extend the metric set (for example, registering process or build-info
// collectors) without reaching for the global default.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// Handler returns an http.Handler that serves the registered metrics in the
// Prometheus exposition format. Mount it at /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// RecordCall records one completed tool call: it increments the call counter
// for (client, downstream, decision) and observes latency against the
// per-downstream histogram. decision is the policy decision or reason code.
func (m *Metrics) RecordCall(client, downstream, decision string, latency time.Duration) {
	m.calls.WithLabelValues(client, downstream, decision).Inc()
	m.latency.WithLabelValues(downstream).Observe(latency.Seconds())
}

// SetDownstreamUp sets the health gauge for a downstream: 1 when up, 0 when
// down.
func (m *Metrics) SetDownstreamUp(downstream string, up bool) {
	v := 0.0
	if up {
		v = 1.0
	}
	m.downstreamUp.WithLabelValues(downstream).Set(v)
}

// SetBreakerState sets the circuit-breaker state gauge for a downstream using
// the [BreakerClosed], [BreakerHalfOpen], and [BreakerOpen] encoding.
func (m *Metrics) SetBreakerState(downstream string, state int) {
	m.breakerState.WithLabelValues(downstream).Set(float64(state))
}

// SetPoolInUse sets the gauge of stdio subprocesses currently checked out of
// the pool for a downstream.
func (m *Metrics) SetPoolInUse(downstream string, n int) {
	m.poolInUse.WithLabelValues(downstream).Set(float64(n))
}

// PoolExhausted increments the counter of stdio pool exhaustion fast-fails for
// a downstream.
func (m *Metrics) PoolExhausted(downstream string) {
	m.poolExhausted.WithLabelValues(downstream).Inc()
}

// AuditUnrecordable increments the counter of security records that could not be
// audited. The gateway fails closed when this happens, so a rising value is a
// security-relevant signal.
func (m *Metrics) AuditUnrecordable() {
	m.auditUnrecordable.Inc()
}
