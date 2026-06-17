# 0014 — Edge per-session MCP server: pass-through handler, injected session port, watcher teardown

- Status: Accepted
- Date: 2026-06-16

## Context

[ADR-0013](0013-per-session-dispatch-and-downstream-session-manager.md) fixes the
*mechanism* for reaching a connection's downstream sessions: dispatch is scoped
per client session over an idle-reaped session manager, and `domain.Call` carries
no session handle. It deliberately left "the precise registration mechanism on the
SDK server" as an implementation detail of the edge handler. This record fixes
that detail — how the client-facing `edge` serves the per-session, policy-filtered
catalog and routes calls through the security pipeline on the Go MCP SDK server,
how it consumes the `registry`/`aggregate`/`policy` it is forbidden to import, and
how it tears a session down without a public SDK close hook.

Several forces constrain the choice:

- **The catalog is dynamic and per-identity.** The tools a client sees come from
  fanning `tools/list` across that client's allowed downstreams, namespacing, and
  applying the security-load-bearing policy filter (design §4, §8). The
  client-facing `tools/list` is paginated with the gateway's own opaque,
  name-stable cursor (`aggregate.Page`), which exists precisely so the merged
  catalog re-paginates correctly across changes. The SDK's static `AddTool`
  registration would serve the SDK's own index pagination instead and would force
  per-session register/remove bookkeeping on every catalog change.

- **`edge` must not import `aggregate`, `policy`, or `registry`.** The acyclic
  import DAG allows `edge → {domain, pipeline}` (plus the SDK) only. Everything
  else the edge needs at request time has to arrive as injected behavior from the
  composition root.

- **The SDK exposes no public per-session close hook** (ADR-0013): the
  `StreamableHTTPHandler`'s `onClose`/`onTransportDeletion` are unexported. It does
  expose `StreamableHTTPOptions.SessionTimeout` (idle client sessions auto-close)
  and, on the receiving side, the session is reachable from each request via
  `Request.GetSession()`, and a session's end is observable by blocking on
  `ServerSession.Wait()`.

## Decision

**The edge serves a generic pass-through `tools/list`/`tools/call` handler over a
per-connection session port injected by the composition root, and ties downstream
teardown to the SDK session via a per-session watcher goroutine.**

Concretely:

- **Injected session port.** `edge` defines, and the composition root implements,
  a per-connection seam:

  ```
  type SessionFactory func(domain.Identity) (Session, error)
  type Session interface {
      ListTools(ctx, cursor string) (page domain.Catalog, next string, err error)
      Call(ctx, name string, args json.RawMessage) (*domain.Result, error)
      Close() error
  }
  ```

  The app implements `Session` by composing a fresh `registry.ClientSession` (the
  per-connection `domain.Dispatcher`), the shared stage chain (`pipeline.Chain`),
  and `aggregate` + `policy`. `ListTools` returns one policy-filtered, namespaced,
  `aggregate.Page`d page; `Call` resolves the namespaced name to a `domain.ToolRef`,
  builds the `domain.Call`, and runs the chain. The edge consumes only `domain`
  and `pipeline` types, so the DAG holds.

- **Generic pass-through, not per-session `AddTool`.** `getServer(*http.Request)`
  builds one fresh `*mcp.Server` and one `Session` per connection and installs a
  receiving middleware that intercepts `tools/list` (→ `Session.ListTools` →
  `mcp.ListToolsResult`, the gateway cursor carried through verbatim) and
  `tools/call` (→ `Session.Call` → the `domain.Result` bytes decoded back into the
  `mcp.CallToolResult` the downstream produced). Every other method —
  `initialize`, version negotiation, ping — falls through to the SDK's default
  handler. The catalog is therefore consulted live; a future `tools/list_changed`
  re-aggregation invalidates a per-session cache and re-emits, with no tool
  re-registration.

- **Identity comes from the request context.** The ADR-0003 request guard
  authenticates and injects the `domain.Identity` ahead of the MCP handler; a
  request that somehow arrives without one yields a nil server (HTTP 400),
  defense-in-depth behind the guard.

- **Watcher teardown.** On the first intercepted request of a session, a
  `sync.Once` captures `Request.GetSession()` and launches a goroutine that blocks
  on `ServerSession.Wait()` and then calls `Session.Close()`. This reclaims the
  connection's downstream sessions promptly when the SDK session ends (client
  close or `SessionTimeout`), without depending on an unexported hook.
  `SessionTimeout` is set so abandoned sessions are closed; the registry idle
  reaper (ADR-0013) remains the backstop, and graceful shutdown still closes
  everything explicitly.

- **First edge increment caches the catalog per session.** The per-connection
  catalog is built lazily on first `tools/list` and cached for the session's
  lifetime. Live downstream `tools/list_changed` subscription, re-aggregation, and
  upstream propagation are deferred to a follow-up; nothing in this record
  precludes them, since the catalog is already served within per-session scope.

## Consequences

- The edge stays a thin protocol adapter: it owns SDK request/result translation
  and session lifecycle, and nothing about routing, policy, or aggregation, which
  keeps the DAG acyclic and the edge independently testable with an in-process SDK
  client (`mcp.NewInMemoryTransports`).
- The gateway's own name-stable pagination cursor is preserved on the wire,
  because the edge serves `tools/list` itself rather than delegating to the SDK's
  static tool registry.
- Decoding `domain.Result.Content` back into an `mcp.CallToolResult` at the edge is
  the inverse of the registry's encode; the round-trip exists so the outbound
  redaction stage can scan the whole downstream payload as bytes, and is the
  accepted cost of keeping redaction byte-level.
- One watcher goroutine exists per active connection, blocked on `Wait()`; this is
  bounded by the same max-session limits as everything else.
- The watcher arms on a session's first received method, which the SDK calls
  `getServer` (and so builds the `Session`) to handle. A connection aborted
  between server construction and that first dispatch — for example a failed SDK
  `Connect` — leaves its `Session` unclosed. Because a `Session` opens no
  downstream sessions until its first `Call`, the orphan holds no subprocess; only
  its bookkeeping lingers, and the registry idle reaper (ADR-0013) reclaims any
  resource a dispatched-then-dropped session did hold. This narrow window is the
  residue of the SDK exposing no connect-time close hook; it is accepted and
  documented rather than worked around.
- The edge maps a pipeline error to a protocol error and a returned result
  (including a tool's `IsError`) to a result, faithfully and without
  interpretation. Whether an authorization denial is represented as a protocol
  error or as an `IsError` result is therefore the pipeline's decision, settled
  when the policy stage is built, not fixed by this record.
- Relying on `Wait()` + `SessionTimeout` rather than an explicit disconnect means a
  departed client's downstream sessions are reclaimed at session end or at worst
  `idle_ttl`, consistent with ADR-0013; accepted and documented.
- Deferring live `tools/list_changed` means the per-session catalog is fixed for
  the connection's lifetime in this increment. Acceptable for V1's static-catalog
  case and revisited in the follow-up.
