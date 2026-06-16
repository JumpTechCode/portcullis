# 0013 — Per-session dispatch scoping and the downstream session manager

- Status: Accepted
- Date: 2026-06-16

## Context

[ADR-0002](0002-per-client-session-downstream-sessions.md) fixes the *semantics*
of how the gateway holds downstream connections: each client session — one MCP
connection, since the SDK is one-session-per-connection — gets its own isolated
set of downstream sessions, one per downstream that client is allowed to use, so
that notifications, cancellation, and per-session state cannot cross between
clients. It does **not** fix the *mechanism* by which a tool call reaches its
client's downstream session, nor how those sessions are created, reused, and
torn down. That mechanism is the seam between the client-facing `edge` and the
`registry` that owns sessions, and it is shared with the `Dispatcher` that
executes a call. It is load-bearing — it shapes the domain leaf, the registry
API, and the edge wiring — so it is recorded here.

Several forces constrain the choice:

- **The hot-path call carries only an identity.** `domain.Call` carries the
  authenticated `Identity` (a configuration id such as `claude-desktop`) and the
  resolved tool, not a handle to a specific connection. Reaching *this
  connection's* session set therefore requires either scoping the dispatcher to
  the session or threading a per-connection key through the call.

- **`per_client` sessions are stateful for the connection's lifetime.** For an
  stdio downstream in the default `per_client` mode, a client session must talk
  to the *same* subprocess across its calls, or a stateful server loses state.
  This is not per-call acquire-and-release (which would hand out a different
  subprocess each call). At the same time, the design reaps subprocesses that
  sit idle past `idle_ttl`, so a held session can disappear between calls.

- **The SDK exposes no public per-session close hook.** The Go SDK's
  `StreamableHTTPHandler` runs an internal `onClose` when a session ends, but the
  field that would let an application observe it (`onTransportDeletion`) is
  unexported. The SDK does expose `SessionTimeout`, which closes idle client
  sessions automatically. So the gateway cannot reliably hang downstream
  teardown off an explicit client disconnect; it must tolerate not being told.

- **Isolation should be structural.** ADR-0002's guarantee is that cross-tenant
  mis-routing is "not expressible." A mechanism that keys sessions by a value
  carried on each call makes mis-keying expressible; a mechanism where a
  dispatcher can only see its own session's sessions does not.

## Decision

**Dispatch is scoped per client session, over an idle-reaped downstream session
manager. `domain.Call` is not extended with a session identifier.**

Concretely:

- **`registry` gains a session manager** that owns, per client session, that
  session's set of downstream sessions. On the first call to a downstream it
  lazily opens a session for it through the existing supervised, bounded pool;
  it caches that session and reuses it for subsequent calls within the same
  client session. This is **cache-and-re-acquire**, not a hard hold: if the
  cached session has been idle-reaped (or found unhealthy), the next call
  transparently re-acquires a fresh one. `per_client` downstreams get one cached
  session per client session; `shared` downstreams (operator-asserted stateless)
  acquire and release per call, so their subprocesses cycle across clients.

- **The edge builds per-session scope.** When a client session begins, `edge`
  constructs that session's downstream session set and a `domain.Dispatcher`
  bound to it, then composes the shared stage chain around that dispatcher. A
  call dispatched on this connection can only reach this connection's sessions —
  isolation holds by construction, with nothing on `domain.Call` to mis-key.

- **Teardown is by idle reaping, not by an explicit disconnect signal.** Because
  the SDK does not expose a session-close hook, the downstream session set is
  reclaimed when its sessions sit idle past `idle_ttl` (the pool already does
  this), and the SDK's `SessionTimeout` is set so abandoned client sessions are
  themselves closed. Graceful shutdown still closes everything explicitly. If a
  future SDK release exposes a public close hook, prompt teardown becomes an
  additive optimization — this record would be extended, not superseded.

- **Same-identity concurrent connections are isolated.** Two connections
  presenting the same client identity are two client sessions and receive two
  independent session sets, consistent with ADR-0002. The resource cost is
  bounded by the same pool limits and idle reaping as everything else.

## Consequences

- The domain leaf stays minimal: no session identifier is added to `domain.Call`,
  and the isolation guarantee is enforced by scope rather than by a key that
  could be set wrong.
- The per-call security chain's base handler is rebuilt per client session. This
  is cheap: the stages themselves (policy, redaction, secrets, resilience,
  audit) are shared and stateless with respect to a session — only the base
  dispatcher differs — so per-session construction is a few allocations.
- The session manager's cache-and-re-acquire contract makes idle reaping and
  stateful `per_client` reuse coexist: a held session is an optimization, never a
  correctness assumption, so reaping an idle session is always safe.
- Relying on idle reaping instead of an explicit disconnect means a just-departed
  client's downstream sessions linger until `idle_ttl`. This is bounded, matches
  the design's existing reaping behavior, and is the price of the SDK not
  surfacing disconnects; it is accepted and documented.
- The dynamic, per-identity tool catalog (downstream `tools/list` → namespace →
  policy filter) is served within this per-session scope, which is where
  `tools/list_changed` re-aggregation will also land. The precise registration
  mechanism on the SDK server is an implementation detail of the edge handler and
  does not change this decision.
- Server-initiated capabilities deferred from V1 (sampling, roots, elicitation)
  remain structurally bridgeable later, because each client session already holds
  real, isolated downstream sessions (consistent with ADR-0002).
