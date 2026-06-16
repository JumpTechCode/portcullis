package audit_test

import (
	"bufio"
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/audit"
	"github.com/JumpTechCode/portcullis/internal/domain"
)

// syncBuffer is a SyncWriter that records everything written and counts Sync
// calls. It is safe for the single writer goroutine plus test-side reads.
type syncBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	syncs int
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncs++
	return nil
}

func (s *syncBuffer) string() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *syncBuffer) syncCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncs
}

func (s *syncBuffer) lines() []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s.string()))
	for sc.Scan() {
		if sc.Text() != "" {
			out = append(out, sc.Text())
		}
	}
	return out
}

func testConfig() audit.Config {
	return audit.Config{
		Buffer:               64,
		HighWaterMark:        0.85,
		FlushMaxRecords:      100,
		FlushMaxInterval:     time.Hour,
		FsyncInterval:        time.Hour,
		Overflow:             audit.OverflowShed,
		SecurityBlockTimeout: 50 * time.Millisecond,
	}
}

func successRecord(client, tool string) *domain.AuditRecord {
	return &domain.AuditRecord{ClientID: client, ToolName: tool, Allowed: true, Decision: domain.ReasonAllowed}
}

func TestAuditWritesRecordsInOrder(t *testing.T) {
	sink := &syncBuffer{}
	w := audit.New(sink, testConfig())

	for range 5 {
		if err := w.Record(successRecord("c", "github__create_issue")); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := sink.lines()
	if len(lines) != 5 {
		t.Fatalf("got %d audit lines, want 5", len(lines))
	}
	for _, ln := range lines {
		if !strings.Contains(ln, `"tool":"github__create_issue"`) {
			t.Errorf("audit line missing tool field: %s", ln)
		}
		if !strings.Contains(ln, `"allowed":true`) {
			t.Errorf("audit line missing allowed field: %s", ln)
		}
	}
}

func TestAuditCloseFlushesAndFsyncs(t *testing.T) {
	sink := &syncBuffer{}
	w := audit.New(sink, testConfig())
	if err := w.Record(successRecord("c", "t")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(sink.lines()); got != 1 {
		t.Errorf("after Close got %d lines, want 1 (record not flushed)", got)
	}
	if sink.syncCount() == 0 {
		t.Error("Close did not fsync the sink")
	}
}

func TestAuditRecordAfterCloseErrors(t *testing.T) {
	w := audit.New(&syncBuffer{}, testConfig())
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Record(successRecord("c", "t")); err == nil {
		t.Error("Record after Close returned nil error")
	}
}
