// Package registry manages the gateway's downstream MCP sessions.
//
// For stdio downstreams it supervises a bounded subprocess pool with lazy
// spawn, idle reaping, exhaustion load-shedding, and orphan-proof teardown.
// For remote downstreams it dials and supervises Streamable HTTP sessions with
// backoff. It owns health checks and the per-client-session connection
// lifecycle.
package registry
