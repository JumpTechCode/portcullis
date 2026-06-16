package resilience_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/resilience"
)

func cfg() resilience.Config {
	return resilience.Config{FailureThreshold: 3, OpenDuration: time.Minute}
}

func TestAllowsByDefault(t *testing.T) {
	b := resilience.New(cfg())
	if err := b.Allow("github", "create_issue"); err != nil {
		t.Errorf("a fresh breaker denied a call: %v", err)
	}
}

func TestTransportFailuresTripTarget(t *testing.T) {
	b := resilience.New(cfg())
	for range 3 {
		b.RecordTransportFailure("github")
	}
	if err := b.Allow("github", "create_issue"); !errors.Is(err, resilience.ErrOpen) {
		t.Errorf("target breaker did not open after threshold transport failures: %v", err)
	}
}

// The point of the failure-class split: a tool that keeps timing out is shed,
// but it must not sink the whole downstream (design §5).
func TestToolTimeoutDoesNotSinkTarget(t *testing.T) {
	b := resilience.New(cfg())
	for range 10 {
		b.RecordToolTimeout("github", "slow_search")
	}
	if err := b.Allow("github", "create_issue"); err != nil {
		t.Errorf("tool timeouts sank the target breaker; create_issue should still work: %v", err)
	}
	if err := b.Allow("github", "slow_search"); !errors.Is(err, resilience.ErrOpen) {
		t.Errorf("the persistently timing-out tool was not shed: %v", err)
	}
}

func TestRecordSuccessResetsTarget(t *testing.T) {
	b := resilience.New(cfg())
	b.RecordTransportFailure("github")
	b.RecordTransportFailure("github")
	b.RecordSuccess("github", "x") // resets the consecutive-failure count
	b.RecordTransportFailure("github")
	b.RecordTransportFailure("github")
	if err := b.Allow("github", "x"); err != nil {
		t.Errorf("a success did not reset the target failure count: %v", err)
	}
}

func TestConcurrentUse(t *testing.T) {
	b := resilience.New(cfg())
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Allow("github", "t")
			b.RecordSuccess("github", "t")
			b.RecordTransportFailure("search")
			b.RecordToolTimeout("github", "t")
			_ = b.AllowProbe("search")
		}()
	}
	wg.Wait()
}
