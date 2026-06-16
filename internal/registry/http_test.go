package registry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/registry"
)

// authHeader is the connection-level header the HTTP factory tests inject and
// the fake server records, proving header injection reached the downstream.
const authHeader = "Authorization"

// newFakeHTTPDownstream starts an httptest.Server serving a minimal MCP server
// over the Streamable HTTP transport, exposing the greet tool. A thin wrapper
// records the request headers of every MCP request so connection-level header
// injection can be asserted. It returns the MCP endpoint URL and an accessor for
// the last recorded value of a named header.
func newFakeHTTPDownstream(t *testing.T) (endpoint string, lastHeader func(name string) string) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-http-downstream", Version: "test"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "greet", Description: "Returns a greeting."},
		func(_ context.Context, _ *mcp.CallToolRequest, in greetIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "hello " + in.Name}},
			}, nil, nil
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	var mu sync.Mutex
	seen := make(map[string]string)
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		for name := range r.Header {
			if v := r.Header.Get(name); v != "" {
				seen[name] = v
			}
		}
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpSrv.Close)

	return httpSrv.URL, func(name string) string {
		mu.Lock()
		defer mu.Unlock()
		return seen[name]
	}
}

func TestHTTPFactoryConnectsAndListsTools(t *testing.T) {
	endpoint, _ := newFakeHTTPDownstream(t)
	f := registry.NewHTTPFactory(registry.HTTPConfig{URL: endpoint})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	tools, next, err := ds.ListTools(ctx, "")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if next != "" {
		t.Errorf("unexpected pagination cursor %q", next)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "greet") {
		t.Errorf("tool %q missing from ListTools result %v", "greet", names)
	}
	for _, tool := range tools {
		if len(tool.InputSchema) == 0 {
			continue
		}
		var anyJSON any
		if err := json.Unmarshal(tool.InputSchema, &anyJSON); err != nil {
			t.Errorf("tool %q InputSchema is not valid JSON: %v", tool.Name, err)
		}
	}
}

func TestHTTPFactoryCallToolGreet(t *testing.T) {
	endpoint, _ := newFakeHTTPDownstream(t)
	f := registry.NewHTTPFactory(registry.HTTPConfig{URL: endpoint})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	args, _ := json.Marshal(greetIn{Name: "portcullis"})
	res, err := ds.CallTool(ctx, "greet", args)
	if err != nil {
		t.Fatalf("CallTool greet: %v", err)
	}
	if res.IsError {
		t.Fatalf("greet reported a tool error: %s", res.Content)
	}
	if !containsString(t, res.Content, "hello portcullis") {
		t.Errorf("greet content %s does not contain greeting", res.Content)
	}
}

// TestHTTPFactoryHeaderInjection is the connection-level header-injection proof:
// a secret bearer token is supplied only as a configured header, and the fake
// server records it from the wire — the client never sends it itself.
func TestHTTPFactoryHeaderInjection(t *testing.T) {
	endpoint, lastHeader := newFakeHTTPDownstream(t)
	const token = "Bearer s3cr3t-from-gateway"
	f := registry.NewHTTPFactory(registry.HTTPConfig{
		URL:     endpoint,
		Headers: map[string]string{authHeader: token},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	// A call guarantees at least one request carried the header end to end.
	args, _ := json.Marshal(greetIn{Name: "portcullis"})
	if _, err := ds.CallTool(ctx, "greet", args); err != nil {
		t.Fatalf("CallTool greet: %v", err)
	}
	if got := lastHeader(authHeader); got != token {
		t.Errorf("downstream saw Authorization %q, want injected %q", got, token)
	}
}

// markerRoundTripper sets a fixed marker header then delegates. It stands in for
// a caller-supplied transport so the test can prove the configured HTTPClient's
// own transport is preserved when the factory wraps it for header injection.
type markerRoundTripper struct {
	name, value string
	base        http.RoundTripper
}

func (m *markerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(m.name, m.value)
	return m.base.RoundTrip(req)
}

// TestHTTPFactoryUsesProvidedClient proves a caller-supplied HTTPClient is
// honored: its transport still runs (its marker header reaches the downstream)
// and the injected connection-level header is layered on top, not in place of
// it.
func TestHTTPFactoryUsesProvidedClient(t *testing.T) {
	endpoint, lastHeader := newFakeHTTPDownstream(t)
	const (
		markerName = "X-Base-Marker"
		markerVal  = "base-transport-ran"
		token      = "Bearer layered-on-top"
	)
	client := &http.Client{Transport: &markerRoundTripper{name: markerName, value: markerVal, base: http.DefaultTransport}}
	f := registry.NewHTTPFactory(registry.HTTPConfig{
		URL:        endpoint,
		Headers:    map[string]string{authHeader: token},
		HTTPClient: client,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	args, _ := json.Marshal(greetIn{Name: "portcullis"})
	if _, err := ds.CallTool(ctx, "greet", args); err != nil {
		t.Fatalf("CallTool greet: %v", err)
	}
	if got := lastHeader(markerName); got != markerVal {
		t.Errorf("downstream saw %s %q, want %q (provided client's transport not used)", markerName, got, markerVal)
	}
	if got := lastHeader(authHeader); got != token {
		t.Errorf("downstream saw Authorization %q, want injected %q", got, token)
	}
}

func TestHTTPFactoryPing(t *testing.T) {
	endpoint, _ := newFakeHTTPDownstream(t)
	f := registry.NewHTTPFactory(registry.HTTPConfig{URL: endpoint})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	if err := ds.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestHTTPFactoryUnknownTool(t *testing.T) {
	endpoint, _ := newFakeHTTPDownstream(t)
	f := registry.NewHTTPFactory(registry.HTTPConfig{URL: endpoint})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	if _, err := ds.CallTool(ctx, "does_not_exist", json.RawMessage(`{}`)); err == nil {
		t.Error("CallTool on an unknown tool returned no error")
	}
}

func TestHTTPSessionCloseIsIdempotent(t *testing.T) {
	endpoint, _ := newFakeHTTPDownstream(t)
	f := registry.NewHTTPFactory(registry.HTTPConfig{URL: endpoint})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("second Close = %v, want nil (idempotent)", err)
	}
}

func TestHTTPFactoryRejectsEmptyURL(t *testing.T) {
	f := registry.NewHTTPFactory(registry.HTTPConfig{URL: ""})
	if _, err := f.New(context.Background()); err == nil {
		t.Error("New with empty URL returned no error")
	}
}

func TestHTTPFactoryConnectFailure(t *testing.T) {
	// A server that 404s every request cannot complete the MCP handshake, so
	// Connect must fail rather than hang.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no MCP here", http.StatusNotFound)
	}))
	defer srv.Close()

	f := registry.NewHTTPFactory(registry.HTTPConfig{URL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.New(ctx); err == nil {
		t.Error("New against a non-MCP endpoint returned no error")
	}
}
