//go:build unix

// This file implements the stdio (subprocess) downstream session and its
// factory. It is unix-only: orphan-proofing relies on POSIX process groups and
// signals (ADR-0006). The transport-agnostic DownstreamSession abstraction lives
// in session.go and is available on all platforms; remote-HTTP downstreams do
// not need this file.

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// stdioSession is a DownstreamSession backed by a local subprocess speaking MCP
// over stdin/stdout. It owns both the SDK client session and the underlying
// command so it can both shut the protocol down cleanly and guarantee the
// process group is reaped (ADR-0006).
type stdioSession struct {
	cs  *mcp.ClientSession
	cmd *exec.Cmd

	closeOnce sync.Once
	closeErr  error
}

// Compile-time assertion that stdioSession satisfies DownstreamSession.
var _ DownstreamSession = (*stdioSession)(nil)

// CallTool invokes the named downstream tool. The raw JSON arguments are passed
// through to the SDK (which marshals json.RawMessage verbatim), and the result
// is marshaled whole to JSON so the outbound redactor can scan every byte the
// downstream returned, including error content.
func (s *stdioSession) CallTool(ctx context.Context, tool string, args json.RawMessage) (*domain.Result, error) {
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
func (s *stdioSession) ListTools(ctx context.Context, cursor string) ([]ToolInfo, string, error) {
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
func (s *stdioSession) Ping(ctx context.Context) error {
	if err := s.cs.Ping(ctx, nil); err != nil {
		return fmt.Errorf("ping downstream: %w", err)
	}
	return nil
}

// Pid reports the subprocess id. It supports lifecycle assertions and is not
// part of DownstreamSession; the dispatcher does not need it.
func (s *stdioSession) Pid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Close shuts the downstream down with defense in depth (ADR-0006). It first
// closes the SDK session, which closes the child's stdin (the MCP shutdown
// signal), waits TerminateDuration, then SIGTERMs and waits on the process. As a
// belt-and-suspenders backstop it then SIGKILLs the whole process group in case
// the server ignored stdin closure, so no descendant is left orphaned. A
// "no such process" from the group kill is expected when the process already
// exited and is not reported.
//
// Close is idempotent: the teardown runs once. This matters because the group
// kill targets the negative PID, and a second kill after the process has been
// reaped could signal an unrelated process group that recycled the PID.
func (s *stdioSession) Close() error {
	s.closeOnce.Do(func() {
		var firstErr error
		if s.cs != nil {
			if err := s.cs.Close(); err != nil {
				firstErr = fmt.Errorf("close downstream session: %w", err)
			}
		}
		if s.cmd != nil && s.cmd.Process != nil {
			if err := killProcessGroup(s.cmd.Process.Pid); err != nil && !errors.Is(err, os.ErrProcessDone) && !isNoSuchProcess(err) {
				if firstErr == nil {
					firstErr = fmt.Errorf("kill downstream process group: %w", err)
				}
			}
		}
		s.closeErr = firstErr
	})
	return s.closeErr
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

// StdioConfig configures a downstream subprocess transport. Env is the full set
// of extra environment variables handed to the child, including any resolved
// connection-level secrets — the composition root resolves each declared
// secret's env var to a value and passes it here (ADR-0012).
type StdioConfig struct {
	// Command is the downstream's argv; Command[0] is the executable.
	Command []string
	// Env are extra environment variables for the child, layered over the
	// gateway's own environment so PATH and friends still resolve. Includes
	// resolved connection-level secrets.
	Env map[string]string
	// TerminateDuration is how long Close waits after closing stdin before
	// escalating to SIGTERM. Zero uses the SDK default.
	TerminateDuration time.Duration
}

// StdioFactory creates stdio DownstreamSessions and satisfies the pool's Factory
// interface, so a bounded pool of downstream subprocesses is just
// NewPool(NewStdioFactory(cfg), poolCfg).
type StdioFactory struct {
	cfg StdioConfig
}

// Compile-time assertion that StdioFactory satisfies Factory.
var _ Factory = (*StdioFactory)(nil)

// NewStdioFactory returns a factory that spawns the configured downstream
// command for each new session.
func NewStdioFactory(cfg StdioConfig) *StdioFactory {
	return &StdioFactory{cfg: cfg}
}

// New spawns the downstream subprocess, performs the MCP handshake, and returns
// a ready DownstreamSession. The child is started in its own process group with
// platform-appropriate death signaling (ADR-0006), and its environment is the
// gateway's environment plus the configured Env (so injected secrets reach the
// child while inherited variables like PATH still resolve). If the handshake
// fails the partially started transport is torn down by the SDK before New
// returns the error.
func (f *StdioFactory) New(ctx context.Context) (Session, error) {
	if len(f.cfg.Command) == 0 {
		return nil, errors.New("stdio factory: empty command")
	}

	//nolint:gosec // G204: command is operator-configured downstream, not attacker input.
	cmd := exec.CommandContext(ctx, f.cfg.Command[0], f.cfg.Command[1:]...)
	cmd.Env = childEnv(f.cfg.Env)
	cmd.SysProcAttr = newSysProcAttr()

	transport := &mcp.CommandTransport{Command: cmd, TerminateDuration: f.cfg.TerminateDuration}
	client := mcp.NewClient(&mcp.Implementation{Name: "portcullis", Version: "dev"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		// The SDK tears the transport down on most handshake failures, but at
		// least one branch (an unsupported protocol version) returns without
		// closing. Best-effort reap the process group so a started child is never
		// orphaned holding injected credentials.
		if cmd.Process != nil {
			_ = killProcessGroup(cmd.Process.Pid)
		}
		return nil, fmt.Errorf("connect downstream %q: %w", f.cfg.Command[0], err)
	}
	return &stdioSession{cs: cs, cmd: cmd}, nil
}

// childEnv layers the configured extra variables over a copy of the gateway's
// own environment. Inherited variables (PATH, etc.) remain so the command
// resolves and behaves normally; configured entries are appended last and, with
// the conventional last-wins semantics of exec, override any inherited value of
// the same key.
func childEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
