package edge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// fakeSession is a test double for the injected per-connection Session port. Each
// behavior is a function field so a test supplies only what it exercises.
type fakeSession struct {
	listTools func(ctx context.Context, cursor string) (domain.Catalog, string, error)
	call      func(ctx context.Context, name string, args json.RawMessage) (*domain.Result, error)
	onClose   func()
}

func (f *fakeSession) ListTools(ctx context.Context, cursor string) (domain.Catalog, string, error) {
	return f.listTools(ctx, cursor)
}

func (f *fakeSession) Call(ctx context.Context, name string, args json.RawMessage) (*domain.Result, error) {
	return f.call(ctx, name, args)
}

func (f *fakeSession) Close() error {
	if f.onClose != nil {
		f.onClose()
	}
	return nil
}

// connect wires an in-process MCP client to srv over an in-memory transport and
// returns the client session. The server must connect before the client.
func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolRef(downstream, tool string) domain.ToolRef {
	return domain.ToolRef{Downstream: downstream, Tool: tool}
}

func TestServerListsToolsAcrossPages(t *testing.T) {
	ctx := context.Background()
	tools := []domain.Tool{
		{Ref: toolRef("github", "create_issue"), Description: "open an issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Ref: toolRef("github", "list_issues"), Description: "list issues"},
		{Ref: toolRef("search", "web"), Description: "search the web"},
	}
	var gotCursors []string
	fake := &fakeSession{
		listTools: func(_ context.Context, cursor string) (domain.Catalog, string, error) {
			gotCursors = append(gotCursors, cursor)
			switch cursor {
			case "":
				return domain.Catalog{Tools: tools[:2]}, "CUR2", nil
			case "CUR2":
				return domain.Catalog{Tools: tools[2:]}, "", nil
			default:
				return domain.Catalog{}, "", errors.New("unexpected cursor")
			}
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))

	page1, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("first ListTools: %v", err)
	}
	if got := toolNames(page1.Tools); !equal(got, []string{"github__create_issue", "github__list_issues"}) {
		t.Errorf("page 1 names = %v, want the first two namespaced tools", got)
	}
	if page1.NextCursor != "CUR2" {
		t.Errorf("page 1 next cursor = %q, want CUR2 (passed through verbatim)", page1.NextCursor)
	}
	if page1.Tools[0].Description != "open an issue" {
		t.Errorf("description not carried through: %q", page1.Tools[0].Description)
	}

	page2, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: page1.NextCursor})
	if err != nil {
		t.Fatalf("second ListTools: %v", err)
	}
	if got := toolNames(page2.Tools); !equal(got, []string{"search__web"}) {
		t.Errorf("page 2 names = %v, want the last tool", got)
	}
	if page2.NextCursor != "" {
		t.Errorf("page 2 next cursor = %q, want empty (final page)", page2.NextCursor)
	}
	if !equal(gotCursors, []string{"", "CUR2"}) {
		t.Errorf("Session saw cursors %v, want the client's cursors passed through", gotCursors)
	}
}

