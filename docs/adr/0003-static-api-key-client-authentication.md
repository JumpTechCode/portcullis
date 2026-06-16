# 0003 — Static API-key client authentication for V1

- Status: Accepted
- Date: 2026-06-16

## Context

Clients must authenticate to the gateway so it can resolve a client identity and
apply that identity's policy. The MCP specification describes OAuth 2.x-based
authorization for HTTP transports. A full OAuth implementation — authorization
server metadata, token issuance and rotation, scope handling — is substantial,
and the gateway's first priority is the policy, secret-injection, and redaction
path rather than an identity provider.

## Decision

For V1, clients authenticate with a static API key. Each configured client maps
a named identity to an environment variable that holds its key; the gateway
compares presented keys in constant time. Keys are never written to the
configuration file or logs.

This is a deliberate, documented deviation from the specification's OAuth-based
HTTP authorization.

## Consequences

- The authentication path is small and easy to reason about, keeping the focus
  on the gateway's security pipeline.
- Static keys lack the rotation and delegation properties of OAuth. To reduce
  the operational cost, the client and policy maps support scoped hot-reload, so
  adding a client or rotating a key takes effect without a restart and without
  dropping live sessions.
- The edge still enforces the protocol's transport-level protections — `Origin`
  validation against DNS rebinding, localhost binding by default, and protocol
  version negotiation — independently of the authentication mechanism.
- OAuth-based client authentication is recorded as future work. Adopting it
  would supersede this record.
