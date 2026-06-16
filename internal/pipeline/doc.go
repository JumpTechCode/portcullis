// Package pipeline runs the per-call security chain.
//
// Each tool call passes through an ordered list of stages — policy decision,
// secret injection, resilience (breaker and timeout), dispatch, redaction, and
// audit — composed at the application root in the manner of net/http
// middleware. Stages communicate through domain interfaces, so reordering or
// adding one is a wiring change rather than an edit here.
package pipeline
