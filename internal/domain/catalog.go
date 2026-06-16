package domain

import "encoding/json"

// Tool is a single callable tool as presented in the gateway's namespaced
// catalog, including the schema and description the client sees.
type Tool struct {
	// Ref identifies the downstream and tool this entry routes to.
	Ref ToolRef
	// Description is the human-readable description shown to the client.
	Description string
	// InputSchema is the tool's JSON Schema for its arguments. It is treated as
	// immutable; callers must not mutate the underlying bytes in place.
	InputSchema json.RawMessage
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
	for _, t := range c.Tools {
		if t.Ref.Namespaced() == namespaced {
			return t, true
		}
	}
	return Tool{}, false
}
