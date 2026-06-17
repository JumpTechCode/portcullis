// Package watch polls a single file and reports content changes via a callback.
//
// It is an operator convenience that lets the gateway pick up an in-place edit
// to its configuration file without an explicit SIGHUP. The package is a leaf
// of the import graph: it imports only the standard library and knows nothing
// about configuration or the gateway. It detects that a file changed and
// invokes a caller-supplied callback, leaving the meaning of a reload to the
// caller. Change is judged by the file's modification time and size, which
// covers both in-place edits and the atomic write-temp-then-rename-over pattern
// (the replacement file carries a fresh modification time).
package watch

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

// fileState is the identity of the watched file used for change detection. A
// file that does not exist — including momentarily, during an atomic
// write-temp-then-rename — has ok == false and is treated as "no new
// information", not as a change.
type fileState struct {
	ok      bool
	modTime time.Time
	size    int64
}

// stat reads the current state of the file. A not-exist error yields a
// zero-value (ok == false) state and a nil error, so a transient disappearance
// during an atomic rename is not mistaken for a change. Any other error is
// returned so the caller can log it.
func stat(path string) (fileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileState{}, nil
		}
		return fileState{}, err
	}
	return fileState{ok: true, modTime: info.ModTime(), size: info.Size()}, nil
}

// changed reports whether b is a different file identity than a. A file that is
// absent in b is never a change (the watcher waits for it to reappear); a file
// that newly appears, or whose modification time or size differs from the last
// known present state, is a change.
func changed(a, b fileState) bool {
	if !b.ok {
		return false
	}
	if !a.ok {
		return true
	}
	return !a.modTime.Equal(b.modTime) || a.size != b.size
}
