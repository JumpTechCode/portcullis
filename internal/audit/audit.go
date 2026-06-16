// Package audit writes a structured, post-redaction record for every call
// through an asynchronous single-writer pipeline, so audit I/O never sits on the
// request hot path (design §5, §8, ADR-0010).
//
// The hot path hands a record to a bounded buffer with a non-blocking handoff. A
// dedicated writer goroutine marshals each record to a line of JSON, batches
// them through a bufio.Writer, and group-commit fsyncs to the sink. Overflow is
// tiered: high-volume success records are shed once the buffer passes a
// high-water mark, while security records (denials, redactions) take a hard path
// that blocks briefly and, if the sink is wedged, fail the request closed so an
// un-auditable security decision never proceeds.
//
// Record must not be called after Close; the composition root closes the writer
// only once request handlers have drained (graceful shutdown), so no producer
// races a Close.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// ErrClosed is returned by Record after the writer has been closed.
var ErrClosed = errors.New("audit writer closed")

// ErrUnrecordable is returned by Record when a security record cannot be
// buffered before the security block timeout; the caller must fail the request
// closed (design §5).
var ErrUnrecordable = errors.New("audit record unrecordable; sink wedged")

// Defaults applied when a Config field is left zero.
const (
	defaultBuffer               = 1024
	defaultHighWaterMark        = 0.85
	defaultFlushMaxRecords      = 100
	defaultFlushMaxInterval     = 50 * time.Millisecond
	defaultFsyncInterval        = time.Second
	defaultSecurityBlockTimeout = 250 * time.Millisecond
)

// OverflowPolicy controls what happens to a success record when the buffer is
// past its high-water mark.
type OverflowPolicy string

const (
	// OverflowShed drops success records past the high-water mark (default),
	// keeping the handoff non-blocking on the hot path.
	OverflowShed OverflowPolicy = "shed"
	// OverflowBlock makes success records wait for buffer room instead of being
	// shed. It deliberately trades the non-blocking hot-path guarantee for
	// zero success-record loss, so a wedged sink can back-pressure callers; use
	// it only when that trade is acceptable (security records are unaffected —
	// they always take the fail-closed hard path).
	OverflowBlock OverflowPolicy = "block"
)

// Config tunes the audit pipeline. Zero fields fall back to package defaults.
type Config struct {
	Buffer               int
	HighWaterMark        float64
	FlushMaxRecords      int
	FlushMaxInterval     time.Duration
	FsyncInterval        time.Duration
	Overflow             OverflowPolicy
	SecurityBlockTimeout time.Duration
}

// SyncWriter is the audit sink: a writer that can flush durably to its backing
// store. *os.File satisfies it; use NopSync to wrap stdout.
type SyncWriter interface {
	io.Writer
	Sync() error
}

type nopSync struct{ io.Writer }

func (nopSync) Sync() error { return nil }

// NopSync adapts a plain writer (e.g. os.Stdout, where fsync is not meaningful)
// to a SyncWriter with a no-op Sync.
func NopSync(w io.Writer) SyncWriter { return nopSync{w} }

// Stats is a snapshot of the writer's counters, for export to metrics.
type Stats struct {
	Written      int64
	Shed         int64
	Unrecordable int64
	SinkErrors   int64
}

// Writer is the asynchronous audit pipeline. It is safe for concurrent Record
// calls; a single goroutine owns all I/O.
type Writer struct {
	cfg       Config
	highWater int
	sink      SyncWriter
	buf       *bufio.Writer

	ch      chan *domain.AuditRecord
	done    chan struct{}
	stopped chan struct{}
	pool    sync.Pool
	closed  atomic.Bool

	stopTickers func()
	batch       int

	written      atomic.Int64
	shed         atomic.Int64
	unrecordable atomic.Int64
	sinkErrors   atomic.Int64
}

// New starts an audit writer feeding the given sink.
func New(sink SyncWriter, cfg Config) *Writer {
	w := newWriter(sink, cfg)
	flush := time.NewTicker(w.cfg.FlushMaxInterval)
	fsync := time.NewTicker(w.cfg.FsyncInterval)
	w.stopTickers = func() {
		flush.Stop()
		fsync.Stop()
	}
	go w.run(flush.C, fsync.C)
	return w
}

// newWithTickers is the test entry point: it lets a test drive the flush and
// fsync triggers deterministically instead of using real tickers.
func newWithTickers(sink SyncWriter, cfg Config, flushC, fsyncC <-chan time.Time) *Writer {
	w := newWriter(sink, cfg)
	w.stopTickers = func() {}
	go w.run(flushC, fsyncC)
	return w
}

