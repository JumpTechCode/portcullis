package domain

import "time"

// AuditRecord is the post-redaction record written for every call.
//
// It is designed for reuse via a sync.Pool on the audit hot path (design §5):
// the volatile per-call fields are overwritten on each use, and Reset clears the
// record so a pooled instance can be reused without leaking the previous call's
// data. Long-lived string fields (ClientID, ToolName) are assigned by reference
// from configuration rather than copied.
type AuditRecord struct {
	// Timestamp is when the call completed.
	Timestamp time.Time
	// ClientID is the calling client's identity.
	ClientID string
	// ToolName is the namespaced tool name.
	ToolName string
	// Decision is the policy reason code recorded for the call.
	Decision ReasonCode
	// Allowed reports whether the call was permitted by policy.
	Allowed bool
	// LatencyMS is the end-to-end call latency in milliseconds.
	LatencyMS int64
	// BreakerState is the circuit-breaker state observed for the call.
	BreakerState string
	// Redactions counts the values redacted from the result, error, and
	// notifications before this record was written.
	Redactions int
	// Truncated reports whether the result exceeded the size cap and was
	// truncated before scanning.
	Truncated bool
	// Error is the redacted error message, empty when the call succeeded.
	Error string
}

// Reset clears every field so the record can be returned to a pool and reused
// without leaking data from a previous call.
func (r *AuditRecord) Reset() { *r = AuditRecord{} }
