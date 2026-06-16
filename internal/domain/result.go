package domain

import "encoding/json"

// Result is the outcome of a tool call as it flows back through the pipeline:
// from dispatch, through outbound redaction, to the client. The payload is kept
// as raw JSON so the redactor can scan and rewrite it without the pipeline
// needing to understand the downstream's content shape.
type Result struct {
	// Content is the raw tool-call result payload (the MCP CallToolResult as
	// JSON). Outbound redaction rewrites it; like Call.Args it is treated as
	// immutable, so a stage that redacts produces new bytes.
	Content json.RawMessage
	// IsError reports whether the downstream returned a tool execution error.
	IsError bool
}
