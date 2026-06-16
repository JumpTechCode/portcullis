package resilience

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The target breaker recovers only via an external ping probe (single-flight);
// user calls never probe it, even after the open duration elapses (ADR-0005).
func TestTargetProbeSingleFlightAndRecovery(t *testing.T) {
	clk := newClock()
	b := newWithClock(Config{FailureThreshold: 1, OpenDuration: time.Minute}, clk.now)

	b.RecordTransportFailure("github") // trips the target
	if err := b.Allow("github", "x"); !errors.Is(err, ErrOpen) {
		t.Fatal("target should be open after tripping")
	}

	clk.advance(time.Minute)
	if err := b.Allow("github", "x"); !errors.Is(err, ErrOpen) {
		t.Error("a user call was allowed to probe the target; only the ping probe may")
	}

	if !b.AllowProbe("github") {
		t.Error("ping probe was not allowed after the open duration elapsed")
	}
	if b.AllowProbe("github") {
		t.Error("a second concurrent ping probe was allowed (single-flight violated)")
	}

	b.RecordProbe("github", true) // probe succeeded
	if err := b.Allow("github", "x"); err != nil {
		t.Errorf("target did not recover after a successful probe: %v", err)
	}
}

func TestTargetProbeFailureKeepsTargetOpen(t *testing.T) {
	clk := newClock()
	b := newWithClock(Config{FailureThreshold: 1, OpenDuration: time.Minute}, clk.now)
	b.RecordTransportFailure("github")
	clk.advance(time.Minute)
	if !b.AllowProbe("github") {
		t.Fatal("expected a probe to be allowed")
	}
	b.RecordProbe("github", false) // probe failed
	if err := b.Allow("github", "x"); !errors.Is(err, ErrOpen) {
		t.Error("target recovered despite a failed probe")
	}
}

// A timing-out tool recovers via a single-flight user call after the open
// duration (a ping cannot exercise a specific tool's timeout behavior).
func TestToolRecoversViaSingleFlightCall(t *testing.T) {
	clk := newClock()
	b := newWithClock(Config{FailureThreshold: 1, OpenDuration: time.Minute}, clk.now)

	b.RecordToolTimeout("github", "slow")
	if err := b.Allow("github", "slow"); !errors.Is(err, ErrOpen) {
		t.Fatal("tool breaker should be open after tripping")
	}

	clk.advance(time.Minute)
	if err := b.Allow("github", "slow"); err != nil {
		t.Error("tool did not allow a probe call after the open duration elapsed")
	}
	if err := b.Allow("github", "slow"); !errors.Is(err, ErrOpen) {
		t.Error("tool allowed a second concurrent probe call (single-flight violated)")
	}

	b.RecordSuccess("github", "slow")
	if err := b.Allow("github", "slow"); err != nil {
		t.Errorf("tool did not recover after a successful probe call: %v", err)
	}
}

// Under a stampede of concurrent ping probes, exactly one may be granted — the
// single-flight invariant that prevents a thundering herd on a recovering server.
func TestConcurrentProbeIsSingleFlight(t *testing.T) {
	clk := newClock()
	b := newWithClock(Config{FailureThreshold: 1, OpenDuration: time.Minute}, clk.now)
	b.RecordTransportFailure("github")
	clk.advance(time.Minute)

	var granted atomic.Int32
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.AllowProbe("github") {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()

	if granted.Load() != 1 {
		t.Errorf("concurrent AllowProbe granted %d probes, want exactly 1", granted.Load())
	}
}

func TestConfigDefaultsApply(t *testing.T) {
	b := newWithClock(Config{}, time.Now) // zero values -> defaults
	if err := b.Allow("a", "b"); err != nil {
		t.Fatalf("default-configured breaker denied a call: %v", err)
	}
	for range DefaultFailureThreshold {
		b.RecordTransportFailure("a")
	}
	if err := b.Allow("a", "b"); !errors.Is(err, ErrOpen) {
		t.Error("default failure threshold was not applied")
	}
}
