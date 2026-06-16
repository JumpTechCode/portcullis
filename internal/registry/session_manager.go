// This file implements the per-client-session dispatch layer (design §4,
// ADR-0002/0013). The Manager owns the gateway's downstream pools; each client
// connection gets its own ClientSession, which is the domain.Dispatcher bound to
// that connection's downstream sessions. Because a ClientSession can only reach
// its own sessions, cross-tenant mis-routing is not expressible. It is
// transport-agnostic (stdio or HTTP), so it carries no build tag.

package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// ErrClientSessionClosed is returned by Dispatch after the ClientSession has
// been closed.
var ErrClientSessionClosed = errors.New("client session closed")

// SessionMode selects how a downstream's sessions are shared across client
// sessions (design §4, ADR-0002).
type SessionMode int

const (
	// PerClient gives each client session its own dedicated session to the
	// downstream. It is the default and is correct for stateful servers: a client
	// session reuses one held session across its calls.
	PerClient SessionMode = iota
	// Shared checks a session out of and back into the pool per call, so a
	// stateless downstream's sessions cycle across client sessions.
	Shared
)

// ManagedDownstream registers one downstream with the Manager: its name, its
// session-sharing mode, and the bounded pool that backs it. The composition root
// builds the pool from the downstream's supervised factory and pool config.
type ManagedDownstream struct {
	Name string
	Mode SessionMode
	Pool *Pool
}

type managed struct {
	pool    *Pool
	mode    SessionMode
	idleTTL time.Duration
}

// Manager owns the gateway's downstream pools and hands each client connection
// its own ClientSession. There is one Manager per gateway; it is safe for
// concurrent use. A background reaper closes held per_client sessions that have
// gone idle past their downstream's idle_ttl (design §4): such sessions stay
// in-use from the pool's view and so never face the pool's own idle reaper.
type Manager struct {
	// downstreams is written once in newManager and never mutated thereafter.
	// That immutability is what lets Dispatch and ClientSession.Close read it
	// without a lock from any goroutine.
	downstreams map[string]managed
	now         func() time.Time
	reapEvery   time.Duration

	mu       sync.Mutex
	sessions map[*ClientSession]struct{} // live client sessions, for the reaper
	stop     chan struct{}
	closed   bool
}

// NewManager builds a Manager from the managed downstreams and starts its idle
// reaper. It fails fast on an empty downstream name, a nil pool, or a duplicate
// name, so a wiring mistake surfaces at startup rather than at request time.
func NewManager(downstreams []ManagedDownstream) (*Manager, error) {
	m, err := newManager(downstreams, time.Now)
	if err != nil {
		return nil, err
	}
	go m.reapLoop()
	return m, nil
}

// newManager builds a Manager over an injected clock but does not start the
// background reaper, so tests can drive reapOnce deterministically.
func newManager(downstreams []ManagedDownstream, now func() time.Time) (*Manager, error) {
	m := &Manager{
		downstreams: make(map[string]managed, len(downstreams)),
		now:         now,
		sessions:    make(map[*ClientSession]struct{}),
		stop:        make(chan struct{}),
	}
	for _, d := range downstreams {
		if d.Name == "" {
			return nil, errors.New("registry: managed downstream with empty name")
		}
		if d.Pool == nil {
			return nil, fmt.Errorf("registry: downstream %q has no pool", d.Name)
		}
		if _, dup := m.downstreams[d.Name]; dup {
			return nil, fmt.Errorf("registry: duplicate downstream %q", d.Name)
		}
		m.downstreams[d.Name] = managed{pool: d.Pool, mode: d.Mode, idleTTL: d.Pool.IdleTTL()}
	}
	m.reapEvery = m.reapInterval()
	return m, nil
}

