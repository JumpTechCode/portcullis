package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/registry"
)

// fakeDownstream is a DownstreamSession that records its use. Each carries a
// unique id so a test can tell which session served a call (proving routing and
// per-client isolation). CallTool returns callErr when set, otherwise a result
// echoing the tool name and the serving session's id.
type fakeDownstream struct {
	id      int
	callErr error
	calls   atomic.Int32
	closed  atomic.Bool

	// arrived and release, when set, gate CallTool so a test can hold several
	// concurrent calls inside the same cached session at once: each call signals
	// arrived.Done() on entry, then blocks until release is closed.
	arrived *sync.WaitGroup
	release chan struct{}
}

func (f *fakeDownstream) CallTool(_ context.Context, tool string, _ json.RawMessage) (*domain.Result, error) {
	f.calls.Add(1)
	if f.arrived != nil {
		f.arrived.Done()
	}
	if f.release != nil {
		<-f.release
	}
	if f.callErr != nil {
		return nil, f.callErr
	}
	content, _ := json.Marshal(map[string]any{"tool": tool, "session": f.id})
	return &domain.Result{Content: content}, nil
}

func (f *fakeDownstream) ListTools(context.Context, string) ([]registry.ToolInfo, string, error) {
	return nil, "", nil
}

func (f *fakeDownstream) Ping(context.Context) error { return nil }

func (f *fakeDownstream) Close() error {
	f.closed.Store(true)
	return nil
}

// fakeDSFactory creates fakeDownstreams, counting creations and retaining each
// for inspection. callErr, if set, is given to every session it creates.
type fakeDSFactory struct {
	callErr error
	arrived *sync.WaitGroup
	release chan struct{}
	created atomic.Int32

	mu       sync.Mutex
	sessions []*fakeDownstream
}

func (f *fakeDSFactory) New(context.Context) (registry.Session, error) {
	id := int(f.created.Add(1))
	s := &fakeDownstream{id: id, callErr: f.callErr, arrived: f.arrived, release: f.release}
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	return s, nil
}

