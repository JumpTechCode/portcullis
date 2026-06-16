// This file implements crash-loop storm prevention for any session Factory.
// SupervisedFactory wraps a Factory and, after too many consecutive New
// failures, marks the downstream "broken" and fast-fails for a cooldown instead
// of repeatedly spawning, then allows a single probe attempt once the cooldown
// elapses (design §5, ADR-0005). It is transport-agnostic: it wraps the stdio
// factory today and the future remote-HTTP factory unchanged, so it carries no
// build tag.

package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrBroken is returned by SupervisedFactory.New while the wrapped downstream is
// in its broken cooldown: too many consecutive New failures tripped the breaker,
// so calls fast-fail without spawning until the cooldown elapses (design §5).
var ErrBroken = errors.New("downstream broken: too many consecutive failures")

// SupervisorConfig tunes a SupervisedFactory. A field that is <= 0 falls back to
// the corresponding package default.
type SupervisorConfig struct {
	// MaxConsecutiveFailures is how many consecutive New failures trip the breaker.
	// The failure that reaches this count is the one that marks the downstream
	// broken (it still returns the delegate's error; the next call fast-fails).
	MaxConsecutiveFailures int
	// BrokenCooldown is how long the breaker stays open before allowing a single
	// probe attempt.
	BrokenCooldown time.Duration
}

const (
	defaultMaxConsecutiveFailures = 5
	defaultBrokenCooldown         = 30 * time.Second
)

// SupervisedFactory wraps a Factory with crash-loop storm prevention. While
// healthy it forwards every New to the delegate. After MaxConsecutiveFailures
// consecutive failures it trips a breaker: subsequent New calls fast-fail with
// ErrBroken without touching the delegate until BrokenCooldown elapses. The
// first New after the cooldown is a single probe that calls the delegate again;
// a successful probe clears the breaker, and a failed probe re-arms the cooldown.
//
// It satisfies Factory, so it composes with the pool by wrapping any factory:
//
//	NewPool(NewSupervisedFactory(stdioFactory, supCfg), poolCfg)
//
// It is safe for concurrent use; multiple pool Acquires call New concurrently.
type SupervisedFactory struct {
	delegate Factory
	cfg      SupervisorConfig
	now      func() time.Time

	mu          sync.Mutex
	failures    int
	broken      bool
	brokenUntil time.Time
}

// Compile-time assertion that SupervisedFactory satisfies Factory.
var _ Factory = (*SupervisedFactory)(nil)

// NewSupervisedFactory wraps delegate with crash-loop storm prevention. Zero or
// negative config fields fall back to package defaults. It uses the wall clock;
// tests that need deterministic cooldown timing use NewSupervisedFactoryForTest.
func NewSupervisedFactory(delegate Factory, cfg SupervisorConfig) *SupervisedFactory {
	return newSupervisedFactory(delegate, cfg, time.Now)
}

// NewSupervisedFactoryForTest is NewSupervisedFactory with an injectable clock,
// for deterministic cooldown timing in tests. Production code uses
// NewSupervisedFactory.
func NewSupervisedFactoryForTest(delegate Factory, cfg SupervisorConfig, now func() time.Time) *SupervisedFactory {
	return newSupervisedFactory(delegate, cfg, now)
}

func newSupervisedFactory(delegate Factory, cfg SupervisorConfig, now func() time.Time) *SupervisedFactory {
	if cfg.MaxConsecutiveFailures <= 0 {
		cfg.MaxConsecutiveFailures = defaultMaxConsecutiveFailures
	}
	if cfg.BrokenCooldown <= 0 {
		cfg.BrokenCooldown = defaultBrokenCooldown
	}
	return &SupervisedFactory{delegate: delegate, cfg: cfg, now: now}
}

// New returns a session from the delegate, or storm-prevents. While the breaker
// is open and its cooldown has not elapsed it returns ErrBroken immediately,
// without calling the delegate. Otherwise it calls the delegate: success resets
// the consecutive-failure count and clears the breaker; failure increments the
// count and, on reaching MaxConsecutiveFailures, opens the breaker for
// BrokenCooldown. The first New after the cooldown is a single probe — a
// successful probe clears the breaker, a failed probe re-arms the cooldown.
func (f *SupervisedFactory) New(ctx context.Context) (Session, error) {
	f.mu.Lock()
	if f.broken && f.now().Before(f.brokenUntil) {
		f.mu.Unlock()
		return nil, ErrBroken
	}
	// Either healthy, or broken but past the cooldown: this call proceeds as a
	// normal attempt (and, when broken, serves as the single recovery probe). The
	// lock is released across the delegate call so concurrent New calls — and the
	// downstream spawn itself — are not serialized behind one another.
	f.mu.Unlock()

	sess, err := f.delegate.New(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.failures++
		if f.failures >= f.cfg.MaxConsecutiveFailures {
			f.broken = true
			f.brokenUntil = f.now().Add(f.cfg.BrokenCooldown)
		}
		return nil, fmt.Errorf("supervised factory: %w", err)
	}
	f.failures = 0
	f.broken = false
	return sess, nil
}
