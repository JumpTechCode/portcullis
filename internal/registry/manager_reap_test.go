package registry

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// reapDS is an internal DownstreamSession fake for the Manager idle-reaper
// tests. It counts calls, records closure, and can optionally gate CallTool so a
// test can hold a call in flight while the reaper runs.
type reapDS struct {
	id     int
	closed bool

	arrived *sync.WaitGroup
	release chan struct{}

	mu    sync.Mutex
	calls int
}

func (s *reapDS) CallTool(_ context.Context, tool string, _ json.RawMessage) (*domain.Result, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.arrived != nil {
		s.arrived.Done()
	}
	if s.release != nil {
		<-s.release
	}
	content, _ := json.Marshal(map[string]any{"tool": tool, "session": s.id})
	return &domain.Result{Content: content}, nil
}

func (s *reapDS) ListTools(context.Context, string) ([]ToolInfo, string, error) { return nil, "", nil }
func (s *reapDS) Ping(context.Context) error                                    { return nil }

func (s *reapDS) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *reapDS) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// reapDSFactory creates reapDS sessions, counting creations and retaining each.
type reapDSFactory struct {
	arrived *sync.WaitGroup
	release chan struct{}

	mu       sync.Mutex
	n        int
	sessions []*reapDS
}

func (f *reapDSFactory) New(context.Context) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	s := &reapDS{id: f.n, arrived: f.arrived, release: f.release}
	f.sessions = append(f.sessions, s)
	return s, nil
}

func (f *reapDSFactory) created() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *reapDSFactory) session(i int) *reapDS {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[i]
}

