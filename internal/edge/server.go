package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// Session is one client connection's view of the gateway: the policy-filtered
// tool catalog it may see and the pipeline-guarded invocation of those tools. The
// composition root supplies an implementation per connection (composing a
// per-connection dispatcher, the security stage chain, aggregation, and policy);
// this package translates between that implementation and the MCP wire, and so
// imports neither registry, aggregate, nor policy (ADR-0014).
type Session interface {
	// ListTools returns one page of the client's namespaced, policy-filtered
	// catalog plus the cursor for the next page ("" when the page is the last).
	// The cursor is opaque and is carried to and from the client verbatim.
	ListTools(ctx context.Context, cursor string) (page domain.Catalog, next string, err error)
	// Call resolves a namespaced tool name and runs the invocation through the
	// security pipeline, returning the downstream result. The result's Content is
	// the downstream CallToolResult as (possibly redacted) JSON bytes. It returns a
	// non-nil result or a non-nil error, never both nil.
	Call(ctx context.Context, name string, args json.RawMessage) (*domain.Result, error)
	// Close releases the connection's downstream sessions. It is called once, when
	// the client's MCP session ends.
	Close() error
}

// SessionFactory builds a Session for an authenticated client identity. It is
// called once per client connection, after the request guard has authenticated
// the identity.
type SessionFactory func(domain.Identity) (Session, error)

// Config wires the client-facing MCP server.
type Config struct {
	// ServerName and ServerVersion identify the gateway to clients during the MCP
	// initialize handshake.
	ServerName    string
	ServerVersion string
	// Guard authenticates and Origin-checks every request before it reaches the
	// MCP handler. It is required: NewHandler wraps the MCP handler with it.
	Guard *Guard
	// NewSession builds the per-connection Session for an authenticated identity.
	NewSession SessionFactory
	// SessionTimeout closes a client session that has received no requests for this
	// long, which also reclaims its downstream sessions. Zero leaves idle sessions
	// open (the registry idle reaper still reclaims downstream subprocesses).
	SessionTimeout time.Duration
}

// MCP method names the pass-through handler intercepts. Everything else falls
// through to the SDK's default handling (initialize, version negotiation, ping).
const (
	methodListTools = "tools/list"
	methodCallTool  = "tools/call"
)

// emptyObjectSchema is the input schema presented for a tool whose downstream
// advertised none; the MCP wire requires inputSchema to be a JSON Schema object,
// so a tool with no declared parameters is presented as an empty object rather
// than a null.
var emptyObjectSchema = json.RawMessage(`{"type":"object"}`)

// NewHandler builds the client-facing http.Handler: the request guard
// (Origin/DNS-rebinding validation + API-key authentication) wrapping a
// per-connection Streamable HTTP MCP server.
func NewHandler(cfg Config) http.Handler {
	h := mcp.NewStreamableHTTPHandler(newGetServer(cfg), &mcp.StreamableHTTPOptions{
		SessionTimeout: cfg.SessionTimeout,
	})
	return cfg.Guard.Wrap(h)
}

// newGetServer returns the per-connection server factory the SDK calls for each
// new session. It reads the identity the guard injected and builds that
// connection's Session and MCP server; a request without an authenticated
// identity yields a nil server (HTTP 400), defense-in-depth behind the guard.
func newGetServer(cfg Config) func(*http.Request) *mcp.Server {
	return func(r *http.Request) *mcp.Server {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			return nil
		}
		sess, err := cfg.NewSession(id)
		if err != nil {
			return nil
		}
		return buildServer(cfg.ServerName, cfg.ServerVersion, sess)
	}
}

// buildServer constructs the per-connection MCP server. It advertises the tools
// capability (the catalog is served by middleware, not AddTool, so the capability
// must be set explicitly) and installs a receiving middleware that serves
// tools/list and tools/call from sess and passes every other method through to
// the SDK. On the session's first request the middleware starts a watcher that
// closes sess when the session ends.
func buildServer(name, version string, sess Session) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: name, Version: version},
		&mcp.ServerOptions{
			// Tools are served by middleware rather than AddTool, so the capability is
			// set explicitly or a capability-gated client would never list them.
			// ListChanged stays false: this increment serves a fixed per-session
			// catalog and emits no tools/list_changed (the dynamic-catalog follow-up
			// flips it true — ADR-0014).
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
		},
	)

	var once sync.Once
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			// Arm teardown on the session's first received method (initialize, or a
			// ping that may precede it). Every session that dispatches a method is
			// covered; a connection aborted before its first dispatch is handled by
			// the registry idle reaper (ADR-0014 consequences).
			once.Do(func() { watchClose(req.GetSession(), sess) })
			switch method {
			case methodListTools:
				return handleList(ctx, sess, req)
			case methodCallTool:
				return handleCall(ctx, sess, req)
			default:
				return next(ctx, method, req)
			}
		}
	})
	return srv
}

// watchClose closes sess once the client's MCP session ends. The SDK exposes no
// session-close callback (ADR-0013/0014), so a goroutine blocks on the session's
// Wait and closes the gateway session when it returns — on client disconnect or
// SessionTimeout.
func watchClose(s mcp.Session, sess Session) {
	ss, ok := s.(*mcp.ServerSession)
	if !ok {
		return
	}
	go func() {
		_ = ss.Wait()
		_ = sess.Close()
	}()
}

// handleList serves tools/list from the session's catalog, namespacing each tool
// and carrying the opaque pagination cursor through in both directions.
func handleList(ctx context.Context, sess Session, req mcp.Request) (mcp.Result, error) {
	cursor := ""
	if params, ok := req.GetParams().(*mcp.ListToolsParams); ok && params != nil {
		cursor = params.Cursor
	}
	page, next, err := sess.ListTools(ctx, cursor)
	if err != nil {
		return nil, err
	}
	tools := make([]*mcp.Tool, 0, len(page.Tools))
	for _, t := range page.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = emptyObjectSchema
		}
		tools = append(tools, &mcp.Tool{
			Name:        t.Ref.Namespaced(),
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return &mcp.ListToolsResult{Tools: tools, NextCursor: next}, nil
}

// handleCall serves tools/call by running the named tool through the session's
// pipeline. The downstream result, carried as JSON bytes by the pipeline (so the
// outbound redactor can scan it), is decoded back into the CallToolResult the
// client expects, preserving its IsError flag. A pipeline error becomes a
// protocol error; whether an authorization denial surfaces as an error or as an
// IsError result is the pipeline's choice, not the edge's.
func handleCall(ctx context.Context, sess Session, req mcp.Request) (mcp.Result, error) {
	params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
	if !ok || params == nil {
		return nil, fmt.Errorf("edge: malformed tools/call params")
	}
	res, err := sess.Call(ctx, params.Name, params.Arguments)
	if err != nil {
		return nil, err
	}
	if res == nil {
		// Defend the contract: a nil result with a nil error would panic on the
		// decode below, and the SDK runs tool handlers without a recover, so the
		// panic would take down every session on the process, not just this call.
		return nil, fmt.Errorf("edge: nil result for tool %q", params.Name)
	}
	var out mcp.CallToolResult
	if err := json.Unmarshal(res.Content, &out); err != nil {
		return nil, fmt.Errorf("edge: decode downstream result for %q: %w", params.Name, err)
	}
	return &out, nil
}
