// Package registry manages the gateway's downstream MCP sessions: a bounded,
// supervised stdio subprocess pool per downstream plus remote HTTP sessions
// (design §4, §6.1, ADR-0002/0004/0006). This file implements the transport-
// agnostic pool; the stdio and HTTP session factories that back it are built on
// top of it.
package registry

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrPoolExhausted is returned by Acquire when no session is available within
// the acquire timeout. The call fails fast rather than queueing unboundedly
// (design §4, ADR-0004).
var ErrPoolExhausted = errors.New("downstream pool exhausted")

// ErrClosed is returned by Acquire after the pool has been closed.
var ErrClosed = errors.New("pool closed")

// Session is a poolable downstream session. The pool only needs to be able to
// close one; richer behavior (calling tools, pinging) is added by the concrete
// session types built on the pool.
type Session interface {
	Close() error
}

// Factory creates a new downstream session, spawning whatever transport backs
// it (an stdio subprocess, a remote HTTP connection).
type Factory interface {
	New(ctx context.Context) (Session, error)
}

// PoolConfig bounds and tunes a pool. Zero fields fall back to package defaults.
type PoolConfig struct {
	// Max is the maximum number of live sessions.
	Max int
	// AcquireTimeout bounds how long Acquire waits for a slot before failing fast.
	AcquireTimeout time.Duration
	// IdleTTL is how long an idle session is kept before being reaped.
	IdleTTL time.Duration
}

const (
	defaultMax            = 16
	defaultAcquireTimeout = 2 * time.Second
	defaultIdleTTL        = 90 * time.Second
)

// Stats is a snapshot of a pool's occupancy.
type Stats struct {
	InUse   int
	Idle    int
	Created int64
}

type idleEntry struct {
	sess  Session
	since time.Time
}

// Pool is a bounded, idle-reaping pool of downstream sessions. At most Max
// sessions are live at once; an Acquire past Max waits up to AcquireTimeout then
// fails fast. It is safe for concurrent use.
//
// The permit channel enforces the bound and gives Acquire a clean timeout; the
// mutex guards the idle set and counters. Idle sessions hold no permit, so the
// reaper can close them independently.
type Pool struct {
	factory Factory
	cfg     PoolConfig
	now     func() time.Time
	permits chan struct{}
	stop    chan struct{}

	mu      sync.Mutex
	idle    []idleEntry
	inUse   int
	created int64
	closed  bool
}

// NewPool builds a pool and starts its idle reaper.
func NewPool(factory Factory, cfg PoolConfig) *Pool {
	p := newPool(factory, cfg, time.Now)
	go p.reapLoop()
	return p
}

func newPool(factory Factory, cfg PoolConfig, now func() time.Time) *Pool {
	if cfg.Max <= 0 {
		cfg.Max = defaultMax
	}
	if cfg.AcquireTimeout <= 0 {
		cfg.AcquireTimeout = defaultAcquireTimeout
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = defaultIdleTTL
	}
	permits := make(chan struct{}, cfg.Max)
	for range cfg.Max {
		permits <- struct{}{}
	}
	return &Pool{
		factory: factory,
		cfg:     cfg,
		now:     now,
		permits: permits,
		stop:    make(chan struct{}),
	}
}

// Acquire returns a session, reusing an idle one or creating a new one. If Max
// sessions are already live it waits up to AcquireTimeout, then returns
// ErrPoolExhausted. It respects context cancellation.
func (p *Pool) Acquire(ctx context.Context) (Session, error) {
	timer := time.NewTimer(p.cfg.AcquireTimeout)
	defer timer.Stop()
	select {
	case <-p.permits:
	case <-timer.C:
		return nil, ErrPoolExhausted
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.returnPermit()
		return nil, ErrClosed
	}
	if n := len(p.idle); n > 0 {
		e := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.inUse++
		p.mu.Unlock()
		return e.sess, nil
	}
	p.inUse++ // reserve the slot while we create outside the lock
	p.mu.Unlock()

	sess, err := p.factory.New(ctx)
	if err != nil {
		p.mu.Lock()
		p.inUse--
		p.mu.Unlock()
		p.returnPermit()
		return nil, err
	}
	p.mu.Lock()
	p.created++
	p.mu.Unlock()
	return sess, nil
}

// Release returns a healthy session to the idle set for reuse.
func (p *Pool) Release(sess Session) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = sess.Close()
		return
	}
	p.inUse--
	p.idle = append(p.idle, idleEntry{sess: sess, since: p.now()})
	p.mu.Unlock()
	p.returnPermit()
}

// Discard closes a session (e.g. one whose subprocess crashed) and frees its
// slot so a fresh session can take its place. Use it instead of Release for an
// unhealthy session.
func (p *Pool) Discard(sess Session) {
	_ = sess.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.inUse--
	p.mu.Unlock()
	p.returnPermit()
}

// Stats returns a snapshot of pool occupancy. After Close the counts are
// best-effort (the pool is shutting down and no longer maintains its invariants
// for observability).
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{InUse: p.inUse, Idle: len(p.idle), Created: p.created}
}

// Close stops the reaper and closes all idle sessions. In-use sessions are
// closed when the caller returns them; the composition root drains in-flight
// calls before closing the pool.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()

	close(p.stop)

	var firstErr error
	for _, e := range idle {
		if err := e.sess.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// returnPermit makes a slot available again. It never blocks: the buffered
// channel has exactly Max capacity and a permit is only ever returned for one
// previously taken.
func (p *Pool) returnPermit() {
	select {
	case p.permits <- struct{}{}:
	default:
	}
}

func (p *Pool) reapLoop() {
	interval := p.cfg.IdleTTL / 2
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			p.reapOnce()
		case <-p.stop:
			return
		}
	}
}

// reapOnce closes idle sessions that have been idle longer than IdleTTL. Idle
// sessions hold no permit, so reaping them does not change the slot count.
func (p *Pool) reapOnce() {
	cutoff := p.now().Add(-p.cfg.IdleTTL)
	p.mu.Lock()
	// Compact in place: kept aliases p.idle's backing array. This is safe only
	// because we append at most one already-read element per iteration, so the
	// write index never outruns the read index.
	kept := p.idle[:0]
	var dead []Session
	for _, e := range p.idle {
		if e.since.Before(cutoff) {
			dead = append(dead, e.sess)
		} else {
			kept = append(kept, e)
		}
	}
	p.idle = kept
	p.mu.Unlock()

	for _, s := range dead {
		_ = s.Close()
	}
}
