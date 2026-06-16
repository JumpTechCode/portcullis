// Package resilience provides failure-class-aware circuit breaking (design §5,
// ADR-0005).
//
// Transport/connection failures are server-health signals and trip a breaker
// scoped to the downstream target. Individual tool-call timeouts are
// tool-specific: they are tracked per (downstream, tool) and never sink the
// target breaker, so a slow tool cannot down its siblings. The target breaker
// recovers through an external, single-flight MCP ping probe; a per-tool breaker
// recovers through a single-flight user call, since a ping cannot exercise a
// specific tool's timeout behavior.
package resilience

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrOpen is returned by Allow when a call is shed because the relevant breaker
// (target or tool) is open. Callers match it with errors.Is.
var ErrOpen = errors.New("circuit breaker open")

// Default breaker tuning, used when a Config field is left zero. Breaker tuning
// is not part of the V1 config surface; these constants are the policy.
const (
	DefaultFailureThreshold = 5
	DefaultOpenDuration     = 30 * time.Second
)

// Config tunes the breakers. Zero fields fall back to the package defaults.
type Config struct {
	// FailureThreshold is the number of consecutive failures that trips a breaker.
	FailureThreshold int
	// OpenDuration is how long a breaker stays open before a probe is allowed.
	OpenDuration time.Duration
}

// Breakers manages per-target and per-(downstream, tool) breakers and applies
// the failure-class rules. It is safe for concurrent use.
type Breakers struct {
	cfg Config
	now func() time.Time

	mu      sync.Mutex
	targets map[string]*breaker
	tools   map[string]*breaker
}

// New builds a Breakers manager with the given configuration.
func New(cfg Config) *Breakers { return newWithClock(cfg, time.Now) }

func newWithClock(cfg Config, now func() time.Time) *Breakers {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = DefaultFailureThreshold
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = DefaultOpenDuration
	}
	return &Breakers{
		cfg:     cfg,
		now:     now,
		targets: make(map[string]*breaker),
		tools:   make(map[string]*breaker),
	}
}

// Allow reports whether a call to (downstream, tool) may proceed. It fails with
// ErrOpen if the target breaker is open (the downstream is unhealthy) or if the
// tool's breaker is open (the tool is persistently timing out). User calls never
// probe the target — only AllowProbe does — but a user call may serve as the
// single-flight probe for a recovering tool breaker.
func (m *Breakers) Allow(downstream, tool string) error {
	if !m.target(downstream).closedNow() {
		return fmt.Errorf("downstream %q: %w", downstream, ErrOpen)
	}
	if ok, _ := m.tool(downstream, tool).allow(); !ok {
		return fmt.Errorf("tool %q/%q: %w", downstream, tool, ErrOpen)
	}
	return nil
}

// RecordSuccess records a successful call, resetting both the target and the
// tool breakers (closing a tool breaker that was probing).
func (m *Breakers) RecordSuccess(downstream, tool string) {
	m.target(downstream).record(true)
	m.tool(downstream, tool).record(true)
}

// RecordTransportFailure records a transport/connection failure, a server-health
// signal that counts against the target breaker only.
func (m *Breakers) RecordTransportFailure(downstream string) {
	m.target(downstream).record(false)
}

// RecordToolTimeout records a tool-call timeout, which counts against the
// (downstream, tool) breaker only and never against the target.
func (m *Breakers) RecordToolTimeout(downstream, tool string) {
	m.tool(downstream, tool).record(false)
}

// AllowProbe reports whether the caller may run the single-flight ping probe for
// a target breaker. It returns true at most once per open period, when the open
// duration has elapsed; the result must be reported with RecordProbe.
func (m *Breakers) AllowProbe(downstream string) bool {
	ok, isProbe := m.target(downstream).allow()
	return ok && isProbe
}

// RecordProbe reports the outcome of a ping probe started via AllowProbe: success
// closes the target breaker, failure reopens it.
func (m *Breakers) RecordProbe(downstream string, success bool) {
	m.target(downstream).record(success)
}

func (m *Breakers) target(downstream string) *breaker {
	return m.getOrCreate(m.targets, downstream)
}

func (m *Breakers) tool(downstream, tool string) *breaker {
	return m.getOrCreate(m.tools, downstream+"\x00"+tool)
}

// getOrCreate returns the breaker for key, creating it on first use. The maps
// grow one entry per distinct downstream and (downstream, tool) seen and are
// never evicted; this stays bounded because the router resolves a call against
// the known, deny-by-default catalog before resilience is consulted (§4 hot
// path), so Allow is never reached with an unbounded set of tool names.
func (m *Breakers) getOrCreate(set map[string]*breaker, key string) *breaker {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := set[key]
	if !ok {
		b = &breaker{
			failureThreshold: m.cfg.FailureThreshold,
			openDuration:     m.cfg.OpenDuration,
			now:              m.now,
		}
		set[key] = b
	}
	return b
}
