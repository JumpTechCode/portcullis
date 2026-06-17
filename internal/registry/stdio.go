//go:build unix

// This file implements the stdio (subprocess) downstream session and its
// factory. It is unix-only: orphan-proofing relies on POSIX process groups and
// signals (ADR-0006). The transport-agnostic DownstreamSession abstraction lives
// in session.go and is available on all platforms; remote-HTTP downstreams do
// not need this file.

package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioSession is a DownstreamSession backed by a local subprocess speaking MCP
// over stdin/stdout. It embeds the shared sdkSession for the protocol behavior
// and adds ownership of the underlying command, so it can both shut the protocol
// down cleanly and guarantee the process group is reaped (ADR-0006).
type stdioSession struct {
	sdkSession
	cmd *exec.Cmd

	closeOnce onceCloser
}

// Compile-time assertion that stdioSession satisfies DownstreamSession.
var _ DownstreamSession = (*stdioSession)(nil)

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
	return s.closeOnce.do(func() error {
		firstErr := s.closeSession()
		if s.cmd != nil && s.cmd.Process != nil {
			if err := killProcessGroup(s.cmd.Process.Pid); err != nil && !errors.Is(err, os.ErrProcessDone) && !isNoSuchProcess(err) {
				if firstErr == nil {
					firstErr = fmt.Errorf("kill downstream process group: %w", err)
				}
			}
		}
		return firstErr
	})
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

	// The process lifetime is owned by Close (stdin-close → SIGTERM → SIGKILL of
	// the process group, plus Pdeathsig), not by ctx: a pooled session outlives
	// the request that created it, so binding the child to ctx via CommandContext
	// would kill it the moment that request's context was cancelled. ctx still
	// bounds the handshake below through client.Connect.
	//nolint:gosec // G204: command is operator-configured downstream, not attacker input.
	cmd := exec.Command(f.cfg.Command[0], f.cfg.Command[1:]...)
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
	return &stdioSession{sdkSession: sdkSession{cs: cs}, cmd: cmd}, nil
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
