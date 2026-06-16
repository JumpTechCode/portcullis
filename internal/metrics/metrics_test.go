package metrics_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/JumpTechCode/portcullis/internal/metrics"
)

// scrape exercises the exported HTTP handler and returns the exposition body.
func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func TestNewDoesNotPanicAndIsolatesRegistries(t *testing.T) {
	// Each New must build its own registry so repeated construction (as in
	// tests) never triggers a duplicate-registration panic on a shared default.
	m1 := metrics.New()
	m2 := metrics.New()
	if m1 == nil || m2 == nil {
		t.Fatal("New returned nil")
	}
	if m1 == m2 {
		t.Fatal("New returned the same instance twice")
	}
	if m1.Registry() == m2.Registry() {
		t.Fatal("New must give each instance its own registry")
	}
}

func TestHandlerServesExpositionFormat(t *testing.T) {
	m := metrics.New()
	// Touch each metric so it appears in the exposition output.
	m.RecordCall("alice", "github", "allow", 5*time.Millisecond)
	m.SetDownstreamUp("github", true)
	m.SetBreakerState("github", metrics.BreakerOpen)
	m.SetPoolInUse("github", 3)
	m.PoolExhausted("github")
	m.AuditUnrecordable()

	out := scrape(t, m)

	wantNames := []string{
		"portcullis_calls_total",
		"portcullis_call_latency_seconds",
		"portcullis_downstream_up",
		"portcullis_breaker_state",
		"portcullis_pool_in_use",
		"portcullis_pool_exhausted_total",
		"portcullis_audit_unrecordable_total",
	}
	for _, name := range wantNames {
		if !strings.Contains(out, name) {
			t.Errorf("exposition output missing metric %q", name)
		}
	}
	// Label discipline: the call counter must carry all three labels.
	if !strings.Contains(out, `client="alice"`) ||
		!strings.Contains(out, `downstream="github"`) ||
		!strings.Contains(out, `decision="allow"`) {
		t.Errorf("calls_total missing expected labels in:\n%s", out)
	}
}

func TestRecordCallIncrementsCounterWithLabels(t *testing.T) {
	m := metrics.New()
	m.RecordCall("alice", "github", "allow", 10*time.Millisecond)
	m.RecordCall("alice", "github", "allow", 20*time.Millisecond)
	m.RecordCall("bob", "github", "deny:policy", time.Millisecond)

	const expect = `
# HELP portcullis_calls_total Tool calls processed by the gateway, labelled by client, downstream, and policy decision.
# TYPE portcullis_calls_total counter
portcullis_calls_total{client="alice",decision="allow",downstream="github"} 2
portcullis_calls_total{client="bob",decision="deny:policy",downstream="github"} 1
`
	if err := testutil.CollectAndCompare(m.Registry(), strings.NewReader(expect), "portcullis_calls_total"); err != nil {
		t.Errorf("calls_total mismatch: %v", err)
	}
}

func TestRecordCallObservesLatencyHistogram(t *testing.T) {
	m := metrics.New()
	m.RecordCall("alice", "github", "allow", 30*time.Millisecond)
	m.RecordCall("alice", "github", "allow", 40*time.Millisecond)

	out := scrape(t, m)
	// Two observations against the per-downstream histogram.
	if !strings.Contains(out, `portcullis_call_latency_seconds_count{downstream="github"} 2`) {
		t.Errorf("latency histogram count for github not 2 in:\n%s", out)
	}
	if !strings.Contains(out, `portcullis_call_latency_seconds_bucket`) {
		t.Errorf("latency histogram emitted no buckets in:\n%s", out)
	}
}

func TestSetDownstreamUpOverwrites(t *testing.T) {
	m := metrics.New()
	m.SetDownstreamUp("github", true)
	out := scrape(t, m)
	if !strings.Contains(out, `portcullis_downstream_up{downstream="github"} 1`) {
		t.Errorf("downstream_up not 1 in:\n%s", out)
	}
	// A gauge must overwrite, not accumulate.
	m.SetDownstreamUp("github", false)
	out = scrape(t, m)
	if !strings.Contains(out, `portcullis_downstream_up{downstream="github"} 0`) {
		t.Errorf("downstream_up not 0 after toggle in:\n%s", out)
	}
}

