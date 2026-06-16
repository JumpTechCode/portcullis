# 0008 — Cancellation is distinct from disconnection

- Status: Accepted
- Date: 2026-06-16

## Context

Two different events can end an in-flight call: the client explicitly cancels it
(`notifications/cancelled`), or the client's transport simply disconnects.
Treating a transport disconnect as an implicit cancellation is tempting, but it
is wrong as a default. The MCP transport specification states that a disconnect
does not by itself cancel work, and cancelling expensive in-flight work on every
transient network blip both wastes that work and can leave downstream state
half-changed.

## Decision

The two signals are handled separately:

- An MCP `notifications/cancelled` from the client cancels the corresponding
  downstream call, via context cancellation on that client's downstream session.
- A transport **disconnect alone does not cancel** in-flight work. The call runs
  to completion — bounded by its timeout — unless disconnect-cancels-work
  behavior is explicitly enabled in configuration.

## Consequences

- Explicit cancellation is honored promptly and propagates to the downstream
  call.
- A flaky client connection does not discard in-flight downstream work or risk
  partial side effects on every reconnect.
- In-flight work after a disconnect is still bounded by the call timeout, so a
  disconnected client cannot pin downstream resources indefinitely.
- An operator who wants disconnect-cancels-work semantics can opt in via
  configuration; the spec-conformant behavior is the default.
