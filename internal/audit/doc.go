// Package audit records a structured entry for every call from the
// post-redaction view, so the audit store is never itself a leak.
//
// Writes are asynchronous: the hot path hands a record to a single writer
// goroutine over a bounded buffer, and the writer batches and group-commits to
// a JSONL sink. Overflow is tiered — security-relevant records are preserved
// while high-volume success records may be shed with a metric — so audit I/O
// never blocks the call path.
package audit
