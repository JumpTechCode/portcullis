# 0001 — Record architecture decisions

- Status: Accepted
- Date: 2026-06-16

## Context

Portcullis makes a number of decisions that are significant and not easily
reversed: the concurrency model, the failure-handling strategy, the
authentication mechanism, and so on. Without a durable record of why each
choice was made, the reasoning is lost, and later contributors are left to
reconstruct it or unknowingly undo it.

## Decision

We keep Architecture Decision Records (ADRs) in `docs/adr`, one Markdown file
per decision, using the format described by Michael Nygard. Each record states
its status, the context that forced a decision, the decision itself, and the
consequences.

Records are immutable once accepted. A decision is changed by adding a new
record that supersedes the previous one, rather than by editing history.

## Consequences

- The motivation behind each significant choice is written down where it can be
  reviewed alongside the code.
- Contributors have a lightweight, consistent place to propose and document
  changes to the architecture.
- The set of records carries a small maintenance cost: statuses must be kept
  current as decisions are superseded.
