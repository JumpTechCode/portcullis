package audit_test

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/audit"
	"github.com/JumpTechCode/portcullis/internal/domain"
)

// blockingSink stalls the writer goroutine inside Write until released, so a
// test can fill the buffer deterministically. entered (cap 1) signals that the
// writer is blocked; closing release unblocks all writes.
type blockingSink struct {
	entered chan struct{}
	release chan struct{}

	mu  sync.Mutex
	buf bytes.Buffer
}

func newBlockingSink() *blockingSink {
	return &blockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (b *blockingSink) Write(p []byte) (int, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *blockingSink) Sync() error { return nil }

func (b *blockingSink) lineCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Count(b.buf.Bytes(), []byte("\n"))
}

func securityRecord(client, tool string) *domain.AuditRecord {
	return &domain.AuditRecord{ClientID: client, ToolName: tool, Allowed: false, Decision: domain.ReasonDeniedDefault}
}

func TestAuditShedsSuccessAboveHighWater(t *testing.T) {
	sink := newBlockingSink()
	cfg := audit.Config{
		Buffer:               4,
		HighWaterMark:        0.5, // highWater = 2
		FlushMaxRecords:      1,   // flush every record so the first Write blocks the writer
		FlushMaxInterval:     time.Hour,
		FsyncInterval:        time.Hour,
		Overflow:             audit.OverflowShed,
		SecurityBlockTimeout: 50 * time.Millisecond,
	}
	w := audit.New(sink, cfg)

	// First record is consumed and the writer blocks inside Write.
	if err := w.Record(successRecord("c", "first")); err != nil {
		t.Fatal(err)
	}
	<-sink.entered

	// Buffer (cap 4, high-water 2) now fills: two buffered, the rest shed.
	for range 4 {
		if err := w.Record(successRecord("c", "t")); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if got := w.Stats().Shed; got != 2 {
		t.Errorf("Shed = %d, want 2 (buffer cap 4, high-water 2, 4 sent past the blocked first)", got)
	}

	close(sink.release)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// first + the two buffered = 3 written; 2 shed.
	if got := sink.lineCount(); got != 3 {
		t.Errorf("wrote %d lines, want 3", got)
	}
}

// A security record must survive even when success records would be shed: the
// high-water mark reserves headroom for it (design §5).
func TestAuditPreservesSecurityAboveHighWater(t *testing.T) {
	sink := newBlockingSink()
	cfg := audit.Config{
		Buffer:               3,
		HighWaterMark:        0.34, // highWater = 1
		FlushMaxRecords:      1,
		FlushMaxInterval:     time.Hour,
		FsyncInterval:        time.Hour,
		Overflow:             audit.OverflowShed,
		SecurityBlockTimeout: 50 * time.Millisecond,
	}
	w := audit.New(sink, cfg)

	if err := w.Record(successRecord("c", "first")); err != nil {
		t.Fatal(err)
	}
	<-sink.entered

	_ = w.Record(successRecord("c", "s1"))                        // len 0 -> buffered
	_ = w.Record(successRecord("c", "s2"))                        // len 1 >= high-water -> shed
	if err := w.Record(securityRecord("c", "deny")); err != nil { // buffered despite high-water
		t.Fatalf("security record was not accepted above the high-water mark: %v", err)
	}
	if got := w.Stats().Shed; got != 1 {
		t.Errorf("Shed = %d, want 1 (only the success record sheds)", got)
	}

	close(sink.release)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sinkAll(sink), `"tool":"deny"`) {
		t.Error("the security record was not written; it must be preserved")
	}
}

func sinkAll(b *blockingSink) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// When the sink is wedged and the buffer is full, a security record fails closed
// rather than blocking forever, so an un-auditable security decision does not
// proceed (design §5).
func TestAuditSecurityFailsClosedWhenWedged(t *testing.T) {
	sink := newBlockingSink()
	cfg := audit.Config{
		Buffer:               2,
		HighWaterMark:        0.85,
		FlushMaxRecords:      1,
		FlushMaxInterval:     time.Hour,
		FsyncInterval:        time.Hour,
		Overflow:             audit.OverflowShed,
		SecurityBlockTimeout: 20 * time.Millisecond,
	}
	w := audit.New(sink, cfg)

	if err := w.Record(securityRecord("c", "first")); err != nil {
		t.Fatal(err)
	}
	<-sink.entered

	for range 2 { // fill the buffer (cap 2)
		if err := w.Record(securityRecord("c", "t")); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}

	err := w.Record(securityRecord("c", "overflow"))
	if !errors.Is(err, audit.ErrUnrecordable) {
		t.Errorf("got %v, want ErrUnrecordable", err)
	}
	if got := w.Stats().Unrecordable; got != 1 {
		t.Errorf("Unrecordable = %d, want 1", got)
	}

	close(sink.release)
	_ = w.Close()
}