func TestServerListSurfacesToolMetadata(t *testing.T) {
	ctx := context.Background()
	tool := domain.Tool{
		Ref:          toolRef("github", "create_issue"),
		Title:        "Create Issue",
		Description:  "open an issue",
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`),
		Annotations:  json.RawMessage(`{"readOnlyHint":true,"title":"Create Issue"}`),
		Icons:        json.RawMessage(`[{"src":"https://example.com/i.png","mimeType":"image/png"}]`),
	}
	fake := &fakeSession{
		listTools: func(_ context.Context, _ string) (domain.Catalog, string, error) {
			return domain.Catalog{Tools: []domain.Tool{tool}}, "", nil
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))

	res, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(res.Tools))
	}
	got := res.Tools[0]

	if got.Name != "github__create_issue" || got.Title != "Create Issue" {
		t.Errorf("name/title = %q/%q, want namespaced name and carried title", got.Name, got.Title)
	}
	if got.Annotations == nil || !got.Annotations.ReadOnlyHint || got.Annotations.Title != "Create Issue" {
		t.Errorf("annotations not surfaced to client: %+v", got.Annotations)
	}
	if len(got.Icons) != 1 || got.Icons[0].Source != "https://example.com/i.png" {
		t.Errorf("icons not surfaced to client: %+v", got.Icons)
	}
	out, err := json.Marshal(got.OutputSchema)
	if err != nil {
		t.Fatalf("marshal received output schema: %v", err)
	}
	if !strings.Contains(string(out), `"url"`) {
		t.Errorf("output schema not surfaced to client: %s", out)
	}
}

func TestServerCallRoutesAndReturnsResult(t *testing.T) {
	ctx := context.Background()
	want := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "created #1"}}}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var gotName string
	var gotArgs json.RawMessage
	fake := &fakeSession{
		call: func(_ context.Context, name string, args json.RawMessage) (*domain.Result, error) {
			gotName, gotArgs = name, args
			return &domain.Result{Content: body}, nil
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "github__create_issue",
		Arguments: json.RawMessage(`{"title":"x"}`),
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if gotName != "github__create_issue" {
		t.Errorf("Session.Call name = %q, want github__create_issue", gotName)
	}
	if string(gotArgs) != `{"title":"x"}` {
		t.Errorf("Session.Call args = %s, want the raw client arguments", gotArgs)
	}
	if len(res.Content) != 1 {
		t.Fatalf("result content len = %d, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "created #1" {
		t.Errorf("downstream result not returned to client: %+v", res.Content[0])
	}
}

func TestServerCallErrorMapsToProtocolError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSession{
		call: func(_ context.Context, _ string, _ json.RawMessage) (*domain.Result, error) {
			return nil, errors.New("policy: denied")
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))

	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "github__create_issue"}); err == nil {
		t.Error("CallTool over a failing Session returned no error")
	}
}

func TestServerAdvertisesToolsCapability(t *testing.T) {
	fake := &fakeSession{
		listTools: func(context.Context, string) (domain.Catalog, string, error) {
			return domain.Catalog{}, "", nil
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))

	// Tools are served by middleware, not AddTool, so the capability must be set
	// explicitly or a capability-gated client would never call tools/list.
	if cs.InitializeResult().Capabilities.Tools == nil {
		t.Error("server did not advertise the tools capability")
	}
}

func TestServerClosesGatewaySessionOnClientDisconnect(t *testing.T) {
	closed := make(chan struct{})
	fake := &fakeSession{
		listTools: func(context.Context, string) (domain.Catalog, string, error) {
			return domain.Catalog{}, "", nil
		},
		onClose: func() { close(closed) },
	}
	cs := connect(t, buildServer("portcullis", "test", fake))

	if err := cs.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Session.Close was not called when the client disconnected")
	}
}

func TestGetServerRequiresIdentity(t *testing.T) {
	cfg := Config{
		ServerName:    "portcullis",
		ServerVersion: "test",
		NewSession: func(domain.Identity) (Session, error) {
			return &fakeSession{}, nil
		},
	}
	getServer := newGetServer(cfg)

	req := httptest.NewRequest("POST", "/", http.NoBody)
	if srv := getServer(req); srv != nil {
		t.Error("getServer returned a server for a request without an authenticated identity")
	}

	authed := req.WithContext(withIdentity(req.Context(), domain.Identity{ID: "claude-desktop"}))
	if srv := getServer(authed); srv == nil {
		t.Error("getServer returned no server for an authenticated request")
	}
}

func TestServerListErrorMapsToProtocolError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSession{
		listTools: func(context.Context, string) (domain.Catalog, string, error) {
			return domain.Catalog{}, "", errors.New("aggregate: downstream unreachable")
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))
	if _, err := cs.ListTools(ctx, &mcp.ListToolsParams{}); err == nil {
		t.Error("ListTools over a failing Session returned no error")
	}
}

func TestServerCallResultDecodeError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSession{
		call: func(context.Context, string, json.RawMessage) (*domain.Result, error) {
			// Content that is not a valid CallToolResult must surface as an error,
			// never a silently empty result.
			return &domain.Result{Content: json.RawMessage(`not json`)}, nil
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "github__create_issue"}); err == nil {
		t.Error("CallTool with an undecodable downstream result returned no error")
	}
}

func TestServerCallNilResultMapsToError(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSession{
		// A Session that returns (nil, nil) breaks its contract; the edge must turn
		// that into an error, never dereference a nil result and crash the process.
		call: func(context.Context, string, json.RawMessage) (*domain.Result, error) {
			return nil, nil
		},
	}
	cs := connect(t, buildServer("portcullis", "test", fake))
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "github__create_issue"}); err == nil {
		t.Error("CallTool with a nil result and nil error returned no error")
	}
}

func TestGetServerNilWhenFactoryFails(t *testing.T) {
	cfg := Config{
		ServerName:    "portcullis",
		ServerVersion: "test",
		NewSession: func(domain.Identity) (Session, error) {
			return nil, errors.New("no downstreams for identity")
		},
	}
	getServer := newGetServer(cfg)
	req := httptest.NewRequest("POST", "/", http.NoBody)
	authed := req.WithContext(withIdentity(req.Context(), domain.Identity{ID: "c"}))
	if srv := getServer(authed); srv != nil {
		t.Error("getServer returned a server even though the session factory failed")
	}
}

// headerRoundTripper injects fixed headers on every request, so a test client can
// present the Bearer API key and Origin the guard requires.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range h.headers {
		clone.Header.Set(k, v)
	}
	return h.base.RoundTrip(clone)
}

func TestNewHandlerServesAuthenticatedClient(t *testing.T) {
	ctx := context.Background()
	want := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	cfg := Config{
		ServerName:    "portcullis",
		ServerVersion: "test",
		Guard:         NewGuard([]string{"https://app.example"}, []ClientKey{{ID: "claude", Key: "secret-key"}}),
		NewSession: func(domain.Identity) (Session, error) {
			return &fakeSession{
				listTools: func(context.Context, string) (domain.Catalog, string, error) {
					return domain.Catalog{Tools: []domain.Tool{{Ref: toolRef("github", "create_issue"), Description: "d"}}}, "", nil
				},
				call: func(context.Context, string, json.RawMessage) (*domain.Result, error) {
					return &domain.Result{Content: body}, nil
				},
			}, nil
		},
	}
	server := httptest.NewServer(NewHandler(cfg))
	defer server.Close()

	httpClient := &http.Client{Transport: headerRoundTripper{
		base: http.DefaultTransport,
		headers: map[string]string{
			"Authorization": "Bearer secret-key",
			"Origin":        "https://app.example",
		},
	}}
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: httpClient}
	client := mcp.NewClient(&mcp.Implementation{Name: "claude", Version: "0"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("authenticated connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	tools, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if got := toolNames(tools.Tools); !equal(got, []string{"github__create_issue"}) {
		t.Errorf("served tools = %v, want [github__create_issue]", got)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "github__create_issue"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text, ok := res.Content[0].(*mcp.TextContent); !ok || text.Text != "ok" {
		t.Errorf("call result = %+v, want text 'ok'", res.Content[0])
	}
}

func TestNewHandlerRejectsUnauthenticated(t *testing.T) {
	cfg := Config{
		ServerName:    "portcullis",
		ServerVersion: "test",
		Guard:         NewGuard(nil, []ClientKey{{ID: "claude", Key: "secret-key"}}),
		NewSession: func(domain.Identity) (Session, error) {
			return &fakeSession{}, nil
		},
	}
	server := httptest.NewServer(NewHandler(cfg))
	defer server.Close()

	resp, err := http.Post(server.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated request status = %d, want 401", resp.StatusCode)
	}
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
