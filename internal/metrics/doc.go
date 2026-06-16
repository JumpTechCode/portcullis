// Package metrics exposes Prometheus instrumentation on /metrics.
//
// It reports downstream health and breaker state, call counts keyed by
// (client, downstream, decision), pool and session gauges, and latency
// histograms.
package metrics
