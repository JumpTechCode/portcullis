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

// supSession is a trivial poolable session for supervisor tests.
type supSession struct{ closed atomic.Bool }

func (s *supSession) Close() error {
	s.closed.Store(true)
	return nil
}

// scriptedFactory hands out fresh sessions, or fails while failing is set. It
// records how many times New was actually invoked so tests can assert that a
// broken supervisor short-circuits without ever touching the delegate.
type scriptedFactory struct {
	calls   atomic.Int64
	failing atomic.Bool
}

var errSpawn = errors.New("spawn failed")

func (f *scriptedFactory) New(_ context.Context) (registry.Session, error) {
	f.calls.Add(1)
	if f.failing.Load() {
		return nil, errSpawn
	}
	return &supSession{}, nil
}

// fakeClock is a manually advanced clock for deterministic cooldown timing.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testSupConfig() registry.SupervisorConfig {
	return registry.SupervisorConfig{MaxConsecutiveFailures: 3, BrokenCooldown: 30 * time.Second}
}

func TestSupervisedFactorySuccessPassesThroughAndResetsCounter(t *testing.T) {
	f := &scriptedFactory{}
	sf := registry.NewSupervisedFactory(f, testSupConfig())

	sess, err := sf.New(context.Background())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if sess == nil {
		t.Fatal("New returned a nil session")
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("delegate called %d times, want 1", got)
	}

	// A couple of failures (below the threshold) followed by a success must reset
	// the consecutive-failure count, so the supervisor never trips.
	f.failing.Store(true)
	for range 2 {
		if _, err := sf.New(context.Background()); err == nil {
			t.Fatal("expected a delegate error while failing")
		}
	}
	f.failing.Store(false)
	if _, err := sf.New(context.Background()); err != nil {
		t.Fatalf("New after recovery failed: %v", err)
	}
	// Two more failures still must not trip (the counter was reset by the success).
	f.failing.Store(true)
	for range 2 {
		if _, err := sf.New(context.Background()); !errors.Is(err, errSpawn) {
			t.Fatalf("got %v, want the delegate error (not broken yet)", err)
		}
	}
}

func TestSupervisedFactoryTripsBrokenAfterMaxFailures(t *testing.T) {
	f := &scriptedFactory{}
	f.failing.Store(true)
	sf := registry.NewSupervisedFactory(f, testSupConfig())

	// The first MaxConsecutiveFailures calls each reach the delegate and return
	// its error. The Nth trips the breaker.
	for i := range 3 {
		_, err := sf.New(context.Background())
		if !errors.Is(err, errSpawn) {
			t.Fatalf("call %d: got %v, want the delegate error", i, err)
		}
	}
	if got := f.calls.Load(); got != 3 {
		t.Fatalf("delegate called %d times, want 3", got)
	}

	// Now broken: New must fast-fail with ErrBroken WITHOUT calling the delegate.
	before := f.calls.Load()
	_, err := sf.New(context.Background())
	if !errors.Is(err, registry.ErrBroken) {
		t.Errorf("got %v, want ErrBroken", err)
	}
	if got := f.calls.Load(); got != before {
		t.Errorf("delegate called while broken (calls went %d -> %d)", before, got)
	}
}

func TestSupervisedFactoryProbesAfterCooldown(t *testing.T) {
	f := &scriptedFactory{}
	f.failing.Store(true)
	clk := newFakeClock()
	sf := registry.NewSupervisedFactoryForTest(f, testSupConfig(), clk.Now)

	for i := range 3 {
		if _, err := sf.New(context.Background()); !errors.Is(err, errSpawn) {
			t.Fatalf("call %d: got %v, want the delegate error", i, err)
		}
	}
	// Broken now; before cooldown elapses the delegate is not called.
	before := f.calls.Load()
	if _, err := sf.New(context.Background()); !errors.Is(err, registry.ErrBroken) {
		t.Fatalf("got %v, want ErrBroken before cooldown", err)
	}
	if f.calls.Load() != before {
		t.Fatal("delegate called while broken before cooldown")
	}

	// Advance just short of the cooldown: still broken, still no delegate call.
	clk.Advance(29 * time.Second)
	if _, err := sf.New(context.Background()); !errors.Is(err, registry.ErrBroken) {
		t.Fatalf("got %v, want ErrBroken just before cooldown end", err)
	}
	if f.calls.Load() != before {
		t.Fatal("delegate called while broken just before cooldown end")
	}

	// Cross the cooldown: the next New is a single probe that reaches the delegate.
	clk.Advance(2 * time.Second)
	if _, err := sf.New(context.Background()); !errors.Is(err, errSpawn) {
		t.Fatalf("probe: got %v, want the delegate error (probe still failing)", err)
	}
	if got := f.calls.Load(); got != before+1 {
		t.Errorf("delegate not called on probe (calls %d -> %d)", before, got)
	}
}

