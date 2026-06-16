# 0004 — Fail-fast pool-exhaustion load-shedding

- Status: Accepted
- Date: 2026-06-16

## Context

stdio downstreams are served by a bounded subprocess pool (ADR-0002). Under
load, demand for subprocesses can exceed a pool's capacity. The gateway must
decide what happens when a client call needs a slot and none is free.

Two options present themselves: queue the call until a slot frees, or refuse it
quickly. An unbounded queue is attractive because no call is ever rejected, but
it defers failure into memory growth and an unbounded latency cliff — it
converts a capacity problem into an out-of-memory crash and unpredictable tail
latency, which is worse than a clear, immediate refusal.

## Decision

A slot acquire waits up to a bounded `acquire_timeout` (default 2s). If no slot
becomes available within that window, the call fails fast with a clean MCP error
carrying `reason=downstream_pool_exhausted`, and a metric is incremented. The
pool is never an unbounded queue.

Separately, idle `per_client` subprocesses are reaped after `idle_ttl`
(default 90s), returning capacity and bounding the steady-state footprint.

Pool size (`pool.max`), `acquire_timeout`, and `idle_ttl` are configured per
downstream.

## Consequences

- Overload produces a fast, explicit, observable failure rather than memory
  growth and a latency cliff.
- `downstream_pool_exhausted` is surfaced as both an MCP error reason and a
  metric, so sustained overload is visible and actionable (raise the ceiling or
  reduce concurrency).
- Idle reaping trades a small spawn/reconnect cost on the next call for a
  bounded steady-state resource footprint.
- A caller can observe rejection under load; this is the intended back-pressure
  signal, not a fault to be masked by retrying into the same exhausted pool.
