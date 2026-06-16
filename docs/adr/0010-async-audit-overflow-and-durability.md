# 0010 — Async audit pipeline: overflow tiers and durability windows

- Status: Accepted
- Date: 2026-06-16

## Context

Every call produces an audit record, written from the post-redaction view so the
audit store is never itself a leak. Three goals are in tension. Audit I/O must
never block or bottleneck the hot path. Audit must not lose security-relevant
records. And audit needs a defined durability story. A fully synchronous
fsync-per-record design is durable but slow; a fire-and-forget design is fast but
loses records and offers no durability guarantee. Neither is acceptable on its
own.

## Decision

Audit uses a single-writer pipeline with tiered overflow and explicit durability
windows.

- **Off the hot path.** The call path performs a non-blocking handoff of a
  pooled `*AuditRecord` to a bounded buffer. A dedicated writer goroutine
  marshals, batches (by record count or a time tick), and writes to a
  `bufio.Writer` over a JSONL sink, with group-commit `fsync`.
- **Tiered overflow.** High-volume **success** records are soft-shed when the
  buffer is near capacity (high-water mark, default ~85%), incrementing a metric.
  **Security** records — deny, authentication failure, redaction — take the hard
  path: they block up to `security_block_timeout`, and if the sink is still
  wedged the originating request is **rejected** (fail-closed) with an alarm
  metric. An un-auditable security decision does not proceed. The buffer is never
  an unbounded stall.
- **Two durability windows.** The `bufio` flush (≈50 ms) bounds loss on
  application crash or panic — records reach the OS page cache. The `fsync`
  (≈1 s) bounds loss on OS crash or power loss — records reach disk. Both are
  drained, with a final `fsync`, on shutdown.

## Consequences

- The gateway's throughput does not depend on sink latency; audit I/O is never on
  the hot path.
- Security records survive bursts that shed success records; the audit store
  cannot silently drop a deny.
- A wedged sink fails security decisions closed rather than blocking the gateway
  or letting a decision proceed un-audited.
- The durability windows are bounded and documented, not absolute. A crash within
  a flush/fsync window can lose the most recent **success** records; security
  records are fail-closed rather than lost. This is the accepted trade for
  keeping I/O off the hot path.
