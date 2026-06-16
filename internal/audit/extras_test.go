package audit_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/audit"
	"github.com/JumpTechCode/portcullis/internal/domain"
)

func TestNopSyncWritesAndSyncsHarmlessly(t *testing.T) {
	var buf bytes.Buffer
	w := audit.New(audit.NopSync(&buf), testConfig())
	if err := w.Record(successRecord("c", "t")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"tool":"t"`) {
		t.Errorf("record not written through NopSync: %q", buf.String())
	}
}

func TestNewWithZeroConfigUsesDefaults(t *testing.T) {
	var buf bytes.Buffer
	w := audit.New(audit.NopSync(&buf), audit.Config{}) // all zero -> defaults
	if err := w.Record(successRecord("c", "t")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"tool":"t"`) {
		t.Error("default-configured writer did not write the record")
	}
}

func TestGetReturnsClearedRecord(t *testing.T) {
	w := audit.New(audit.NopSync(&bytes.Buffer{}), testConfig())
	defer func() { _ = w.Close() }()
	r := w.Get()
	if r == nil {
		t.Fatal("Get returned nil")
	}
	if *r != (domain.AuditRecord{}) {
		t.Errorf("Get returned a non-cleared record: %+v", *r)
	}
}

func TestOverflowBlockWritesEverything(t *testing.T) {
	var buf bytes.Buffer
	cfg := testConfig()
	cfg.Overflow = audit.OverflowBlock
	w := audit.New(audit.NopSync(&buf), cfg)
	for range 20 {
		if err := w.Record(successRecord("c", "t")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := w.Stats().Shed; got != 0 {
		t.Errorf("block policy shed %d records, want 0", got)
	}
	if got := strings.Count(buf.String(), "\n"); got != 20 {
		t.Errorf("wrote %d lines, want 20", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	w := audit.New(audit.NopSync(&bytes.Buffer{}), testConfig())
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil", err)
	}
}

func TestTimestampRendered(t *testing.T) {
	var buf bytes.Buffer
	w := audit.New(audit.NopSync(&buf), testConfig())
	rec := successRecord("c", "t")
	rec.Timestamp = time.Date(2026, 6, 16, 4, 30, 0, 0, time.UTC)
	if err := w.Record(rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"timestamp":"2026-06-16T04:30:00Z"`) {
		t.Errorf("timestamp not rendered: %q", buf.String())
	}
}

type erroringSink struct{}

func (erroringSink) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func (erroringSink) Sync() error               { return errors.New("disk gone") }

func TestSinkErrorsCounted(t *testing.T) {
	w := audit.New(erroringSink{}, testConfig())
	if err := w.Record(successRecord("c", "t")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Stats().SinkErrors == 0 {
		t.Error("a failing sink did not increment SinkErrors")
	}
}
