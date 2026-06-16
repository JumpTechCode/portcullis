# 0012 — Connection-level secret injection for V1; defer argument-level

- Status: Accepted
- Date: 2026-06-16

## Context

The design describes two secret-injection modes. **Connection-level** injection
supplies a downstream's credentials at the transport: an environment variable
for an stdio subprocess, or a request header (for example `Authorization`) for a
remote HTTP server. **Argument-level** injection fills a declared credential
parameter inside a tool's arguments and then strips that parameter from the
client-facing schema as a pipeline invariant.

The configuration shape (design §8) defines connection-level secrets —
`downstreams[].secrets: { KEY: { env: ENV_VAR } }` — but provides no way to
declare *which tool* and *which argument* receives an argument-level secret.
Implementing argument-level injection would require designing that
operator-facing configuration and the accompanying schema-stripping invariant.

## Decision

V1 implements **connection-level injection only.**

For each downstream, configured secrets are resolved from their environment
variables and applied to the session when it is created — as subprocess
environment variables for stdio downstreams, or as request headers for remote
HTTP downstreams. Each resolved value is registered with the outbound redactor,
so it is scrubbed by value from results, errors, and notifications.

Argument-level injection and its client-schema-stripping invariant are deferred.
There is **no `arg_secrets` configuration in V1.**

When argument-level injection is added, it will use a shape such as:

```yaml
downstreams:
  - name: github
    arg_secrets:
      - { tool: create_issue, param: token, env: GH_TOKEN }
```

filling the named parameter before dispatch and stripping it from the
client-facing schema on every (re-)aggregation.

## Consequences

- Connection-level injection covers the V1 downstreams (the design's examples
  authenticate at the connection level), and clients still never hold downstream
  credentials — the gateway's central promise is met.
- Resolved secret values are registered with the redactor regardless of mode, so
  the outbound by-value leak guard is unaffected by this scope choice.
- This is a deliberate, documented deferral rather than an omission: there is no
  V1 consumer of argument-level injection, and a tool that takes a credential as
  an argument is an MCP anti-pattern — credentials belong at the
  connection/transport layer, not in tool inputs — so connection-level covers the
  common, recommended case.
- Connection-level resolution is small (resolve env to value, apply at the
  session's transport, register with the redactor) and is wired where sessions
  are created rather than carried in a dedicated package.
- Adding argument-level injection later is additive — a new `arg_secrets` config
  block plus a schema-stripping stage — and does not change the connection-level
  contract. This record would be extended, not superseded.
