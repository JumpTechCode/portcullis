# 0006 — Orphan-proof subprocess lifecycle

- Status: Accepted
- Date: 2026-06-16

## Context

stdio downstreams run as child subprocesses of the gateway. If the gateway dies
abnormally — SIGKILL, OOM-kill, an unrecovered panic — naive child management
leaks orphaned processes that keep holding resources and injected credentials.
Relying solely on the parent sending a termination signal fails in exactly the
case that matters: when the parent dies without getting to run any cleanup code.

A robust design must ensure children exit even when the parent cannot execute
shutdown logic at all.

## Decision

Subprocess teardown uses defense in depth:

- Children are spawned in their own process group
  (`SysProcAttr{Setpgid: true}`); termination targets the **negative PID** (the
  whole group), so a child's own descendants are not left orphaned.
- The **primary backstop is stdin closure.** The MCP stdio transport specifies
  closing the child's stdin as the shutdown signal, and the OS closes that pipe
  automatically when the parent exits for any reason. A compliant server
  therefore exits even if the parent was SIGKILLed or OOM-killed.
- On Linux, `Pdeathsig: SIGKILL` is set as belt-and-suspenders: the kernel
  delivers the signal when the parent dies, independently of any parent code.

Graceful shutdown follows: stdin-close → SIGTERM → grace window → SIGKILL of the
process group.

## Consequences

- stdio children do not survive the gateway as orphans, even on SIGKILL/OOM, so
  credentials and resources are released promptly.
- The design depends on the MCP stdio spec's stdin-closure contract for the
  primary guarantee; a non-compliant server that ignores stdin closure is still
  caught by the process-group kill and, on Linux, by `Pdeathsig`.
- `Pdeathsig` is Linux-specific. On other platforms the stdin-closure and
  group-kill paths carry shutdown; this is a documented platform difference, not
  a regression in behavior.