func TestSetBreakerState(t *testing.T) {
	m := metrics.New()
	cases := []struct {
		state int
		want  string
	}{
		{metrics.BreakerClosed, `portcullis_breaker_state{downstream="github"} 0`},
		{metrics.BreakerHalfOpen, `portcullis_breaker_state{downstream="github"} 1`},
		{metrics.BreakerOpen, `portcullis_breaker_state{downstream="github"} 2`},
	}
	for _, tc := range cases {
		m.SetBreakerState("github", tc.state)
		out := scrape(t, m)
		if !strings.Contains(out, tc.want) {
			t.Errorf("breaker_state %d: want %q in:\n%s", tc.state, tc.want, out)
		}
	}
}

func TestSetPoolInUseOverwrites(t *testing.T) {
	m := metrics.New()
	m.SetPoolInUse("github", 5)
	out := scrape(t, m)
	if !strings.Contains(out, `portcullis_pool_in_use{downstream="github"} 5`) {
		t.Errorf("pool_in_use not 5 in:\n%s", out)
	}
	m.SetPoolInUse("github", 2)
	out = scrape(t, m)
	if !strings.Contains(out, `portcullis_pool_in_use{downstream="github"} 2`) {
		t.Errorf("pool_in_use not 2 after update in:\n%s", out)
	}
}

func TestPoolExhaustedIncrements(t *testing.T) {
	m := metrics.New()
	m.PoolExhausted("github")
	m.PoolExhausted("github")
	m.PoolExhausted("gitlab")

	const expect = `
# HELP portcullis_pool_exhausted_total Stdio subprocess pool exhaustion fast-fails by downstream.
# TYPE portcullis_pool_exhausted_total counter
portcullis_pool_exhausted_total{downstream="github"} 2
portcullis_pool_exhausted_total{downstream="gitlab"} 1
`
	if err := testutil.CollectAndCompare(m.Registry(), strings.NewReader(expect), "portcullis_pool_exhausted_total"); err != nil {
		t.Errorf("pool_exhausted_total mismatch: %v", err)
	}
}

func TestAuditUnrecordableIncrements(t *testing.T) {
	m := metrics.New()
	m.AuditUnrecordable()
	m.AuditUnrecordable()
	m.AuditUnrecordable()

	const expect = `
# HELP portcullis_audit_unrecordable_total Security records that could not be audited (fail-closed).
# TYPE portcullis_audit_unrecordable_total counter
portcullis_audit_unrecordable_total 3
`
	if err := testutil.CollectAndCompare(m.Registry(), strings.NewReader(expect), "portcullis_audit_unrecordable_total"); err != nil {
		t.Errorf("audit_unrecordable_total mismatch: %v", err)
	}
}

func TestCollectorsRegistered(t *testing.T) {
	m := metrics.New()
	// Drive one observation into every collector, then assert the registry
	// gathers all seven distinct metric families.
	m.RecordCall("alice", "github", "allow", time.Millisecond)
	m.SetDownstreamUp("github", true)
	m.SetBreakerState("github", metrics.BreakerClosed)
	m.SetPoolInUse("github", 1)
	m.PoolExhausted("github")
	m.AuditUnrecordable()

	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	got := make(map[string]bool, len(families))
	for _, f := range families {
		got[f.GetName()] = true
	}
	want := []string{
		"portcullis_calls_total",
		"portcullis_call_latency_seconds",
		"portcullis_downstream_up",
		"portcullis_breaker_state",
		"portcullis_pool_in_use",
		"portcullis_pool_exhausted_total",
		"portcullis_audit_unrecordable_total",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("registry did not gather metric %q", name)
		}
	}
}

func TestConcurrentUpdatesAreSafe(t *testing.T) {
	// The prometheus collectors are concurrency-safe; this guards the wrapper
	// methods under the race detector.
	m := metrics.New()
	const workers = 8
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				m.RecordCall("alice", "github", "allow", time.Millisecond)
				m.SetDownstreamUp("github", j%2 == 0)
				m.SetBreakerState("github", j%3)
				m.SetPoolInUse("github", j)
				m.PoolExhausted("github")
				m.AuditUnrecordable()
			}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	// After the storm, the counter reflects every recorded call.
	out := scrape(t, m)
	want := fmt.Sprintf(`portcullis_calls_total{client="alice",decision="allow",downstream="github"} %d`, workers*100)
	if !strings.Contains(out, want) {
		t.Errorf("calls_total after concurrent updates: want %q in:\n%s", want, out)
	}
}
