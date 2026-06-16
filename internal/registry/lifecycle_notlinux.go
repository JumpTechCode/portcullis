//go:build unix && !linux

package registry

import "syscall"

// newSysProcAttr configures a downstream subprocess for orphan-proof teardown on
// non-Linux unix systems, such as the darwin development target (ADR-0006).
// Setpgid puts the child in its own process group so the gateway can signal the
// whole group via the negative PID. Pdeathsig is Linux-only; on these platforms
// the stdin-closure shutdown contract and the explicit process-group kill carry
// teardown, which is a documented platform difference rather than a regression.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
