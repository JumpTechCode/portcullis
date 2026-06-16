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

## Adding a record

Copy the structure of an existing record, give it the next number, and set its
status. Records are immutable once accepted: to change a decision, add a new
record that supersedes the old one and update the older record's status.