// reapInterval is how often the reaper scans, set to half the shortest
// downstream idle_ttl (floored at a second) so an idle session is closed within
// roughly its TTL of going idle — the same cadence the pool's own reaper uses.
func (m *Manager) reapInterval() time.Duration {
	shortest := time.Duration(0)
	for _, d := range m.downstreams {
		if shortest == 0 || d.idleTTL < shortest {
			shortest = d.idleTTL
		}
	}
	interval := shortest / 2
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

// NewClientSession returns a fresh per-connection session set and registers it
// with the reaper. Each client session implements domain.Dispatcher over its own
// downstream sessions, so a call dispatched on one client session can never reach
// another's (ADR-0013).
func (m *Manager) NewClientSession() *ClientSession {
	cs := &ClientSession{mgr: m, entries: make(map[string]*cachedEntry)}
	m.mu.Lock()
	m.sessions[cs] = struct{}{}
	m.mu.Unlock()
	return cs
}

// forget removes a client session from the reaper's set once it has closed.
func (m *Manager) forget(cs *ClientSession) {
	m.mu.Lock()
	delete(m.sessions, cs)
	m.mu.Unlock()
}

// liveSessions snapshots the live client sessions so the reaper can scan them
// without holding the Manager lock across per-session work.
func (m *Manager) liveSessions() []*ClientSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ClientSession, 0, len(m.sessions))
	for cs := range m.sessions {
		out = append(out, cs)
	}
	return out
}

// Close stops the reaper and closes every downstream pool. Call it during
// graceful shutdown after all client sessions have been closed; it returns the
// first close error, if any, and is idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	close(m.stop)

	var firstErr error
	for _, d := range m.downstreams {
		if err := d.pool.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) reapLoop() {
	t := time.NewTicker(m.reapEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.reapOnce()
		case <-m.stop:
			return
		}
	}
}

// reapOnce closes and evicts every held per_client session that has been idle
// past its downstream's idle_ttl, returning its pool permit so the slot is freed
// for reuse. A session with a call in flight is left alone; the next call after
// an eviction re-acquires a fresh session (cache-and-re-acquire).
func (m *Manager) reapOnce() {
	now := m.now()
	for _, cs := range m.liveSessions() {
		cs.reapIdle(now)
	}
}

// cachedEntry holds a client session's per_client session for one downstream.
// Its mutex single-flights creation, so a concurrent first-use burst opens one
// session rather than racing to spawn several; it also guards the in-flight
// count and last-use time the reaper consults to evict an idle session safely.
type cachedEntry struct {
	mu       sync.Mutex
	sess     DownstreamSession
	inFlight int
	lastUse  time.Time
}

// ClientSession is one client connection's set of downstream sessions and the
// domain.Dispatcher for that connection. It is safe for concurrent Dispatch.
// Close must not run concurrently with Dispatch: the composition root drains
// in-flight calls before closing a client session (design §5 shutdown order).
type ClientSession struct {
	mgr *Manager

	mu      sync.Mutex
	entries map[string]*cachedEntry
	closed  bool
}

// Compile-time assertion that ClientSession is a domain.Dispatcher.
var _ domain.Dispatcher = (*ClientSession)(nil)

// Dispatch routes a call to this client session's session for the call's
// downstream and executes it. An unknown downstream is a clean error, never a
// panic (design §7). In Shared mode the session is acquired from the pool for the
// call and returned afterward (or discarded if the call failed at the
// transport); in PerClient mode the session is cached for reuse across the client
// session's calls and discarded (so the next call re-acquires) only if a call
// fails at the transport.
func (cs *ClientSession) Dispatch(ctx context.Context, call *domain.Call) (*domain.Result, error) {
	d, ok := cs.mgr.downstreams[call.Tool.Downstream]
	if !ok {
		return nil, fmt.Errorf("registry: unknown downstream %q", call.Tool.Downstream)
	}
	if d.mode == Shared {
		return cs.dispatchShared(ctx, d.pool, call)
	}
	return cs.dispatchPerClient(ctx, call.Tool.Downstream, d.pool, call)
}

func (cs *ClientSession) dispatchShared(ctx context.Context, pool *Pool, call *domain.Call) (*domain.Result, error) {
	sess, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	ds, ok := sess.(DownstreamSession)
	if !ok {
		pool.Discard(sess)
		return nil, fmt.Errorf("registry: downstream %q session is not a DownstreamSession", call.Tool.Downstream)
	}
	res, callErr := ds.CallTool(ctx, call.Tool.Tool, call.Args)
	if callErr != nil {
		// A transport/protocol error makes the session suspect: discard it rather
		// than return it to the pool for the next caller to inherit.
		pool.Discard(sess)
		return nil, callErr
	}
	pool.Release(sess)
	return res, nil
}

func (cs *ClientSession) dispatchPerClient(ctx context.Context, name string, pool *Pool, call *domain.Call) (*domain.Result, error) {
	ds, e, err := cs.perClientSession(ctx, name, pool)
	if err != nil {
		return nil, err
	}
	res, callErr := ds.CallTool(ctx, call.Tool.Tool, call.Args)
	cs.finishPerClient(e, pool, ds, callErr != nil)
	if callErr != nil {
		return nil, callErr
	}
	return res, nil
}

