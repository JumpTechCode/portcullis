# 0007 — At-most-once tool calls

- Status: Accepted
- Date: 2026-06-16

## Context

Network and connection failures invite retries, and retrying is the right reflex
for idempotent operations. MCP tool calls, however, are not guaranteed
idempotent: a `create_issue` that times out may already have created the issue
downstream. Automatically retrying a call that may have reached the server risks
duplicating side effects — creating two issues, sending two messages — which is
worse than surfacing a single failure.

## Decision

The gateway provides **at-most-once** delivery for tool calls. It never
automatically retries a call that may have been received by the downstream.

Retries are permitted only for failures that prove the request was never
dispatched — pre-dispatch connection failures where the request was provably not
written (for example, the dial failed, or the connection dropped before the
request bytes were sent). Once a request may have left the gateway, a failure is
reported to the client rather than retried.

## Consequences

- No tool call is silently executed twice by the gateway; the retry path cannot
  introduce duplicate side effects.
- Some transient failures that a blind retry might have masked are surfaced to
  the client instead. This is the correct trade for non-idempotent operations:
  the client, which knows whether a given call is safe to repeat, can retry on
  its own terms.
- The dispatch path must distinguish provably-pre-dispatch failures from
  ambiguous ones; only the former are eligible for automatic retry.
