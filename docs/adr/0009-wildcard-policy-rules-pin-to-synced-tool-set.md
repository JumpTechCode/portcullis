# 0009 — Wildcard policy rules pin to the synced tool set

- Status: Accepted
- Date: 2026-06-16

## Context

Policy rules may use wildcards (for example `github__*`) for authoring
convenience. But catalog filtering is security-load-bearing, and a wildcard that
auto-admits any tool matching its pattern at runtime would silently grant access
to tools that appear after the rule was written — including a new, potentially
dangerous tool added by a downstream server. That would violate deny-by-default:
a downstream could widen a client's access without any explicit decision by the
operator.

## Decision

A wildcard rule pins to the concrete set of tools observed at the last explicit
allowlist sync. Tools that appear at runtime — including those introduced via a
downstream `tools/list_changed` — are **not** auto-admitted by a matching
wildcard. They remain denied until an explicit re-sync re-expands the wildcard
against the new tool set.

Conflicting rules resolve **most-restrictive-wins**: a deny beats an allow.

## Consequences

- A downstream cannot widen a client's effective permissions merely by exposing a
  new tool that matches an existing wildcard; new tools are denied by default
  until reviewed and re-synced.
- Wildcards stay useful for authoring without becoming a silent privilege-
  escalation channel.
- Granting access to a newly-appeared tool requires an explicit re-sync of the
  allowlist. This is deliberate: the re-sync is the review point, and it is the
  intended security posture rather than an inconvenience to be optimized away.