// perClientSession returns this client session's cached session for the named
// downstream (and its entry), acquiring and caching one on first use. Creation is
// single-flight per downstream: the entry mutex is held across Acquire so a
// concurrent burst opens one session, but it is released before the call so
// concurrent calls to the same downstream are not serialized (the SDK session
// multiplexes them). It marks the call in flight under the entry lock so the
// reaper cannot evict a session a call is about to use; finishPerClient clears
// the mark.
func (cs *ClientSession) perClientSession(ctx context.Context, name string, pool *Pool) (DownstreamSession, *cachedEntry, error) {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return nil, nil, ErrClientSessionClosed
	}
	e := cs.entries[name]
	if e == nil {
		e = &cachedEntry{}
		cs.entries[name] = e
	}
	cs.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess != nil {
		e.inFlight++
		return e.sess, e, nil
	}
	sess, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	ds, ok := sess.(DownstreamSession)
	if !ok {
		pool.Discard(sess)
		return nil, nil, fmt.Errorf("registry: downstream %q session is not a DownstreamSession", name)
	}
	e.sess = ds
	e.lastUse = cs.mgr.now()
	e.inFlight++
	return ds, e, nil
}

// finishPerClient records that a per_client call has completed: it drops the
// in-flight count and stamps last-use, so the reaper measures idleness from the
// last completed call. If the call failed at the transport it also discards the
// suspect session so the next call re-acquires a fresh one. The discard is gated
// on the clear-race (e.sess == ds): when several concurrent calls share one
// cached session from a single Acquire and all fail, only the caller that still
// finds e.sess == ds returns it to the pool — otherwise one Acquire would be
// returned many times, corrupting the permit count and the Max bound.
func (cs *ClientSession) finishPerClient(e *cachedEntry, pool *Pool, ds DownstreamSession, failed bool) {
	e.mu.Lock()
	e.inFlight--
	e.lastUse = cs.mgr.now()
	won := failed && e.sess == ds
	if won {
		e.sess = nil
	}
	e.mu.Unlock()
	if won {
		pool.Discard(ds)
	}
}

// reapIdle closes and evicts this client session's held per_client sessions that
// have no call in flight and were last used before their downstream's idle_ttl,
// returning each to its pool. It runs concurrently with Dispatch: the entry lock
// serializes it against a call's start and finish, and gating on inFlight == 0
// guarantees an in-use session is never closed out from under a call. The next
// call after an eviction re-acquires (cache-and-re-acquire).
func (cs *ClientSession) reapIdle(now time.Time) {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return
	}
	// Snapshot the entries so the per-entry close work (which may block) runs
	// without holding the client-session lock that Dispatch needs.
	targets := make([]namedEntry, 0, len(cs.entries))
	for name, e := range cs.entries {
		targets = append(targets, namedEntry{name: name, e: e})
	}
	cs.mu.Unlock()

	for _, t := range targets {
		d := cs.mgr.downstreams[t.name]
		cutoff := now.Add(-d.idleTTL)
		t.e.mu.Lock()
		evict := t.e.sess != nil && t.e.inFlight == 0 && t.e.lastUse.Before(cutoff)
		var ds DownstreamSession
		if evict {
			ds = t.e.sess
			t.e.sess = nil
		}
		t.e.mu.Unlock()
		if evict {
			d.pool.Discard(ds)
		}
	}
}

// namedEntry pairs a downstream name with its cached entry for the reaper's
// snapshot, so the per-entry name (for its pool and idle_ttl) survives the
// release of the client-session lock.
type namedEntry struct {
	name string
	e    *cachedEntry
}

// Close discards every cached per_client session, returning each to its pool,
// and marks the client session closed. It is idempotent. Shared-mode downstreams
// hold nothing between calls, so they need no teardown here.
func (cs *ClientSession) Close() error {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return nil
	}
	cs.closed = true
	entries := cs.entries
	cs.entries = nil
	cs.mu.Unlock()

	cs.mgr.forget(cs) // stop the reaper from scanning a closed session

	for name, e := range entries {
		e.mu.Lock()
		if e.sess != nil {
			cs.mgr.downstreams[name].pool.Discard(e.sess)
			e.sess = nil
		}
		e.mu.Unlock()
	}
	return nil
}
