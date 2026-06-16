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
	pool *Pool
	mode SessionMode
}

// Manager owns the gateway's downstream pools and hands each client connection
// its own ClientSession. There is one Manager per gateway; it is safe for
// concurrent use.
type Manager struct {
	// downstreams is written once in NewManager and never mutated thereafter.
	// That immutability is what lets Dispatch and ClientSession.Close read it
	// without a lock from any goroutine.
	downstreams map[string]managed
}

// NewManager builds a Manager from the managed downstreams. It fails fast on an
// empty downstream name, a nil pool, or a duplicate name, so a wiring mistake
// surfaces at startup rather than at request time.
func NewManager(downstreams []ManagedDownstream) (*Manager, error) {
	m := &Manager{downstreams: make(map[string]managed, len(downstreams))}
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
		m.downstreams[d.Name] = managed{pool: d.Pool, mode: d.Mode}
	}
	return m, nil
}

// NewClientSession returns a fresh per-connection session set. Each client
// session implements domain.Dispatcher over its own downstream sessions, so a
// call dispatched on one client session can never reach another's (ADR-0013).
func (m *Manager) NewClientSession() *ClientSession {
	return &ClientSession{mgr: m, entries: make(map[string]*cachedEntry)}
}

// Close closes every downstream pool. Call it during graceful shutdown after all
// client sessions have been closed; it returns the first close error, if any.
func (m *Manager) Close() error {
	var firstErr error
	for _, d := range m.downstreams {
		if err := d.pool.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// cachedEntry holds a client session's per_client session for one downstream.
// Its mutex single-flights creation, so a concurrent first-use burst opens one
// session rather than racing to spawn several.
type cachedEntry struct {
	mu   sync.Mutex
	sess DownstreamSession
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
	ds, err := cs.perClientSession(ctx, name, pool)
	if err != nil {
		return nil, err
	}
	res, callErr := ds.CallTool(ctx, call.Tool.Tool, call.Args)
	if callErr != nil {
		cs.discardPerClient(name, pool, ds)
		return nil, callErr
	}
	return res, nil
}

// perClientSession returns this client session's cached session for the named
// downstream, acquiring and caching one on first use. Creation is single-flight
// per downstream: the entry mutex is held across Acquire so a concurrent burst
// opens one session, but it is released before the call so concurrent calls to
// the same downstream are not serialized (the SDK session multiplexes them).
func (cs *ClientSession) perClientSession(ctx context.Context, name string, pool *Pool) (DownstreamSession, error) {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return nil, ErrClientSessionClosed
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
		return e.sess, nil
	}
	sess, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	ds, ok := sess.(DownstreamSession)
	if !ok {
		pool.Discard(sess)
		return nil, fmt.Errorf("registry: downstream %q session is not a DownstreamSession", name)
	}
	e.sess = ds
	return ds, nil
}

// discardPerClient drops a suspect cached session so the next call re-acquires a
// fresh one. Because perClientSession releases the entry lock before the call,
// several concurrent calls can share one cached session from a single Acquire; if
// they all fail, only the caller that wins the clear-race (still finds e.sess ==
// ds) must Discard it — otherwise one Acquire would be returned to the pool many
// times, corrupting the permit count and the Max bound. Losers (e.sess already
// cleared, or replaced by a freshly re-acquired session) skip the Discard.
func (cs *ClientSession) discardPerClient(name string, pool *Pool, ds DownstreamSession) {
	cs.mu.Lock()
	e := cs.entries[name]
	cs.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	won := e.sess == ds
	if won {
		e.sess = nil
	}
	e.mu.Unlock()
	if won {
		pool.Discard(ds)
	}
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
