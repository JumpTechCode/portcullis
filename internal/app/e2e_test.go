//go:build unix

package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/app"
	"github.com/JumpTechCode/portcullis/internal/config"
)

// fakeMCPEnv gates the helper-process fake downstream MCP server. The gateway
// inherits it from this test process at spawn time, so only the spawned child
// (running solely TestHelperProcess) starts the server; this process's own
// TestHelperProcess run sees the var unset and returns immediately.
const fakeMCPEnv = "PORTCULLIS_APP_E2E_FAKE_MCP"

// secretEnvSource holds the connection-level secret the gateway injects into the
// downstream and registers with the redactor; the downstream echoes it back so
// the test can prove outbound redaction across the whole stack.
const secretEnvSource = "PORTCULLIS_APP_E2E_SECRET"

// injectedSecretValue is echoed by the downstream and must be redacted before it
// reaches the client.
const injectedSecretValue = "sup3r-s3cret-e2e-value"

// TestHelperProcess is not a real test: when fakeMCPEnv is set it serves an
// in-repo MCP server over stdio and exits, so the gateway has a real downstream
// to spawn without any external command (the os/exec helper-process pattern).
func TestHelperProcess(t *testing.T) {
	if os.Getenv(fakeMCPEnv) != "1" {
		return
	}
	runFakeDownstream()
	os.Exit(0)
}

type greetIn struct {
	Name string `json:"name"`
}

type echoEnvIn struct {
	Var string `json:"var"`
}

// runFakeDownstream serves three tools: greet (a greeting), echo_env (the value
// of a named env var, used to surface the injected secret), and secret_tool
// (which policy will deny, so the client must never see or reach it).
func runFakeDownstream() {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-downstream", Version: "test"}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "greet", Description: "Returns a greeting."},
		func(_ context.Context, _ *mcp.CallToolRequest, in greetIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello " + in.Name}}}, nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "echo_env", Description: "Echoes an environment variable."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoEnvIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv(in.Var)}}}, nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "secret_tool", Description: "Must never be exposed to a client."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ greetIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "should be unreachable"}}}, nil, nil
		})

	_ = s.Run(context.Background(), &mcp.StdioTransport{})
}

// headerRoundTripper presents the Bearer API key the gateway's request guard
// requires on every request.
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

func e2eConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Listen:         "127.0.0.1:0",
		AllowedOrigins: []string{"http://localhost"},
		Clients:        []config.Client{{ID: "e2e", APIKeyEnv: "PORTCULLIS_APP_E2E_KEY"}},
		Downstreams: []config.Downstream{{
			Name:        "github",
			Transport:   config.TransportStdio,
			Command:     []string{os.Args[0], "-test.run=TestHelperProcess", "-test.v=false"},
			SessionMode: config.SessionPerClient,
			Secrets:     map[string]config.SecretRef{"E2E_SECRET": {Env: secretEnvSource}},
			Pool: config.Pool{
				Max:            4,
				AcquireTimeout: config.Duration(5 * time.Second),
				IdleTTL:        config.Duration(90 * time.Second),
			},
			Health: config.Health{Interval: config.Duration(30 * time.Second)},
		}},
		Policy: config.Policy{
			Default: config.DefaultDeny,
			Rules:   []config.Rule{{Client: "e2e", Allow: []string{"github__greet", "github__echo_env"}}},
		},
		Redaction: config.Redaction{MaxResultBytes: 1 << 20},
		Audit: config.Audit{
			Sink:                 "file:" + t.TempDir() + "/audit.jsonl",
			Buffer:               100,
			HighWaterMark:        0.85,
			Overflow:             config.OverflowShed,
			Flush:                config.Flush{MaxRecords: 100, MaxInterval: config.Duration(50 * time.Millisecond)},
			FsyncInterval:        config.Duration(time.Second),
			SecurityBlockTimeout: config.Duration(250 * time.Millisecond),
		},
	}
}

func TestE2EGatewayProxiesThroughFullStack(t *testing.T) {
	t.Setenv(fakeMCPEnv, "1")
	t.Setenv("PORTCULLIS_APP_E2E_KEY", "e2e-api-key")
	t.Setenv(secretEnvSource, injectedSecretValue)

	gateway, err := app.Build(e2eConfig(t), "e2e")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = gateway.Shutdown() })

	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpClient := &http.Client{Transport: headerRoundTripper{
		base:    http.DefaultTransport,
		headers: map[string]string{"Authorization": "Bearer e2e-api-key"},
	}}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	t.Run("lists only the policy-allowed, namespaced tools", func(t *testing.T) {
		tools, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		got := make([]string, len(tools.Tools))
		for i, tool := range tools.Tools {
			got[i] = tool.Name
		}
		sort.Strings(got)
		want := []string{"github__echo_env", "github__greet"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("listed tools = %v, want %v (secret_tool must be filtered out)", got, want)
		}
	})

	t.Run("proxies an allowed call to the downstream", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "github__greet", Arguments: map[string]any{"name": "world"}})
		if err != nil {
			t.Fatalf("CallTool greet: %v", err)
		}
		if text := firstText(t, res); text != "hello world" {
			t.Errorf("greet result = %q, want %q", text, "hello world")
		}
	})

	t.Run("redacts an injected secret the downstream echoes", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "github__echo_env", Arguments: map[string]any{"var": "E2E_SECRET"}})
		if err != nil {
			t.Fatalf("CallTool echo_env: %v", err)
		}
		text := firstText(t, res)
		if text == injectedSecretValue {
			t.Error("injected secret reached the client unredacted")
		}
		if text != "[REDACTED]" {
			t.Errorf("echo_env result = %q, want %q", text, "[REDACTED]")
		}
	})

	t.Run("denies a tool outside the client's allowlist", func(t *testing.T) {
		if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "github__secret_tool", Arguments: map[string]any{"name": "x"}}); err == nil {
			t.Error("a denied tool returned no error")
		}
	})
}

// firstText extracts the text of a CallToolResult's first content block.
func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("result had no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content block is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}