// reapManager builds a Manager with the given downstreams over a fake clock,
// without starting the background reaper goroutine, so a test can drive reapOnce
// deterministically.
func reapManager(t *testing.T, clk *reapClock, downstreams []ManagedDownstream) *Manager {
	t.Helper()
	mgr, err := newManager(downstreams, clk.now)
	if err != nil {
		t.Fatalf("newManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func reapCall(downstream string) *domain.Call {
	return &domain.Call{
		Client: domain.Identity{ID: "client"},
		Tool:   domain.ToolRef{Downstream: downstream, Tool: "t"},
		Args:   json.RawMessage(`{}`),
	}
}

func TestManagerReaperEvictsIdlePerClient(t *testing.T) {
	clk := &reapClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
	f := &reapDSFactory{}
	pool := newPool(f, PoolConfig{Max: 4, AcquireTimeout: time.Second, IdleTTL: time.Minute}, clk.now)
	mgr := reapManager(t, clk, []ManagedDownstream{{Name: "ds", Mode: PerClient, Pool: pool}})
	cs := mgr.NewClientSession()
	ctx := context.Background()

	if _, err := cs.Dispatch(ctx, reapCall("ds")); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	clk.advance(2 * time.Minute) // past the downstream's idle TTL
	mgr.reapOnce()

	if !f.session(0).isClosed() {
		t.Error("idle per_client session was not closed by the reaper")
	}
	if st := pool.Stats(); st.InUse != 0 {
		t.Errorf("pool InUse = %d after reap, want 0 (permit not returned)", st.InUse)
	}

	// The next call must re-acquire a fresh session (cache-and-re-acquire).
	if _, err := cs.Dispatch(ctx, reapCall("ds")); err != nil {
		t.Fatalf("Dispatch after reap: %v", err)
	}
	if got := f.created(); got != 2 {
		t.Errorf("created %d sessions, want 2 (re-acquire after reap)", got)
	}
}

func TestManagerReaperKeepsFreshPerClient(t *testing.T) {
	clk := &reapClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
	f := &reapDSFactory{}
	pool := newPool(f, PoolConfig{Max: 4, AcquireTimeout: time.Second, IdleTTL: time.Minute}, clk.now)
	mgr := reapManager(t, clk, []ManagedDownstream{{Name: "ds", Mode: PerClient, Pool: pool}})
	cs := mgr.NewClientSession()
	ctx := context.Background()

	if _, err := cs.Dispatch(ctx, reapCall("ds")); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	clk.advance(30 * time.Second) // within the idle TTL
	mgr.reapOnce()

	if f.session(0).isClosed() {
		t.Error("a still-fresh per_client session was wrongly reaped")
	}
	if _, err := cs.Dispatch(ctx, reapCall("ds")); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if got := f.created(); got != 1 {
		t.Errorf("created %d sessions, want 1 (fresh session reused)", got)
	}
}

func TestManagerReaperSkipsInFlightPerClient(t *testing.T) {
	clk := &reapClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
	var arrived sync.WaitGroup
	arrived.Add(1)
	f := &reapDSFactory{arrived: &arrived, release: make(chan struct{})}
	pool := newPool(f, PoolConfig{Max: 4, AcquireTimeout: time.Second, IdleTTL: time.Minute}, clk.now)
	mgr := reapManager(t, clk, []ManagedDownstream{{Name: "ds", Mode: PerClient, Pool: pool}})
	cs := mgr.NewClientSession()

	done := make(chan error, 1)
	go func() {
		_, err := cs.Dispatch(context.Background(), reapCall("ds"))
		done <- err
	}()
	arrived.Wait() // the call is now in flight inside CallTool

	clk.advance(2 * time.Minute) // past the idle TTL, but the call is in flight
	mgr.reapOnce()

	if f.session(0).isClosed() {
		t.Error("the reaper closed a session with a call in flight")
	}

	close(f.release) // let the in-flight call finish
	if err := <-done; err != nil {
		t.Fatalf("in-flight Dispatch failed: %v", err)
	}

	// Once the call has completed and the session is idle past the TTL, the next
	// reap closes it.
	clk.advance(2 * time.Minute)
	mgr.reapOnce()
	if !f.session(0).isClosed() {
		t.Error("idle session was not reaped after its in-flight call completed")
	}
}

func TestManagerReaperUsesPerDownstreamTTL(t *testing.T) {
	clk := &reapClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
	short := &reapDSFactory{}
	long := &reapDSFactory{}
	shortPool := newPool(short, PoolConfig{Max: 4, AcquireTimeout: time.Second, IdleTTL: time.Minute}, clk.now)
	longPool := newPool(long, PoolConfig{Max: 4, AcquireTimeout: time.Second, IdleTTL: 10 * time.Minute}, clk.now)
	mgr := reapManager(t, clk, []ManagedDownstream{
		{Name: "short", Mode: PerClient, Pool: shortPool},
		{Name: "long", Mode: PerClient, Pool: longPool},
	})
	cs := mgr.NewClientSession()
	ctx := context.Background()

	if _, err := cs.Dispatch(ctx, reapCall("short")); err != nil {
		t.Fatalf("dispatch short: %v", err)
	}
	if _, err := cs.Dispatch(ctx, reapCall("long")); err != nil {
		t.Fatalf("dispatch long: %v", err)
	}

	clk.advance(2 * time.Minute) // past short's TTL, within long's
	mgr.reapOnce()

	if !short.session(0).isClosed() {
		t.Error("short-TTL session was not reaped after passing its idle TTL")
	}
	if long.session(0).isClosed() {
		t.Error("long-TTL session was reaped before passing its idle TTL")
	}
}

func TestManagerReaperRaceVsDispatch(t *testing.T) {
	clk := &reapClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
	f := &reapDSFactory{}
	pool := newPool(f, PoolConfig{Max: 16, AcquireTimeout: 2 * time.Second, IdleTTL: time.Minute}, clk.now)
	mgr := reapManager(t, clk, []ManagedDownstream{{Name: "ds", Mode: PerClient, Pool: pool}})

	const dispatchers = 16
	const rounds = 50
	var wg sync.WaitGroup
	errs := make([]error, dispatchers)
	wg.Add(dispatchers)
	for i := range dispatchers {
		go func(i int) {
			defer wg.Done()
			cs := mgr.NewClientSession()
			defer func() { _ = cs.Close() }()
			for range rounds {
				if _, err := cs.Dispatch(context.Background(), reapCall("ds")); err != nil {
					errs[i] = err
					return
				}
			}
		}(i)
	}

	// Concurrently reap, advancing the clock so sessions cross the TTL mid-flight.
	stop := make(chan struct{})
	var reaperWG sync.WaitGroup
	reaperWG.Add(1)
	go func() {
		defer reaperWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				clk.advance(10 * time.Second)
				mgr.reapOnce()
			}
		}
	}()

	wg.Wait()
	close(stop)
	reaperWG.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("dispatcher %d errored under concurrent reaping: %v", i, err)
		}
	}
}

func TestManagerCloseStopsReaperAndIsIdempotent(t *testing.T) {
	f := &reapDSFactory{}
	pool := NewPool(f, PoolConfig{Max: 4, AcquireTimeout: time.Second, IdleTTL: time.Minute})
	mgr, err := NewManager([]ManagedDownstream{{Name: "ds", Mode: PerClient, Pool: pool}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Errorf("second Close = %v, want nil (idempotent)", err)
	}
}
