//go:build unix

package registry_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/registry"
)

// fakeMCPEnv gates the helper-process fake downstream MCP server.
const fakeMCPEnv = "PORTCULLIS_FAKE_MCP"

// injectedSecretEnv is the connection-level secret the factory tests inject and
// the fake server echoes back, proving env injection reached the child.
const injectedSecretEnv = "INJECTED_SECRET"

// TestHelperProcess is not a real test: when PORTCULLIS_FAKE_MCP=1 it runs an
// in-repo MCP server over stdio and exits. The factory tests spawn this same
// test binary with that env var set, so the gateway has a real downstream MCP
// server to connect to without depending on any external command. See the
// standard library's exec helper-process pattern (os/exec TestHelperProcess).
func TestHelperProcess(t *testing.T) {
	if os.Getenv(fakeMCPEnv) != "1" {
		return
	}
	runFakeMCPServer()
	// runFakeMCPServer blocks until the client disconnects, then we exit so the
	// helper process does not fall through into the rest of the test binary.
	os.Exit(0)
}

// echoEnvIn is the input to the echo_env tool.
type echoEnvIn struct {
	Var string `json:"var"`
}

// runFakeMCPServer serves a minimal MCP server over stdin/stdout exposing two
// tools: greet (returns a greeting) and echo_env (returns the value of a named
// environment variable, used to prove connection-level env injection).
func runFakeMCPServer() {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-downstream", Version: "test"}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "greet", Description: "Returns a greeting."},
		func(_ context.Context, _ *mcp.CallToolRequest, in greetIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "hello " + in.Name}},
			}, nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "echo_env", Description: "Returns the value of an environment variable."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoEnvIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv(in.Var)}},
			}, nil, nil
		})

	// Run blocks until the client closes stdin (the orphan-proof shutdown path).
	_ = s.Run(context.Background(), &mcp.StdioTransport{})
}

// helperFactory builds a StdioFactory pointed at this test binary running as the
// fake MCP server, with the given secret injected as a connection-level env var.
func helperFactory(t *testing.T, secret string) *registry.StdioFactory {
	t.Helper()
	return registry.NewStdioFactory(registry.StdioConfig{
		Command: []string{os.Args[0], "-test.run=TestHelperProcess", "-test.v=false"},
		Env: map[string]string{
			fakeMCPEnv:        "1",
			injectedSecretEnv: secret,
		},
		TerminateDuration: 2 * time.Second,
	})
}

func TestStdioSessionSurvivesCreatingContextCancel(t *testing.T) {
	f := helperFactory(t, "unused")

	// A pooled session is created during one request and reused across later
	// ones, so cancelling the context that created it must not tear down the
	// subprocess: its lifetime is owned by Close, not the creating context.
	ctx, cancel := context.WithCancel(context.Background())
	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	cancel()

	ds, ok := sess.(registry.DownstreamSession)
	if !ok {
		t.Fatal("stdio session is not a DownstreamSession")
	}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := ds.Ping(pingCtx); err != nil {
		t.Fatalf("session did not survive its creating context being cancelled: %v", err)
	}
}

func TestStdioSessionCloseIsIdempotent(t *testing.T) {
	f := helperFactory(t, "unused")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// A second Close must be a safe no-op: it must not re-signal the now-reaped
	// (possibly recycled) process group, and must not error or panic.
	if err := sess.Close(); err != nil {
		t.Errorf("second Close = %v, want nil (idempotent)", err)
	}
}

func TestStdioFactoryConnectsAndListsTools(t *testing.T) {
	f := helperFactory(t, "unused")
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
	for _, want := range []string{"greet", "echo_env"} {
		if !slices.Contains(names, want) {
			t.Errorf("tool %q missing from ListTools result %v", want, names)
		}
	}
	// The schema must round-trip as raw JSON for the composition root to namespace.
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

func TestStdioFactoryCallToolGreet(t *testing.T) {
	f := helperFactory(t, "unused")
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

// TestStdioFactoryEnvInjection is the marquee env-injection proof: a secret is
// passed only as a connection-level env var, and the child echoes it back.
func TestStdioFactoryEnvInjection(t *testing.T) {
	const secret = "s3cr3t-from-gateway"
	f := helperFactory(t, secret)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	args, _ := json.Marshal(echoEnvIn{Var: injectedSecretEnv})
	res, err := ds.CallTool(ctx, "echo_env", args)
	if err != nil {
		t.Fatalf("CallTool echo_env: %v", err)
	}
	if res.IsError {
		t.Fatalf("echo_env reported a tool error: %s", res.Content)
	}
	if !containsString(t, res.Content, secret) {
		t.Errorf("echo_env content %s did not return the injected secret %q", res.Content, secret)
	}
}

func TestStdioFactoryPing(t *testing.T) {
	f := helperFactory(t, "unused")
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

// TestStdioFactoryCallToolError verifies a downstream tool error surfaces as a
// protocol-level error or IsError result rather than a panic: calling a tool
// that does not exist must not crash the session.
func TestStdioFactoryUnknownTool(t *testing.T) {
	f := helperFactory(t, "unused")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)
	defer func() { _ = ds.Close() }()

	_, err = ds.CallTool(ctx, "does_not_exist", json.RawMessage(`{}`))
	if err == nil {
		t.Error("CallTool on an unknown tool returned no error")
	}
}

// TestStdioFactoryTeardownReapsProcess is the orphan-proofing check. After
// Close the child process must be gone: stdin closure (and on Linux Pdeathsig)
// plus the explicit process-group kill in Close guarantee it. We verify by
// signal-0 probing the pid, which fails once the process has been reaped.
func TestStdioFactoryTeardownReapsProcess(t *testing.T) {
	f := helperFactory(t, "unused")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := f.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ds := downstream(t, sess)

	// Pid is exposed by the concrete stdio session (not the DownstreamSession
	// interface) for lifecycle assertions; reach it via a narrow assertion.
	pidder, ok := sess.(interface{ Pid() int })
	if !ok {
		t.Fatalf("session %T does not expose Pid()", sess)
	}
	pid := pidder.Pid()
	if pid <= 0 {
		t.Fatalf("session reported non-positive pid %d", pid)
	}
	// Sanity: the process is alive before Close (signal 0 probes liveness).
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child pid %d not alive before Close: %v", pid, err)
	}

	if err := ds.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Poll until the process group has been reaped. signal 0 to a dead pid
	// returns ESRCH (no such process); a zombie not yet waited on can linger,
	// but CommandTransport.Close waits on the process, so it should be reaped.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			break // ESRCH: gone
		}
		if time.Now().After(deadline) {
			t.Fatalf("child pid %d still alive 5s after Close (orphaned)", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStdioFactoryRejectsEmptyCommand(t *testing.T) {
	f := registry.NewStdioFactory(registry.StdioConfig{Command: nil})
	if _, err := f.New(context.Background()); err == nil {
		t.Error("New with empty command returned no error")
	}
}

func TestStdioFactoryConnectFailure(t *testing.T) {
	// A command that exits immediately cannot complete the MCP handshake, so
	// Connect must fail rather than hang. /bin/false exits non-zero at once.
	f := registry.NewStdioFactory(registry.StdioConfig{
		Command:           []string{"/bin/false"},
		TerminateDuration: time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.New(ctx); err == nil {
		t.Error("New against a non-MCP command returned no error")
	}
}
