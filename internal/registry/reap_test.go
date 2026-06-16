package registry

import (
	"context"
	"sync"
	"testing"
	"time"
)

type reapClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *reapClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *reapClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type reapSession struct{ closed bool }

func (s *reapSession) Close() error { s.closed = true; return nil }

type reapFactory struct{ n int }

func (f *reapFactory) New(_ context.Context) (Session, error) {
	f.n++
	return &reapSession{}, nil
}

func newReapPool(clk *reapClock, ttl time.Duration) *Pool {
	return newPool(&reapFactory{}, PoolConfig{Max: 2, AcquireTimeout: time.Second, IdleTTL: ttl}, clk.now)
}

func TestReapClosesExpiredIdleSessions(t *testing.T) {
	clk := &reapClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
	p := newReapPool(clk, time.Minute)

	s, _ := p.Acquire(context.Background())
	p.Release(s) // idle as of clk.now()

	clk.advance(2 * time.Minute) // past IdleTTL
	p.reapOnce()

	if st := p.Stats(); st.Idle != 0 {
		t.Errorf("idle sessions after reap = %d, want 0", st.Idle)
	}
	if !s.(*reapSession).closed {
		t.Error("reaped session was not closed")
	}
}

func TestReapKeepsFreshIdleSessions(t *testing.T) {
	clk := &reapClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
	p := newReapPool(clk, time.Minute)

	s, _ := p.Acquire(context.Background())
	p.Release(s)

	clk.advance(30 * time.Second) // within IdleTTL
	p.reapOnce()

	if st := p.Stats(); st.Idle != 1 {
		t.Errorf("idle sessions after reap = %d, want 1 (still fresh)", st.Idle)
	}
	if s.(*reapSession).closed {
		t.Error("a still-fresh idle session was wrongly reaped")
	}
}