// managerWith builds a Manager with a single downstream named "ds" in the given
// mode, backed by a pool over factory. A generous pool keeps the tests free of
// incidental exhaustion; the default idle TTL keeps the reaper from interfering.
func managerWith(t *testing.T, mode registry.SessionMode, factory registry.Factory) *registry.Manager {
	t.Helper()
	pool := registry.NewPool(factory, registry.PoolConfig{Max: 32, AcquireTimeout: 2 * time.Second})
	mgr, err := registry.NewManager([]registry.ManagedDownstream{{Name: "ds", Mode: mode, Pool: pool}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func callTo(downstream, tool string) *domain.Call {
	return &domain.Call{
		Client: domain.Identity{ID: "client"},
		Tool:   domain.ToolRef{Downstream: downstream, Tool: tool},
		Args:   json.RawMessage(`{}`),
	}
}

func TestNewManagerRejectsBadSpec(t *testing.T) {
	pool := registry.NewPool(&fakeDSFactory{}, registry.PoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	cases := map[string][]registry.ManagedDownstream{
		"empty name":     {{Name: "", Mode: registry.PerClient, Pool: pool}},
		"nil pool":       {{Name: "ds", Mode: registry.PerClient, Pool: nil}},
		"duplicate name": {{Name: "ds", Pool: pool}, {Name: "ds", Pool: pool}},
	}
	for name, spec := range cases {
		if _, err := registry.NewManager(spec); err == nil {
			t.Errorf("%s: NewManager returned no error", name)
		}
	}
}

func TestDispatchUnknownDownstream(t *testing.T) {
	mgr := managerWith(t, registry.PerClient, &fakeDSFactory{})
	cs := mgr.NewClientSession()
	if _, err := cs.Dispatch(context.Background(), callTo("nope", "tool")); err == nil {
		t.Error("Dispatch to an unregistered downstream returned no error")
	}
}

func TestDispatchRoutesAndReturnsResult(t *testing.T) {
	mgr := managerWith(t, registry.PerClient, &fakeDSFactory{})
	cs := mgr.NewClientSession()
	res, err := cs.Dispatch(context.Background(), callTo("ds", "create_issue"))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Content, &got); err != nil {
		t.Fatalf("result content: %v", err)
	}
	if got["tool"] != "create_issue" {
		t.Errorf("served tool = %v, want create_issue", got["tool"])
	}
}

func TestDispatchSharedReleasesForReuse(t *testing.T) {
	f := &fakeDSFactory{}
	mgr := managerWith(t, registry.Shared, f)
	cs := mgr.NewClientSession()
	ctx := context.Background()

	if _, err := cs.Dispatch(ctx, callTo("ds", "a")); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	if _, err := cs.Dispatch(ctx, callTo("ds", "b")); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	// Shared mode returns the session to the pool after each call, so the second
	// call reuses the first session rather than creating a new one.
	if got := f.created.Load(); got != 1 {
		t.Errorf("created %d sessions, want 1 (shared reuse)", got)
	}
}

func TestDispatchSharedDiscardsOnError(t *testing.T) {
	f := &fakeDSFactory{callErr: errors.New("transport boom")}
	mgr := managerWith(t, registry.Shared, f)
	cs := mgr.NewClientSession()
	ctx := context.Background()

	if _, err := cs.Dispatch(ctx, callTo("ds", "a")); err == nil {
		t.Fatal("Dispatch with a failing session returned no error")
	}
	if _, err := cs.Dispatch(ctx, callTo("ds", "b")); err == nil {
		t.Fatal("second Dispatch returned no error")
	}
	// A transport error makes the session suspect, so it is discarded rather than
	// returned to the pool; the next call must create a fresh session.
	if got := f.created.Load(); got != 2 {
		t.Errorf("created %d sessions, want 2 (discard-on-error then re-create)", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sessions[0].closed.Load() {
		t.Error("errored shared session was not closed on discard")
	}
}

func TestDispatchPerClientCachesSession(t *testing.T) {
	f := &fakeDSFactory{}
	mgr := managerWith(t, registry.PerClient, f)
	cs := mgr.NewClientSession()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := cs.Dispatch(ctx, callTo("ds", "t")); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}
	// per_client holds one session for the client session's lifetime, so all
	// three calls reuse the same subprocess.
	if got := f.created.Load(); got != 1 {
		t.Errorf("created %d sessions, want 1 (per_client caching)", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.sessions[0].calls.Load(); got != 3 {
		t.Errorf("cached session served %d calls, want 3", got)
	}
}

func TestPerClientSessionsIsolatedAcrossClients(t *testing.T) {
	f := &fakeDSFactory{}
	mgr := managerWith(t, registry.PerClient, f)
	ctx := context.Background()

	cs1 := mgr.NewClientSession()
	cs2 := mgr.NewClientSession()
	r1, err := cs1.Dispatch(ctx, callTo("ds", "t"))
	if err != nil {
		t.Fatalf("cs1 Dispatch: %v", err)
	}
	r2, err := cs2.Dispatch(ctx, callTo("ds", "t"))
	if err != nil {
		t.Fatalf("cs2 Dispatch: %v", err)
	}
	// Two client sessions must not share a per_client session: each gets its own.
	if got := f.created.Load(); got != 2 {
		t.Errorf("created %d sessions, want 2 (per-client isolation)", got)
	}
	var g1, g2 map[string]any
	_ = json.Unmarshal(r1.Content, &g1)
	_ = json.Unmarshal(r2.Content, &g2)
	if g1["session"] == g2["session"] {
		t.Errorf("both clients were served by session %v (no isolation)", g1["session"])
	}
}

func TestDispatchPerClientReacquiresAfterError(t *testing.T) {
	f := &fakeDSFactory{callErr: errors.New("transport boom")}
	mgr := managerWith(t, registry.PerClient, f)
	cs := mgr.NewClientSession()
	ctx := context.Background()

	if _, err := cs.Dispatch(ctx, callTo("ds", "a")); err == nil {
		t.Fatal("Dispatch with a failing session returned no error")
	}
	if _, err := cs.Dispatch(ctx, callTo("ds", "b")); err == nil {
		t.Fatal("second Dispatch returned no error")
	}
	// The cached session is discarded on a transport error, so the next call
	// re-acquires a fresh one (cache-and-re-acquire).
	if got := f.created.Load(); got != 2 {
		t.Errorf("created %d sessions, want 2 (re-acquire after error)", got)
	}
}

func TestClientSessionCloseDiscardsCached(t *testing.T) {
	f := &fakeDSFactory{}
	mgr := managerWith(t, registry.PerClient, f)
	cs := mgr.NewClientSession()
	ctx := context.Background()

	if _, err := cs.Dispatch(ctx, callTo("ds", "t")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := cs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	f.mu.Lock()
	cached := f.sessions[0]
	f.mu.Unlock()
	if !cached.closed.Load() {
		t.Error("cached per_client session was not closed by ClientSession.Close")
	}
	// Close is idempotent.
	if err := cs.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// bareSession implements only Session (not DownstreamSession), to exercise the
// manager's defensive guard against a pool built from the wrong factory.
type bareSession struct{}

func (bareSession) Close() error { return nil }

type bareFactory struct{}

func (bareFactory) New(context.Context) (registry.Session, error) { return bareSession{}, nil }

func TestDispatchRejectsNonDownstreamSession(t *testing.T) {
	for _, mode := range []registry.SessionMode{registry.Shared, registry.PerClient} {
		mgr := managerWith(t, mode, bareFactory{})
		cs := mgr.NewClientSession()
		if _, err := cs.Dispatch(context.Background(), callTo("ds", "t")); err == nil {
			t.Errorf("mode %v: Dispatch with a non-DownstreamSession returned no error", mode)
		}
	}
}

func TestDispatchAfterCloseFails(t *testing.T) {
	mgr := managerWith(t, registry.PerClient, &fakeDSFactory{})
	cs := mgr.NewClientSession()
	if err := cs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := cs.Dispatch(context.Background(), callTo("ds", "t"))
	if !errors.Is(err, registry.ErrClientSessionClosed) {
		t.Errorf("Dispatch after Close = %v, want ErrClientSessionClosed", err)
	}
}

func TestDispatchAcquireError(t *testing.T) {
	// Closing the pool makes Acquire fail fast, exercising the acquire-error path.
	pool := registry.NewPool(&fakeDSFactory{}, registry.PoolConfig{Max: 1})
	mgr, err := registry.NewManager([]registry.ManagedDownstream{{Name: "ds", Mode: registry.Shared, Pool: pool}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	_ = pool.Close()
	cs := mgr.NewClientSession()
	if _, err := cs.Dispatch(context.Background(), callTo("ds", "t")); err == nil {
		t.Error("Dispatch over a closed pool returned no error")
	}
}

// TestConcurrentErrorDiscardDoesNotCorruptPool is the regression test for the
// concurrent per_client discard bug: several calls share one cached session (one
// Acquire), and when they all fail at the transport each tries to discard it. The
// session — and its single pool permit — must be returned exactly once, not once
// per failing call, or the pool's in-use count and Max bound are corrupted.
func TestConcurrentErrorDiscardDoesNotCorruptPool(t *testing.T) {
	const n = 20
	var arrived sync.WaitGroup
	arrived.Add(n)
	f := &fakeDSFactory{
		callErr: errors.New("transport boom"),
		arrived: &arrived,
		release: make(chan struct{}),
	}
	pool := registry.NewPool(f, registry.PoolConfig{Max: 4, AcquireTimeout: 2 * time.Second})
	mgr, err := registry.NewManager([]registry.ManagedDownstream{{Name: "ds", Mode: registry.PerClient, Pool: pool}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	cs := mgr.NewClientSession()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = cs.Dispatch(context.Background(), callTo("ds", "t"))
		}()
	}
	arrived.Wait()   // all n calls are inside CallTool on the one cached session
	close(f.release) // release them to fail together → concurrent finishPerClient discard
	wg.Wait()

	if got := f.created.Load(); got != 1 {
		t.Fatalf("created %d sessions, want 1 (single-flight)", got)
	}
	// Exactly one Acquire and one Discard: in-use returns to zero. The bug drove
	// this negative (one acquire returned to the pool n times).
	if st := pool.Stats(); st.InUse != 0 {
		t.Errorf("pool InUse = %d after the error burst, want 0 (permit over-return)", st.InUse)
	}
}

func TestDispatchPerClientMultipleDownstreams(t *testing.T) {
	fa, fb := &fakeDSFactory{}, &fakeDSFactory{}
	poolA := registry.NewPool(fa, registry.PoolConfig{Max: 4})
	poolB := registry.NewPool(fb, registry.PoolConfig{Max: 4})
	mgr, err := registry.NewManager([]registry.ManagedDownstream{
		{Name: "a", Mode: registry.PerClient, Pool: poolA},
		{Name: "b", Mode: registry.Shared, Pool: poolB},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	cs := mgr.NewClientSession()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := cs.Dispatch(ctx, callTo("a", "ta")); err != nil {
			t.Fatalf("dispatch a: %v", err)
		}
		if _, err := cs.Dispatch(ctx, callTo("b", "tb")); err != nil {
			t.Fatalf("dispatch b: %v", err)
		}
	}
	// Each downstream is served independently: per_client "a" caches one session;
	// shared "b" reuses its pooled session. The two never cross.
	if got := fa.created.Load(); got != 1 {
		t.Errorf("downstream a created %d sessions, want 1 (per_client cache)", got)
	}
	if got := fb.created.Load(); got != 1 {
		t.Errorf("downstream b created %d sessions, want 1 (shared reuse)", got)
	}
}

func TestConcurrentDispatchPerClientIsRaceFree(t *testing.T) {
	f := &fakeDSFactory{}
	mgr := managerWith(t, registry.PerClient, f)
	cs := mgr.NewClientSession()
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = cs.Dispatch(ctx, callTo("ds", "t"))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	// Single-flight init must converge on exactly one cached session for the
	// downstream, regardless of the concurrent first-use burst.
	if got := f.created.Load(); got != 1 {
		t.Errorf("created %d sessions under concurrent first-use, want 1", got)
	}
}
