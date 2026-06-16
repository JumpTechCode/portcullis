package resilience

import (
	"sync"
	"time"
)

// state is a single breaker's lifecycle state.
type state int

const (
	stateClosed state = iota
	stateOpen
	stateHalfOpen
)

// breaker is a generic single-scope circuit breaker with single-flight
// half-open recovery. It is the building block the manager composes per target
// and per (downstream, tool); it knows nothing about failure classes.
//
// It is safe for concurrent use.
type breaker struct {
	failureThreshold int
	openDuration     time.Duration
	now              func() time.Time

	mu       sync.Mutex
	state    state
	failures int
	openedAt time.Time
}

// allow reports whether a call may proceed and whether this caller is the
// single-flight half-open probe.
//
//   - closed: (true, false)
//   - open, before the open duration elapses: (false, false)
//   - open, after it elapses: transition to half-open and return (true, true) —
//     this caller is the one probe
//   - half-open (a probe is already in flight): (false, false)
func (b *breaker) allow() (ok, isProbe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case stateClosed:
		return true, false
	case stateOpen:
		if b.now().Sub(b.openedAt) >= b.openDuration {
			b.state = stateHalfOpen
			return true, true
		}
		return false, false
	default: // stateHalfOpen
		return false, false
	}
}

// closedNow reports whether the breaker is currently closed without changing
// state. It is used for scopes whose probe is external (the target breaker,
// probed by an MCP ping rather than a user call), so a user call never consumes
// the probe.
func (b *breaker) closedNow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == stateClosed
}

// record applies a call (or probe) outcome. A success closes the breaker and
// clears the failure count. A failure during a half-open probe reopens the
// breaker; a failure while closed trips it once it reaches the threshold.
func (b *breaker) record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.failures = 0
		b.state = stateClosed
		return
	}
	switch b.state {
	case stateHalfOpen:
		b.trip()
	case stateClosed:
		b.failures++
		if b.failures >= b.failureThreshold {
			b.trip()
		}
	default: // stateOpen: already open, nothing to do
	}
}

// trip moves the breaker to open and stamps the time, gating recovery on the
// open duration.
func (b *breaker) trip() {
	b.state = stateOpen
	b.openedAt = b.now()
}