func newWriter(sink SyncWriter, cfg Config) *Writer {
	cfg = withDefaults(cfg)
	w := &Writer{
		cfg:       cfg,
		highWater: int(cfg.HighWaterMark * float64(cfg.Buffer)),
		sink:      sink,
		buf:       bufio.NewWriter(sink),
		ch:        make(chan *domain.AuditRecord, cfg.Buffer),
		done:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	w.pool.New = func() any { return &domain.AuditRecord{} }
	return w
}

func withDefaults(cfg Config) Config {
	if cfg.Buffer <= 0 {
		cfg.Buffer = defaultBuffer
	}
	if cfg.HighWaterMark <= 0 || cfg.HighWaterMark > 1 {
		cfg.HighWaterMark = defaultHighWaterMark
	}
	if cfg.FlushMaxRecords <= 0 {
		cfg.FlushMaxRecords = defaultFlushMaxRecords
	}
	if cfg.FlushMaxInterval <= 0 {
		cfg.FlushMaxInterval = defaultFlushMaxInterval
	}
	if cfg.FsyncInterval <= 0 {
		cfg.FsyncInterval = defaultFsyncInterval
	}
	if cfg.Overflow == "" {
		cfg.Overflow = OverflowShed
	}
	if cfg.SecurityBlockTimeout <= 0 {
		cfg.SecurityBlockTimeout = defaultSecurityBlockTimeout
	}
	return cfg
}

// Get returns a cleared record from the pool for the caller to fill. After
// passing it to Record the caller must not touch it again.
func (w *Writer) Get() *domain.AuditRecord { return w.pool.Get().(*domain.AuditRecord) }

// Record hands a filled record to the pipeline. The caller must not use rec
// afterward. It returns nil once handed off (or shed), ErrClosed after Close,
// and ErrUnrecordable when a security record cannot be buffered in time.
func (w *Writer) Record(rec *domain.AuditRecord) error {
	if w.closed.Load() {
		w.release(rec)
		return ErrClosed
	}
	if isSecurity(rec) {
		return w.recordSecurity(rec)
	}
	return w.recordSuccess(rec)
}

// recordSuccess hands off a high-volume success record. Past the high-water mark
// it is shed (default) so headroom is reserved for security records; under the
// block policy it waits for room instead.
func (w *Writer) recordSuccess(rec *domain.AuditRecord) error {
	if w.cfg.Overflow == OverflowBlock {
		w.ch <- rec
		return nil
	}
	if len(w.ch) >= w.highWater {
		w.shedRec(rec)
		return nil
	}
	select {
	case w.ch <- rec:
	default:
		w.shedRec(rec)
	}
	return nil
}

// recordSecurity hands off a security record on the hard path: it may use the
// full buffer and blocks up to the security block timeout. If it still cannot be
// buffered, the call is failed closed so an un-auditable security decision does
// not proceed.
func (w *Writer) recordSecurity(rec *domain.AuditRecord) error {
	select {
	case w.ch <- rec:
		return nil
	default:
	}
	timer := time.NewTimer(w.cfg.SecurityBlockTimeout)
	defer timer.Stop()
	select {
	case w.ch <- rec:
		return nil
	case <-timer.C:
		w.release(rec)
		w.unrecordable.Add(1)
		return ErrUnrecordable
	}
}

func (w *Writer) shedRec(rec *domain.AuditRecord) {
	w.release(rec)
	w.shed.Add(1)
}

// isSecurity reports whether a record is security-relevant and must take the
// hard, fail-closed path: a denial (which also covers auth failures, since they
// are not allowed) or any call where a redaction occurred (design §5).
func isSecurity(rec *domain.AuditRecord) bool {
	return !rec.Allowed || rec.Redactions > 0
}

// Stats returns a snapshot of the writer's counters.
func (w *Writer) Stats() Stats {
	return Stats{
		Written:      w.written.Load(),
		Shed:         w.shed.Load(),
		Unrecordable: w.unrecordable.Load(),
		SinkErrors:   w.sinkErrors.Load(),
	}
}

// Close drains buffered records, flushes and fsyncs, and stops the writer. It is
// idempotent. It must be called only after request handlers have stopped
// submitting records (graceful shutdown drains in-flight calls first).
func (w *Writer) Close() error {
	if w.closed.Swap(true) {
		return nil
	}
	close(w.done)
	<-w.stopped
	w.stopTickers()
	return nil
}

func (w *Writer) run(flushC, fsyncC <-chan time.Time) {
	defer close(w.stopped)
	for {
		select {
		case rec := <-w.ch:
			w.write(rec)
		case <-flushC:
			w.flushBuf()
		case <-fsyncC:
			w.fsync()
		case <-w.done:
			w.drain()
			w.fsync()
			return
		}
	}
}

func (w *Writer) drain() {
	for {
		select {
		case rec := <-w.ch:
			w.write(rec)
		default:
			return
		}
	}
}

func (w *Writer) write(rec *domain.AuditRecord) {
	l := toLine(rec)
	w.release(rec)

	b, err := json.Marshal(l)
	if err != nil {
		w.sinkErrors.Add(1)
		return
	}
	if _, err := w.buf.Write(b); err != nil {
		w.sinkErrors.Add(1)
		return
	}
	if err := w.buf.WriteByte('\n'); err != nil {
		w.sinkErrors.Add(1)
		return
	}
	w.written.Add(1)
	w.batch++
	if w.batch >= w.cfg.FlushMaxRecords {
		w.flushBuf()
	}
}

func (w *Writer) flushBuf() {
	if err := w.buf.Flush(); err != nil {
		w.sinkErrors.Add(1)
	}
	w.batch = 0
}

func (w *Writer) fsync() {
	w.flushBuf()
	if err := w.sink.Sync(); err != nil {
		w.sinkErrors.Add(1)
	}
}

func (w *Writer) release(rec *domain.AuditRecord) {
	rec.Reset()
	w.pool.Put(rec)
}
