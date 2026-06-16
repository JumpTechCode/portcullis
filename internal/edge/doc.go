// Package edge is the client-facing side of the gateway.
//
// It serves the Streamable HTTP MCP endpoint, validates the request Origin to
// guard against DNS-rebinding, authenticates the client API key into an
// identity, negotiates the MCP protocol version, and manages the per-client
// session lifecycle.
package edge
