//go:build linux

package registry

import "syscall"

// newSysProcAttr configures a downstream subprocess for orphan-proof teardown
// on Linux (ADR-0006). Setpgid puts the child in its own process group so the
// gateway can signal the whole group via the negative PID, and Pdeathsig:
// SIGKILL has the kernel kill the child the instant the gateway dies — even on
// SIGKILL or OOM, when no gateway cleanup code runs.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
