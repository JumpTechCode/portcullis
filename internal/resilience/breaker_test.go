package resilience

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a controllable monotonic clock for deterministic breaker tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newBreaker(threshold int, open time.Duration, now func() time.Time) *breaker {
	return &breaker{failureThreshold: threshold, openDuration: open, now: now}
}

func TestBreakerClosedAllows(t *testing.T) {
	b := newBreaker(3, time.Minute, time.Now)
	ok, probe := b.allow()
	if !ok || probe {
		t.Errorf("closed breaker allow() = (%v, %v), want (true, false)", ok, probe)
	}
}

func TestBreakerTripsAtThreshold(t *testing.T) {
	b := newBreaker(3, time.Minute, time.Now)
	b.record(false)
	b.record(false)
	if ok, _ := b.allow(); !ok {
		t.Fatal("breaker opened before reaching the failure threshold")
	}
	b.record(false) // third consecutive failure
	if ok, _ := b.allow(); ok {
		t.Error("breaker did not open at the failure threshold")
	}
}

func TestBreakerSuccessResetsFailures(t *testing.T) {
	b := newBreaker(3, time.Minute, time.Now)
	b.record(false)
	b.record(false)
	b.record(true) // resets the count
	b.record(false)
	b.record(false)
	if ok, _ := b.allow(); !ok {
		t.Error("breaker opened despite a success resetting the failure count")
	}
}

func TestBreakerHalfOpenSingleProbeThenClose(t *testing.T) {
	clk := newClock()
	b := newBreaker(1, time.Minute, clk.now)
	b.record(false) // trips (threshold 1)
	if ok, _ := b.allow(); ok {
		t.Fatal("breaker should be open immediately after tripping")
	}

	clk.advance(time.Minute)
	ok, probe := b.allow()
	if !ok || !probe {
		t.Errorf("after open duration allow() = (%v, %v), want a single probe (true, true)", ok, probe)
	}
	if ok2, _ := b.allow(); ok2 {
		t.Error("half-open breaker allowed a second concurrent call (single-flight violated)")
	}

	b.record(true) // probe succeeded
	if ok3, _ := b.allow(); !ok3 {
		t.Error("breaker did not close after a successful probe")
	}
}

func TestBreakerProbeFailureReopens(t *testing.T) {
	clk := newClock()
	b := newBreaker(1, time.Minute, clk.now)
	b.record(false)
	clk.advance(time.Minute)
	if ok, probe := b.allow(); !ok || !probe {
		t.Fatal("expected a probe after the open duration")
	}
	b.record(false) // probe failed -> reopen

	if ok, _ := b.allow(); ok {
		t.Error("breaker did not reopen after a failed probe")
	}
	clk.advance(time.Minute)
	if ok, probe := b.allow(); !ok || !probe {
		t.Error("breaker did not become probe-eligible again after reopening")
	}
}

// closedNow is the no-transition peek used for scopes whose probe is external
// (the target breaker, probed by ping). It must never move open -> half-open,
// so the external probe remains available.
func TestBreakerClosedNowDoesNotTransition(t *testing.T) {
	clk := newClock()
	b := newBreaker(1, time.Minute, clk.now)
	b.record(false)
	clk.advance(time.Minute)

	if b.closedNow() {
		t.Error("closedNow reported closed while the breaker was open")
	}
	// The peek must not have consumed the probe: allow() can still take it.
	if ok, probe := b.allow(); !ok || !probe {
		t.Error("closedNow consumed the half-open probe; it must not transition state")
	}
}
