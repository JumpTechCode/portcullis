package watch

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStatExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := stat(path)
	if err != nil {
		t.Fatalf("stat returned error: %v", err)
	}
	if !st.ok {
		t.Fatal("stat reported an existing file as absent")
	}
	if st.size != 5 {
		t.Errorf("size = %d, want 5", st.size)
	}
}

func TestStatMissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")
	st, err := stat(path)
	if err != nil {
		t.Fatalf("stat on a missing file returned error %v, want nil", err)
	}
	if st.ok {
		t.Error("stat reported a missing file as present")
	}
}

func TestStatNonNotExistErrorPropagates(t *testing.T) {
	// A regular file used as a path component yields ENOTDIR, not ErrNotExist,
	// so stat must surface it rather than report the file as absent.
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := stat(filepath.Join(file, "child"))
	if err == nil {
		t.Fatal("stat through a non-directory parent returned nil error, want propagated error")
	}
	if st.ok {
		t.Error("stat reported ok == true alongside a non-nil error")
	}
}

func TestChanged(t *testing.T) {
	base := time.Unix(1000, 0)
	present := fileState{ok: true, modTime: base, size: 10}
	cases := []struct {
		name string
		a, b fileState
		want bool
	}{
		{"both absent", fileState{}, fileState{}, false},
		{"unchanged", present, present, false},
		{"newer modtime", present, fileState{ok: true, modTime: base.Add(time.Second), size: 10}, true},
		{"different size", present, fileState{ok: true, modTime: base, size: 11}, true},
		{"appeared", fileState{}, present, true},
		{"vanished", present, fileState{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := changed(c.a, c.b); got != c.want {
				t.Errorf("changed(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// fakeFS is a scripted statFn that yields successive states from the script,
// repeating the last entry once exhausted. The parallel errs slice supplies the
// error returned alongside each state.
type fakeFS struct {
	mu     sync.Mutex
	states []fileState
	errs   []error
	i      int
}

func (f *fakeFS) statFn(string) (fileState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.i
	if j >= len(f.states) {
		j = len(f.states) - 1
	}
	f.i++
	return f.states[j], f.errs[j]
}

// driveWatcher runs w.Run on a manual tick channel and returns a function that
// delivers one tick and blocks until the loop has consumed it (so assertions
// are race-free), plus a cancel func.
func driveWatcher(t *testing.T, w *Watcher) (tick, cancel func()) {
	t.Helper()
	ticks := make(chan time.Time)
	w.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	ctx, cancelCtx := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	tick = func() { ticks <- time.Time{} }
	cancel = func() { cancelCtx(); <-done }
	return tick, cancel
}

func TestWatcherFiresOnChange(t *testing.T) {
	base := time.Unix(2000, 0)
	fs := &fakeFS{
		states: []fileState{
			{ok: true, modTime: base, size: 1},                  // baseline (Run start)
			{ok: true, modTime: base, size: 1},                  // tick 1: unchanged
			{ok: true, modTime: base.Add(time.Second), size: 1}, // tick 2: changed
		},
		errs: []error{nil, nil, nil},
	}
	fired := make(chan struct{}, 4)
	w := New("cfg.yaml", time.Second, func() { fired <- struct{}{} })
	w.statFn = fs.statFn
	tick, cancel := driveWatcher(t, w)
	defer cancel()

	tick() // unchanged
	select {
	case <-fired:
		t.Fatal("onChange fired on an unchanged file")
	default:
	}
	tick() // changed
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("onChange did not fire after a real change")
	}
}

func TestWatcherTransientMissingThenReappear(t *testing.T) {
	base := time.Unix(3000, 0)
	fs := &fakeFS{
		states: []fileState{
			{ok: true, modTime: base, size: 1}, // baseline
			{},                                 // tick 1: vanished (mid-rename)
			{ok: true, modTime: base.Add(time.Second), size: 2}, // tick 2: reappeared, new content
		},
		errs: []error{nil, nil, nil},
	}
	fired := make(chan struct{}, 4)
	w := New("cfg.yaml", time.Second, func() { fired <- struct{}{} })
	w.statFn = fs.statFn
	tick, cancel := driveWatcher(t, w)
	defer cancel()

	tick() // vanished -> no fire
	select {
	case <-fired:
		t.Fatal("onChange fired on a transient disappearance")
	default:
	}
	tick() // reappeared -> fire once
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("onChange did not fire when the file reappeared")
	}
}

func TestWatcherStatErrorIsLoggedOnceAndDoesNotFire(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	boom := errors.New("boom")
	base := time.Unix(4000, 0)
	fs := &fakeFS{
		states: []fileState{
			{ok: true, modTime: base, size: 1}, // baseline
			{}, {},                             // tick 1, 2: error
			{ok: true, modTime: base.Add(time.Second), size: 1}, // tick 3: recovered + changed
		},
		errs: []error{nil, boom, boom, nil},
	}
	fired := make(chan struct{}, 4)
	w := New("cfg.yaml", time.Second, func() { fired <- struct{}{} })
	w.statFn = fs.statFn
	tick, cancel := driveWatcher(t, w)
	defer cancel()

	tick() // error
	tick() // error again (must not re-log)
	select {
	case <-fired:
		t.Fatal("onChange fired during a stat error")
	default:
	}
	tick() // recovered + changed -> fire
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("onChange did not fire after recovery")
	}
	if n := strings.Count(buf.String(), "boom"); n != 1 {
		t.Errorf("stat error logged %d times, want exactly 1", n)
	}
}

func TestWatcherStopsOnContextCancel(t *testing.T) {
	fs := &fakeFS{states: []fileState{{ok: true}}, errs: []error{nil}}
	w := New("cfg.yaml", time.Second, func() {})
	w.statFn = fs.statFn
	_, cancel := driveWatcher(t, w)
	cancel() // returns only after Run has exited; a hang here fails the test by timeout
}
