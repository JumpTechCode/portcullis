# 0002 — Per-client-session downstream sessions

- Status: Accepted
- Date: 2026-06-16

## Context

Portcullis sits between many MCP clients and many downstream MCP servers. It
must maintain connections to the downstreams while serving each client.

MCP is a session-oriented protocol. A session carries negotiated protocol
version and capabilities, correlates request and response IDs, and routes
server-initiated notifications (progress, logging, `tools/list_changed`,
resource updates) back to the peer that established it. The official Go SDK
models this directly: one session per connection.

An early design shared a single connection to each downstream across all
clients and remapped JSON-RPC request IDs by hand to multiplex calls. This is
cheaper in connections but is incompatible with the protocol's per-session
model: capabilities and notifications belong to a session, so a shared
connection cannot route them to the right client without re-implementing
session semantics the SDK already owns — and it cannot express that with the
SDK at all.

## Decision

Each client session gets its own set of downstream sessions, one per downstream
that client is allowed to use. The gateway acts as an SDK client toward each
downstream and mirrors the client's negotiated protocol version and
capabilities. The SDK handles ID correlation, cancellation, and notification
routing per session; the gateway never manipulates raw JSON-RPC IDs.

For stdio downstreams, sessions are backed by a supervised, bounded subprocess
pool with a configurable `session_mode`:

- `per_client` (default): each client session gets its own subprocess, which is
  correct for stateful servers.
- `shared` (opt-in): subprocesses are checked out and in across client sessions,
  which the operator may enable only when the server is stateless.

## Consequences

- Notifications, cancellation, and per-session state are correctly isolated per
  client by construction; cross-tenant mis-routing is not expressible.
- Server-initiated capabilities (sampling, roots, elicitation) become
  structurally bridgeable later, because each client already has a real,
  isolated downstream session.
- The cost is resource usage: process and memory footprint grows with
  clients × allowed downstreams. This is bounded by pool limits, idle reaping,
  and fail-fast load-shedding, and documented as an operational limit.
  Container- or cgroup-per-tenant isolation is a deferred scaling path, not part
  of V1.
- The hand-rolled ID-remapping design is rejected and will not be revisited
  unless the protocol's session model changes.
