# Architecture Decision Records

This directory records the significant, hard-to-reverse decisions made while
building Portcullis. Each record captures the context, the decision, and its
consequences, so the reasoning is available later even when the people change.

The format follows Michael Nygard's
[Documenting Architecture Decisions](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions.html).

## Index

- [0001 — Record architecture decisions](0001-record-architecture-decisions.md)
- [0002 — Per-client-session downstream sessions](0002-per-client-session-downstream-sessions.md)
- [0003 — Static API-key client authentication for V1](0003-static-api-key-client-authentication.md)
- [0004 — Fail-fast pool-exhaustion load-shedding](0004-pool-exhaustion-load-shedding.md)
- [0005 — Failure-class-aware circuit breaking](0005-failure-class-aware-circuit-breaking.md)
- [0006 — Orphan-proof subprocess lifecycle](0006-orphan-proof-subprocess-lifecycle.md)
- [0007 — At-most-once tool calls](0007-at-most-once-tool-calls.md)
- [0008 — Cancellation is distinct from disconnection](0008-cancellation-distinct-from-disconnection.md)
- [0009 — Wildcard policy rules pin to the synced tool set](0009-wildcard-policy-rules-pin-to-synced-tool-set.md)
- [0010 — Async audit pipeline: overflow tiers and durability windows](0010-async-audit-overflow-and-durability.md)
- [0011 — Buffer-and-scan redaction for V1](0011-buffer-and-scan-redaction.md)
- [0012 — Connection-level secret injection for V1; defer argument-level](0012-connection-level-secret-injection-for-v1.md)
- [0013 — Per-session dispatch scoping and the downstream session manager](0013-per-session-dispatch-and-downstream-session-manager.md)
- [0014 — Edge per-session MCP server: pass-through handler, injected session port, watcher teardown](0014-edge-per-session-mcp-server.md)

## Adding a record

Copy the structure of an existing record, give it the next number, and set its
status. Records are immutable once accepted: to change a decision, add a new
record that supersedes the old one and update the older record's status.