func TestSupervisedFactorySuccessfulProbeClearsBroken(t *testing.T) {
	f := &scriptedFactory{}
	f.failing.Store(true)
	clk := newFakeClock()
	sf := registry.NewSupervisedFactoryForTest(f, testSupConfig(), clk.Now)

	for range 3 {
		_, _ = sf.New(context.Background())
	}
	if _, err := sf.New(context.Background()); !errors.Is(err, registry.ErrBroken) {
		t.Fatalf("expected broken, got %v", err)
	}

	// The downstream recovers; after the cooldown the probe succeeds and clears
	// the breaker, so subsequent New calls work without short-circuiting.
	f.failing.Store(false)
	clk.Advance(31 * time.Second)
	sess, err := sf.New(context.Background())
	if err != nil {
		t.Fatalf("successful probe returned an error: %v", err)
	}
	if sess == nil {
		t.Fatal("successful probe returned a nil session")
	}
	if _, err := sf.New(context.Background()); err != nil {
		t.Fatalf("New after cleared breaker failed: %v", err)
	}
}

func TestSupervisedFactoryFailedProbeRearmsCooldown(t *testing.T) {
	f := &scriptedFactory{}
	f.failing.Store(true)
	clk := newFakeClock()
	sf := registry.NewSupervisedFactoryForTest(f, testSupConfig(), clk.Now)

	for range 3 {
		_, _ = sf.New(context.Background())
	}
	// Cross the cooldown for the first probe; it fails and must re-arm the breaker.
	clk.Advance(31 * time.Second)
	if _, err := sf.New(context.Background()); !errors.Is(err, errSpawn) {
		t.Fatalf("first probe: got %v, want the delegate error", err)
	}

	// Immediately broken again: the delegate is not called and the cooldown holds.
	before := f.calls.Load()
	if _, err := sf.New(context.Background()); !errors.Is(err, registry.ErrBroken) {
		t.Fatalf("after failed probe: got %v, want ErrBroken", err)
	}
	if f.calls.Load() != before {
		t.Fatal("delegate called while re-armed broken")
	}

	// The cooldown restarted from the failed probe, so a partial advance stays broken.
	clk.Advance(20 * time.Second)
	if _, err := sf.New(context.Background()); !errors.Is(err, registry.ErrBroken) {
		t.Fatalf("re-armed cooldown not honored: got %v, want ErrBroken", err)
	}
	// Crossing the re-armed cooldown allows another probe.
	clk.Advance(11 * time.Second)
	before = f.calls.Load()
	if _, err := sf.New(context.Background()); !errors.Is(err, errSpawn) {
		t.Fatalf("second probe: got %v, want the delegate error", err)
	}
	if f.calls.Load() != before+1 {
		t.Error("second probe did not reach the delegate")
	}
}

func TestSupervisedFactoryAppliesDefaults(t *testing.T) {
	f := &scriptedFactory{}
	f.failing.Store(true)
	// Zero config -> package defaults (a threshold > 1 and a real cooldown).
	sf := registry.NewSupervisedFactory(f, registry.SupervisorConfig{})

	// A single failure must not trip the breaker under the default threshold; the
	// delegate error is returned, not ErrBroken.
	_, err := sf.New(context.Background())
	if !errors.Is(err, errSpawn) {
		t.Fatalf("got %v, want the delegate error under default threshold", err)
	}
	// A second failure also stays under the default threshold (which is > 2).
	if _, err := sf.New(context.Background()); !errors.Is(err, registry.ErrBroken) && !errors.Is(err, errSpawn) {
		t.Fatalf("unexpected error class: %v", err)
	}
	if errors.Is(err, registry.ErrBroken) {
		t.Fatal("default threshold tripped after a single failure")
	}
}

func TestSupervisedFactoryConcurrentNewIsRaceClean(t *testing.T) {
	f := &scriptedFactory{}
	f.failing.Store(true)
	sf := registry.NewSupervisedFactory(f, registry.SupervisorConfig{MaxConsecutiveFailures: 5, BrokenCooldown: time.Hour})

	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sf.New(context.Background())
		}()
	}
	wg.Wait()

	// After a storm of concurrent failures the supervisor is broken and the next
	// call short-circuits with ErrBroken. (Run under -race to catch data races.)
	if _, e := sf.New(context.Background()); !errors.Is(e, registry.ErrBroken) {
		t.Errorf("after concurrent failures got %v, want ErrBroken", e)
	}
}

// Compile-time assertion that the supervisor satisfies the pool's Factory.
var _ registry.Factory = (*registry.SupervisedFactory)(nil)
