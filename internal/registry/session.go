package registry

import (
	"context"
	"encoding/json"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// ToolInfo is a downstream's raw, un-namespaced tool as discovered over the
// wire. The composition root maps these to the gateway's aggregate listing,
// applying the per-downstream namespace; this package stays transport-level and
// leaves namespacing and policy filtering to higher layers.
type ToolInfo struct {
	// Name is the downstream's own tool name (not yet namespaced).
	Name string
	// Description is the human-readable tool description, if any.
	Description string
	// InputSchema is the tool's JSON Schema as raw JSON, or nil if the downstream
	// advertised none. Kept as raw bytes so it round-trips unmodified.
	InputSchema json.RawMessage
}

// DownstreamSession is a live session to one downstream MCP server. It extends
// the pool's Session (which only needs Close) with the operations the dispatcher
// performs against a downstream: calling tools, discovering them, and pinging
// for liveness. It is transport-agnostic — both the stdio (subprocess) and
// remote-HTTP session types implement it.
type DownstreamSession interface {
	Session
	// CallTool invokes a downstream tool by its un-namespaced name with the given
	// raw JSON arguments, returning the result for the outbound pipeline.
	CallTool(ctx context.Context, tool string, args json.RawMessage) (*domain.Result, error)
	// ListTools returns one page of the downstream's tools plus the next
	// pagination cursor ("" when the listing is complete).
	ListTools(ctx context.Context, cursor string) (tools []ToolInfo, next string, err error)
	// Ping checks that the downstream is responsive.
	Ping(ctx context.Context) error
}
