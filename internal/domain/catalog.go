package domain

import "encoding/json"

// Tool is a single callable tool as presented in the gateway's namespaced
// catalog, including the schema, description, and display metadata the client
// sees.
//
// The metadata fields beyond Ref are carried verbatim from the downstream's
// listing so the client receives the tool exactly as the downstream advertised
// it. OutputSchema, Annotations, and Icons are kept as opaque raw JSON — the
// gateway never interprets them, and the domain layer stays free of any
// transport (MCP SDK) types. All of them are treated as immutable; callers must
// not mutate the underlying bytes in place.
type Tool struct {
	// Ref identifies the downstream and tool this entry routes to.
	Ref ToolRef
	// Title is the optional human-readable display name for the tool.
	Title string
	// Description is the human-readable description shown to the client.
	Description string
	// InputSchema is the tool's JSON Schema for its arguments.
	InputSchema json.RawMessage
	// OutputSchema is the optional JSON Schema for a structured result, or nil if
	// the downstream advertised none. Clients validate structured output against it.
	OutputSchema json.RawMessage
	// Annotations is the tool's optional annotations (display and behavior hints)
	// as raw JSON, or nil if none.
	Annotations json.RawMessage
	// Icons is the tool's optional icon set as raw JSON, or nil if none.
	Icons json.RawMessage
}

// Catalog is the set of tools presented to a client, after namespacing and
// policy filtering.
type Catalog struct {
	Tools []Tool
}

// Lookup returns the tool with the given namespaced name and whether it exists.
//
// The scan is linear by design: a per-client catalog holds only the tools that
// client may see (tens of entries), built once and queried per call, so a map
// would add allocation and upkeep for no real gain at this scale.
func (c Catalog) Lookup(namespaced string) (Tool, bool) {
	// Index rather than range by value: Tool now carries several metadata fields,
	// so copying each entry per iteration is needless work.
	for i := range c.Tools {
		if c.Tools[i].Ref.Namespaced() == namespaced {
			return c.Tools[i], true
		}
	}
	return Tool{}, false
}
