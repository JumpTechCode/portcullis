package audit

import (
	"sync"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

type countingSink struct {
	mu     sync.Mutex
	writes int
	syncs  int
}

func (s *countingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	return len(p), nil
}

func (s *countingSink) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncs++
	return nil
}

func (s *countingSink) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func (s *countingSink) syncCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncs
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met before deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWithDefaults(t *testing.T) {
	d := withDefaults(Config{})
	if d.Buffer != defaultBuffer ||
		d.HighWaterMark != defaultHighWaterMark ||
		d.FlushMaxRecords != defaultFlushMaxRecords ||
		d.FlushMaxInterval != defaultFlushMaxInterval ||
		d.FsyncInterval != defaultFsyncInterval ||
		d.Overflow != OverflowShed ||
		d.SecurityBlockTimeout != defaultSecurityBlockTimeout {
		t.Errorf("defaults not applied: %+v", d)
	}
	if got := withDefaults(Config{HighWaterMark: 1.5}).HighWaterMark; got != defaultHighWaterMark {
		t.Errorf("out-of-range high-water mark = %v, want default", got)
	}
	if got := withDefaults(Config{HighWaterMark: 0.5}).HighWaterMark; got != 0.5 {
		t.Errorf("valid high-water mark overwritten = %v", got)
	}
}

// The flush ticker pushes buffered records to the OS page cache; the fsync ticker
// group-commits to disk. Both triggers are driven deterministically here.
func TestDurabilityFlushAndFsyncTriggers(t *testing.T) {
	flushC := make(chan time.Time)
	fsyncC := make(chan time.Time)
	sink := &countingSink{}
	w := newWithTickers(sink, Config{Buffer: 16, FlushMaxRecords: 1000, Overflow: OverflowShed}, flushC, fsyncC)
	defer func() { _ = w.Close() }()

	if err := w.Record(&domain.AuditRecord{ClientID: "c", ToolName: "t", Allowed: true}); err != nil {
		t.Fatal(err)
	}

	// Drive the flush ticker until the record reaches the sink (the writer may
	// receive a tick before it has consumed the record; keep nudging).
	waitFor(t, func() bool {
		select {
		case flushC <- time.Now():
		default:
		}
		return sink.writeCount() > 0
	})

	// Drive the fsync ticker until a Sync occurs.
	waitFor(t, func() bool {
		select {
		case fsyncC <- time.Now():
		default:
		}
		return sink.syncCount() > 0
	})
}
