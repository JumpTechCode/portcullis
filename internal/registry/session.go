package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

// sdkSession is the transport-neutral core of a DownstreamSession: the
// tool-calling, listing, and ping behavior that is identical whether the SDK
// client speaks to a local subprocess (stdio) or a remote endpoint (HTTP). Both
// concrete session types embed it and add only their own teardown — the stdio
// session reaps a process group, the HTTP session just closes the session.
//
// It deliberately does not implement Close: teardown is transport-specific and
// each concrete type owns its own idempotent Close. closeSession is the shared
// half (closing the SDK session) that both call.
type sdkSession struct {
	cs *mcp.ClientSession
}

// CallTool invokes the named downstream tool. The raw JSON arguments are passed
// through to the SDK (which marshals json.RawMessage verbatim), and the result
// is marshaled whole to JSON so the outbound redactor can scan every byte the
// downstream returned, including error content.
func (s *sdkSession) CallTool(ctx context.Context, tool string, args json.RawMessage) (*domain.Result, error) {
	params := &mcp.CallToolParams{Name: tool, Arguments: args}
	res, err := s.cs.CallTool(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("call tool %q: %w", tool, err)
	}
	content, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("marshal result of tool %q: %w", tool, err)
	}
	return &domain.Result{Content: content, IsError: res.IsError}, nil
}

// ListTools returns one page of the downstream's advertised tools. The cursor is
// forwarded as-is and the SDK's next cursor is returned for the caller to
// continue pagination.
func (s *sdkSession) ListTools(ctx context.Context, cursor string) ([]ToolInfo, string, error) {
	res, err := s.cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
	if err != nil {
		return nil, "", fmt.Errorf("list tools: %w", err)
	}
	tools := make([]ToolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, err := marshalSchema(t.InputSchema)
		if err != nil {
			return nil, "", fmt.Errorf("marshal input schema for tool %q: %w", t.Name, err)
		}
		tools = append(tools, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return tools, res.NextCursor, nil
}

// Ping checks downstream liveness by delegating to the SDK's ping.
func (s *sdkSession) Ping(ctx context.Context) error {
	if err := s.cs.Ping(ctx, nil); err != nil {
		return fmt.Errorf("ping downstream: %w", err)
	}
	return nil
}

// closeSession closes the underlying SDK session, the shared first step of every
// concrete session's teardown. It is safe to call on a zero session.
func (s *sdkSession) closeSession() error {
	if s.cs == nil {
		return nil
	}
	if err := s.cs.Close(); err != nil {
		return fmt.Errorf("close downstream session: %w", err)
	}
	return nil
}

// onceCloser runs a teardown function exactly once and memoizes its error, so a
// session's Close is safe to call repeatedly. Idempotent close matters for the
// stdio session in particular: a second process-group kill after the pid has
// been reaped (and possibly recycled) could signal an unrelated group.
type onceCloser struct {
	once sync.Once
	err  error
}

// do runs fn the first time it is called and records its error; later calls are
// no-ops that return the recorded error.
func (o *onceCloser) do(fn func() error) error {
	o.once.Do(func() { o.err = fn() })
	return o.err
}

// marshalSchema renders an SDK tool input schema to raw JSON. From the client
// side the SDK delivers the schema as a decoded value (commonly a
// map[string]any); marshaling round-trips it. A nil schema yields nil bytes.
func marshalSchema(schema any) (json.RawMessage, error) {
	if schema == nil {
		return nil, nil
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	return b, nil
}
