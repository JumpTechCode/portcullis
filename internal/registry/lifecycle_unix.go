//go:build unix

package registry

import (
	"errors"
	"syscall"
)

// killProcessGroup SIGKILLs the entire process group led by pid. Because each
// downstream is spawned as its own group leader (Setpgid in newSysProcAttr),
// the group id equals the leader's pid, so signaling the negative pid reaches
// the child and every descendant it spawned — leaving no orphans (ADR-0006).
//
// stdio downstreams are unix-only: this relies on POSIX process groups. The
// gateway runs on Linux in CI and darwin in development, both unix; there is no
// Windows build of this path.
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// isNoSuchProcess reports whether err is the "no such process" (ESRCH) error a
// signal returns once the target has already exited and been reaped. Close uses
// it to treat an already-gone process group as success rather than a failure.
func isNoSuchProcess(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}
