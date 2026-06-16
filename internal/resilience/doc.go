// Package resilience guards downstream calls with timeouts and circuit
// breaking.
//
// Breaking is failure-class-aware: transport failures trip a per-target
// breaker, while individual tool-call timeouts are tracked per (target, tool)
// and do not sink the target. The half-open probe is a lightweight MCP ping
// admitted single-flight, so probing cost is uniform and bounded.
package resilience
