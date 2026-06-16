# 0005 — Failure-class-aware circuit breaking

- Status: Accepted
- Date: 2026-06-16

## Context

The gateway calls many downstream servers, each exposing many tools. A breaker
that trips on any error from a server would let a single slow or failing tool
take down every other tool on that server. But genuine server-health failures —
the process is down, the connection resets — should stop traffic to that server
quickly. These are different failure classes, and conflating them produces
either too little protection or too much collateral damage.

The half-open recovery probe matters too. Using the next real user call as the
probe makes a heavy call extend the outage and gives non-uniform probe cost; it
also risks several concurrent calls all probing a still-broken server at once.

## Decision

Failures are classified, and the breaker is scoped to match the class:

- **Transport/connection failures** (dial failure, connection reset, HTTP 5xx,
  broken pipe) are server-health signals and trip a breaker scoped to the
  downstream **target**.
- **Individual tool-call timeouts** are tool-specific and are tracked per
  **`(downstream, tool)`**. They do **not** trip the target breaker, so a slow
  tool cannot down its siblings on the same server.
- The **half-open probe is a lightweight MCP `ping`**, not the next user call,
  giving uniform probe cost and never extending the outage with a heavy call.
- Half-open is **single-flight**: exactly one probe is in flight at a time;
  other calls fast-fail until that probe resolves.

The residual blast radius — a genuinely dead server affecting all of its tools —
is accepted and documented. That is a real server-health failure, and tripping
the target breaker for it is the correct behavior.

## Consequences

- A misbehaving tool is isolated to itself; healthy tools on the same server
  keep serving.
- A dead or unreachable server is shed quickly at the target level.
- Recovery is probed cheaply and uniformly, and a single probe gates reopening,
  so a recovering server is not stampeded.
- The breaker carries two scopes (per-target and per-`(downstream, tool)`),
  which is more state to maintain than a single per-server breaker. The
  isolation it buys justifies the added state.
