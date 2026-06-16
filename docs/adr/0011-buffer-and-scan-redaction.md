# 0011 — Buffer-and-scan redaction for V1

- Status: Accepted
- Date: 2026-06-16

## Context

Outbound redaction must remove injected secret values and PII from tool results,
errors, and notifications before they leave the gateway or are written to the
audit log. Results can in principle be streamed back to the client. True
streaming-window redaction — scanning a sliding window as bytes flow — is more
complex: it must handle a match that straddles a window boundary, and getting
that wrong risks leaking a secret split across two chunks.

## Decision

V1 buffers each tool result, scans the complete buffer, redacts, and then emits.
This is simple and correct: the scanner always sees a whole value, so a secret
cannot evade it by straddling a chunk boundary.

A hard `max_result_bytes` cap bounds the buffer. An oversized result is truncated
and flagged in the audit record rather than buffered without limit.

True streaming-window redaction is recorded as a documented stretch and is not
part of V1.

## Consequences

- Redaction correctness is straightforward, because matching always runs over a
  complete value.
- The size cap bounds memory and avoids the unbounded-buffer out-of-memory class.
  The cost is that a very large result is truncated — with the truncation
  audited — rather than streamed.
- For large results, latency includes buffering time before the first byte
  reaches the client. Results are rarely large in the tools capability, and the
  size cap keeps the worst case bounded.
- Streaming-window redaction remains available as future work without changing
  the redactor's external contract.
