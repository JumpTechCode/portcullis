// Package redact scans outbound data for sensitive values.
//
// It removes injected secret values by exact match (Aho-Corasick) and matches
// configurable PII patterns with regular expressions gated behind a cheap
// literal pre-screen. Redaction is applied to tool results, JSON-RPC errors,
// and notifications before they leave the gateway or are written to the audit
// log. By-value secret redaction is best-effort and labelled as such.
package redact
