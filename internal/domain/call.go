package domain

import "encoding/json"

// Call is a single in-flight tool invocation as it moves through the security
// pipeline. It carries the data stages need; behavior (deciding, injecting,
// dispatching) is supplied by the injected port interfaces.
type Call struct {
	// Client is the authenticated identity that issued the call.
	Client Identity
	// Tool is the resolved downstream tool being invoked.
	Tool ToolRef
	// Args is the raw JSON arguments object from the client request. Like
	// Tool.InputSchema it is treated as immutable: stages that redact or inject
	// must produce new bytes rather than mutating the underlying array in place,
	// which would be visible to anything else holding the same Call.
	Args json.RawMessage
}
