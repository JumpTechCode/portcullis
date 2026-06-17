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
	"context"
	"errors"
	"io/fs"
	"log"
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

// Watcher polls a file and calls onChange whenever the file's modification time
// or size changes after Run begins.
type Watcher struct {
	path     string
	interval time.Duration
	onChange func()

	// statFn and newTicker are seams for deterministic tests; production wiring
	// uses the real filesystem and a real time.Ticker (set by New).
	statFn    func(string) (fileState, error)
	newTicker func(time.Duration) (<-chan time.Time, func())
}

// New returns a Watcher that polls path every interval and calls onChange after
// each detected change. interval must be positive; the caller (cmd) validates
// it before constructing the Watcher.
func New(path string, interval time.Duration, onChange func()) *Watcher {
	return &Watcher{
		path:     path,
		interval: interval,
		onChange: onChange,
		statFn:   stat,
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
	}
}

// Run polls the file until ctx is cancelled, then returns. It captures a
// baseline at start so the first poll never fires a spurious change; thereafter
// it fires onChange once per detected change. A transient not-exist is ignored
// (the baseline is held until the file reappears); any other stat error is
// logged once per error episode and never fires onChange or stops the loop.
//
// onChange runs synchronously on the polling goroutine, so it must not block: a
// slow callback stalls polling and delays observing ctx cancellation. Callers
// are expected to hand off (e.g. a non-blocking send) rather than do work inline.
func (w *Watcher) Run(ctx context.Context) {
	ticks, stop := w.newTicker(w.interval)
	defer stop()

	baseline, _ := w.statFn(w.path) // a stat error here surfaces on the first tick
	inError := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			cur, err := w.statFn(w.path)
			if err != nil {
				if !inError {
					log.Printf("portcullis: cannot stat watched config %s: %v", w.path, err)
					inError = true
				}
				continue
			}
			inError = false
			if changed(baseline, cur) {
				baseline = cur
				w.onChange()
			}
		}
	}
}
