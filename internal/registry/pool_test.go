package registry_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/registry"
)

// fakeSession is a poolable session that records whether it was closed.
type fakeSession struct {
	id     int64
	closed atomic.Bool
}

func (s *fakeSession) Close() error {
	s.closed.Store(true)
	return nil
}

// fakeFactory creates fakeSessions and can be made to fail on demand.
type fakeFactory struct {
	created atomic.Int64
	fail    atomic.Bool
}

func (f *fakeFactory) New(_ context.Context) (registry.Session, error) {
	if f.fail.Load() {
		return nil, errors.New("spawn failed")
	}
	return &fakeSession{id: f.created.Add(1)}, nil
}

func testPoolConfig(maxSessions int) registry.PoolConfig {
	return registry.PoolConfig{Max: maxSessions, AcquireTimeout: 50 * time.Millisecond, IdleTTL: time.Hour}
}

func TestPoolAcquireCreatesUpToMax(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, testPoolConfig(2))
	defer func() { _ = p.Close() }()

	s1, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s1 == s2 {
		t.Error("two concurrent acquires returned the same session")
	}
	if got := f.created.Load(); got != 2 {
		t.Errorf("created %d sessions, want 2", got)
	}
	if st := p.Stats(); st.InUse != 2 {
		t.Errorf("InUse = %d, want 2", st.InUse)
	}
}

func TestPoolExhaustionFailsFast(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, testPoolConfig(1))
	defer func() { _ = p.Close() }()

	s1, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = s1

	start := time.Now()
	_, err = p.Acquire(context.Background())
	elapsed := time.Since(start)
	if !errors.Is(err, registry.ErrPoolExhausted) {
		t.Errorf("got %v, want ErrPoolExhausted", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("Acquire returned in %v, expected to wait ~acquire_timeout", elapsed)
	}
}

func TestPoolReleaseReusesSession(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, testPoolConfig(2))
	defer func() { _ = p.Close() }()

	s1, _ := p.Acquire(context.Background())
	p.Release(s1)

	s2, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s1 != s2 {
		t.Error("Acquire after Release did not reuse the idle session")
	}
	if got := f.created.Load(); got != 1 {
		t.Errorf("created %d sessions, want 1 (the released one is reused)", got)
	}
}

func TestPoolDiscardClosesAndFreesSlot(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, testPoolConfig(1))
	defer func() { _ = p.Close() }()

	s1, _ := p.Acquire(context.Background())
	p.Discard(s1)
	if !s1.(*fakeSession).closed.Load() {
		t.Error("Discard did not close the session")
	}

	// The slot is freed, so a fresh session can be acquired.
	s2, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire after Discard failed: %v", err)
	}
	if s2 == s1 {
		t.Error("Discard returned the discarded session to the pool")
	}
	if got := f.created.Load(); got != 2 {
		t.Errorf("created %d sessions, want 2", got)
	}
}

func TestPoolFactoryErrorFreesSlot(t *testing.T) {
	f := &fakeFactory{}
	f.fail.Store(true)
	p := registry.NewPool(f, testPoolConfig(1))
	defer func() { _ = p.Close() }()

	if _, err := p.Acquire(context.Background()); err == nil {
		t.Fatal("expected a factory error")
	}
	// The reserved slot must be released so a later acquire can succeed.
	f.fail.Store(false)
	if _, err := p.Acquire(context.Background()); err != nil {
		t.Errorf("slot not freed after factory error: %v", err)
	}
}

func TestPoolAcquireRespectsContextCancellation(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, testPoolConfig(1))
	defer func() { _ = p.Close() }()

	_, _ = p.Acquire(context.Background()) // exhaust
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestPoolCloseClosesIdleSessions(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, testPoolConfig(2))
	s1, _ := p.Acquire(context.Background())
	p.Release(s1)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if !s1.(*fakeSession).closed.Load() {
		t.Error("Close did not close the idle session")
	}
}

func TestPoolAppliesDefaults(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, registry.PoolConfig{}) // all zero -> defaults
	defer func() { _ = p.Close() }()
	s, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("default-configured pool failed to acquire: %v", err)
	}
	p.Release(s)
}

func TestPoolAfterCloseRejectsAndCleansUp(t *testing.T) {
	f := &fakeFactory{}
	p := registry.NewPool(f, testPoolConfig(2))
	s, _ := p.Acquire(context.Background())

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close not idempotent: %v", err)
	}
	if _, err := p.Acquire(context.Background()); !errors.Is(err, registry.ErrClosed) {
		t.Errorf("Acquire after Close = %v, want ErrClosed", err)
	}
	// Releasing an in-use session into a closed pool must close it, not pool it.
	p.Release(s)
	if !s.(*fakeSession).closed.Load() {
		t.Error("Release after Close did not close the session")
	}
}

func TestPoolConcurrentAcquireRelease(t *testing.T) {
	f := &fakeFactory{}
	const maxLive = 4
	p := registry.NewPool(f, registry.PoolConfig{Max: maxLive, AcquireTimeout: time.Second, IdleTTL: time.Hour})
	defer func() { _ = p.Close() }()

	var wg sync.WaitGroup
	var peak atomic.Int64
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := p.Acquire(context.Background())
			if err != nil {
				return
			}
			if iu := int64(p.Stats().InUse); iu > peak.Load() {
				peak.Store(iu)
			}
			p.Release(s)
		}()
	}
	wg.Wait()
	if peak.Load() > maxLive {
		t.Errorf("peak in-use %d exceeded max %d", peak.Load(), maxLive)
	}
}
