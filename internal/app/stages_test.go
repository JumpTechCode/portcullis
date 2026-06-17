package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/audit"
	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/pipeline"
	"github.com/JumpTechCode/portcullis/internal/policy"
	"github.com/JumpTechCode/portcullis/internal/redact"
	"github.com/JumpTechCode/portcullis/internal/registry"
	"github.com/JumpTechCode/portcullis/internal/resilience"
)

// returns is a base handler that yields a fixed result and error.
func returns(res *domain.Result, err error) domain.HandlerFunc {
	return func(_ context.Context, _ *domain.Call) (*domain.Result, error) {
		return res, err
	}
}

// allowAll is a base handler that records that it ran and returns a fixed result.
func allowAll(ran *bool) domain.HandlerFunc {
	return func(_ context.Context, _ *domain.Call) (*domain.Result, error) {
		*ran = true
		return &domain.Result{Content: []byte(`{"ok":true}`)}, nil
	}
}

func TestPolicyStageDeniesBeforeDispatch(t *testing.T) {
	eng := policy.New(false, nil) // deny-by-default, no rules
	st := policyStage{decider: eng}

	rec := &domain.AuditRecord{}
	ctx := withRecord(context.Background(), rec)
	call := &domain.Call{
		Client: domain.Identity{ID: "claude-desktop"},
		Tool:   domain.ToolRef{Downstream: "github", Tool: "create_issue"},
	}

	ran := false
	res, err := st.Handle(ctx, call, allowAll(&ran))

	if err == nil {
		t.Fatal("expected a denial error, got nil")
	}
	if res != nil {
		t.Fatalf("expected nil result on denial, got %v", res)
	}
	if ran {
		t.Fatal("denied call must not reach the base handler")
	}
	if rec.Allowed {
		t.Error("audit record should mark a denied call as not allowed")
	}
	if rec.Decision != domain.ReasonDeniedDefault {
		t.Errorf("audit decision = %q, want %q", rec.Decision, domain.ReasonDeniedDefault)
	}
}

func TestPolicyStageAllowsAndDispatches(t *testing.T) {
	eng := policy.New(false, []policy.Rule{
		{Client: "claude-desktop", Allow: []string{"github__create_issue"}},
	})
	st := policyStage{decider: eng}

	rec := &domain.AuditRecord{}
	ctx := withRecord(context.Background(), rec)
	call := &domain.Call{
		Client: domain.Identity{ID: "claude-desktop"},
		Tool:   domain.ToolRef{Downstream: "github", Tool: "create_issue"},
	}

	ran := false
	res, err := st.Handle(ctx, call, allowAll(&ran))

	if err != nil {
		t.Fatalf("expected the allowed call to succeed, got %v", err)
	}
	if !ran {
		t.Fatal("allowed call must reach the base handler")
	}
	if res == nil || string(res.Content) != `{"ok":true}` {
		t.Fatalf("unexpected result %v", res)
	}
	if !rec.Allowed {
		t.Error("audit record should mark an allowed call as allowed")
	}
	if rec.Decision != domain.ReasonAllowed {
		t.Errorf("audit decision = %q, want %q", rec.Decision, domain.ReasonAllowed)
	}
}

func mustRedactor(t *testing.T, secrets []string, patterns []redact.Pattern) *redact.Redactor {
	t.Helper()
	r, err := redact.New(secrets, patterns)
	if err != nil {
		t.Fatalf("building redactor: %v", err)
	}
	return r
}

func TestRedactOutRedactsResultAndCounts(t *testing.T) {
	st := redactOutStage{redactor: mustRedactor(t, []string{"s3cr3t-token"}, nil), maxBytes: 1 << 20}

	rec := &domain.AuditRecord{}
	ctx := withRecord(context.Background(), rec)
	in := &domain.Result{Content: []byte(`{"value":"s3cr3t-token here"}`)}

	res, err := st.Handle(ctx, &domain.Call{}, returns(in, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(res.Content), "s3cr3t-token") {
		t.Errorf("secret survived redaction: %s", res.Content)
	}
	if rec.Redactions != 1 {
		t.Errorf("Redactions = %d, want 1", rec.Redactions)
	}
}

func TestRedactOutRedactsErrorMessage(t *testing.T) {
	st := redactOutStage{redactor: mustRedactor(t, []string{"hunter2"}, nil), maxBytes: 1 << 20}

	rec := &domain.AuditRecord{}
	ctx := withRecord(context.Background(), rec)

	res, err := st.Handle(ctx, &domain.Call{}, returns(nil, errors.New("dial failed for hunter2")))
	if res != nil {
		t.Fatalf("expected nil result, got %v", res)
	}
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error message not redacted: %v", err)
	}
	if rec.Redactions != 1 {
		t.Errorf("Redactions = %d, want 1", rec.Redactions)
	}
}

func TestRedactOutTruncatesOversizedResult(t *testing.T) {
	st := redactOutStage{redactor: mustRedactor(t, nil, nil), maxBytes: 16}

	rec := &domain.AuditRecord{}
	ctx := withRecord(context.Background(), rec)
	big := `{"content":[{"type":"text","text":"` + strings.Repeat("x", 200) + `"}]}`
	in := &domain.Result{Content: []byte(big)}

	res, err := st.Handle(ctx, &domain.Call{}, returns(in, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rec.Truncated {
		t.Error("oversized result should be flagged truncated")
	}
	// The replacement must remain a valid CallToolResult the edge can decode.
	var out mcp.CallToolResult
	if jErr := json.Unmarshal(res.Content, &out); jErr != nil {
		t.Fatalf("truncated content is not valid CallToolResult JSON: %v", jErr)
	}
	if !out.IsError {
		t.Error("truncated result should be marked IsError")
	}
}

// fakeMeter captures the metric calls a stage makes.
type fakeMeter struct {
	calls        []string
	exhausts     map[string]int
	unrecordable int
}

func (m *fakeMeter) RecordCall(client, downstream, decision string, _ time.Duration) {
	m.calls = append(m.calls, client+"|"+downstream+"|"+decision)
}

func (m *fakeMeter) PoolExhausted(downstream string) {
	if m.exhausts == nil {
		m.exhausts = map[string]int{}
	}
	m.exhausts[downstream]++
}

func ghCall() *domain.Call {
	return &domain.Call{
		Client: domain.Identity{ID: "claude-desktop"},
		Tool:   domain.ToolRef{Downstream: "github", Tool: "create_issue"},
	}
}

func TestResilienceFastFailsWhenBreakerOpen(t *testing.T) {
	b := resilience.New(resilience.Config{FailureThreshold: 1})
	b.RecordTransportFailure("github") // trip the target breaker
	st := resilienceStage{breakers: b, meter: &fakeMeter{}, timeout: time.Second}

	rec := &domain.AuditRecord{}
	ctx := withRecord(context.Background(), rec)
	ran := false
	_, err := st.Handle(ctx, ghCall(), func(context.Context, *domain.Call) (*domain.Result, error) {
		ran = true
		return &domain.Result{}, nil
	})
	if err == nil {
		t.Fatal("expected fast-fail with an open breaker")
	}
	if !errors.Is(err, resilience.ErrOpen) {
		t.Errorf("error = %v, want wrapped ErrOpen", err)
	}
	if ran {
		t.Error("an open breaker must not dispatch")
	}
	if rec.BreakerState != "open" {
		t.Errorf("BreakerState = %q, want open", rec.BreakerState)
	}
}

func TestResilienceRecordsClosedOnSuccess(t *testing.T) {
	st := resilienceStage{breakers: resilience.New(resilience.Config{}), meter: &fakeMeter{}, timeout: time.Second}
	rec := &domain.AuditRecord{}
	ctx := withRecord(context.Background(), rec)

	_, err := st.Handle(ctx, ghCall(), returns(&domain.Result{Content: []byte("{}")}, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.BreakerState != "closed" {
		t.Errorf("BreakerState = %q, want closed", rec.BreakerState)
	}
}

func TestResiliencePoolExhaustionDoesNotTripTarget(t *testing.T) {
	b := resilience.New(resilience.Config{FailureThreshold: 1})
	fm := &fakeMeter{}
	st := resilienceStage{breakers: b, meter: fm, timeout: time.Second}

	_, err := st.Handle(withRecord(context.Background(), &domain.AuditRecord{}), ghCall(),
		returns(nil, fmt.Errorf("acquire: %w", registry.ErrPoolExhausted)))
	if err == nil {
		t.Fatal("expected the pool-exhaustion error to propagate")
	}
	if fm.exhausts["github"] != 1 {
		t.Errorf("PoolExhausted count = %d, want 1", fm.exhausts["github"])
	}
	if aErr := b.Allow("github", "create_issue"); aErr != nil {
		t.Errorf("pool exhaustion must not trip the target breaker, got %v", aErr)
	}
}

func TestResilienceTimeoutTripsToolNotTarget(t *testing.T) {
	b := resilience.New(resilience.Config{FailureThreshold: 1})
	st := resilienceStage{breakers: b, meter: &fakeMeter{}, timeout: time.Second}

	_, err := st.Handle(withRecord(context.Background(), &domain.AuditRecord{}), ghCall(),
		returns(nil, fmt.Errorf("call: %w", context.DeadlineExceeded)))
	if err == nil {
		t.Fatal("expected the timeout error to propagate")
	}
	if aErr := b.Allow("github", "other_tool"); aErr != nil {
		t.Errorf("a tool timeout must not down the target breaker, got %v", aErr)
	}
	if aErr := b.Allow("github", "create_issue"); aErr == nil {
		t.Error("a tool timeout should open that tool's breaker")
	}
}

func (m *fakeMeter) AuditUnrecordable() { m.unrecordable++ }

// fakeWriter captures the last record handed to it and returns a fixed error.
type fakeWriter struct {
	got      *domain.AuditRecord
	err      error
	recorded int
}

func (w *fakeWriter) Get() *domain.AuditRecord { return &domain.AuditRecord{} }

func (w *fakeWriter) Record(rec *domain.AuditRecord) error {
	cp := *rec
	w.got = &cp
	w.recorded++
	return w.err
}

// stepClock returns a fixed sequence of times so latency is deterministic.
type stepClock struct {
	times []time.Time
	i     int
}

func (c *stepClock) now() time.Time {
	t := c.times[c.i]
	if c.i < len(c.times)-1 {
		c.i++
	}
	return t
}

func TestAuditStageRecordsCompletedCall(t *testing.T) {
	fw := &fakeWriter{}
	fm := &fakeMeter{}
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	clock := &stepClock{times: []time.Time{base, base.Add(5 * time.Millisecond)}}
	st := auditStage{writer: fw, meter: fm, now: clock.now}

	res, err := st.Handle(context.Background(), ghCall(), func(ctx context.Context, _ *domain.Call) (*domain.Result, error) {
		annotate(ctx, func(rec *domain.AuditRecord) {
			rec.Allowed = true
			rec.Decision = domain.ReasonAllowed
		})
		return &domain.Result{Content: []byte("{}")}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected the result to pass through")
	}
	if fw.recorded != 1 {
		t.Fatalf("recorded %d times, want 1", fw.recorded)
	}
	if fw.got.ClientID != "claude-desktop" || fw.got.ToolName != "github__create_issue" {
		t.Errorf("record identity = %q/%q", fw.got.ClientID, fw.got.ToolName)
	}
	if fw.got.Decision != domain.ReasonAllowed || !fw.got.Allowed {
		t.Errorf("record decision = %q allowed=%v", fw.got.Decision, fw.got.Allowed)
	}
	if fw.got.LatencyMS != 5 {
		t.Errorf("LatencyMS = %d, want 5", fw.got.LatencyMS)
	}
	if len(fm.calls) != 1 || fm.calls[0] != "claude-desktop|github|allowed" {
		t.Errorf("meter calls = %v", fm.calls)
	}
}

func TestAuditStageFailsClosedWhenUnrecordable(t *testing.T) {
	fw := &fakeWriter{err: audit.ErrUnrecordable}
	fm := &fakeMeter{}
	st := auditStage{writer: fw, meter: fm, now: func() time.Time { return time.Time{} }}

	res, err := st.Handle(context.Background(), ghCall(), func(ctx context.Context, _ *domain.Call) (*domain.Result, error) {
		annotate(ctx, func(rec *domain.AuditRecord) { rec.Decision = domain.ReasonDeniedDefault })
		return nil, errors.New("denied")
	})
	if err == nil {
		t.Fatal("an unrecordable security decision must fail closed")
	}
	if res != nil {
		t.Errorf("expected nil result on fail-closed, got %v", res)
	}
	if fm.unrecordable != 1 {
		t.Errorf("AuditUnrecordable count = %d, want 1", fm.unrecordable)
	}
}

func TestRedactInScrubsArgsBeforeDispatch(t *testing.T) {
	st := redactInStage{redactor: mustRedactor(t, []string{"AKIAEXAMPLE"}, nil)}

	call := &domain.Call{Args: json.RawMessage(`{"key":"AKIAEXAMPLE"}`)}
	var seen json.RawMessage
	_, err := st.Handle(context.Background(), call, func(_ context.Context, c *domain.Call) (*domain.Result, error) {
		seen = c.Args
		return &domain.Result{Content: []byte("{}")}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(seen), "AKIAEXAMPLE") {
		t.Errorf("secret reached the downstream in args: %s", seen)
	}
}

func denyDeps(fw *fakeWriter, fm *fakeMeter) *stageDeps {
	return &stageDeps{
		decider:  policy.New(false, nil),
		redactor: mustRedactorNoErr(nil, nil),
		breakers: resilience.New(resilience.Config{}),
		writer:   fw,
		meter:    fm,
		maxBytes: 1 << 20,
		timeout:  time.Second,
		now:      func() time.Time { return time.Time{} },
	}
}

func mustRedactorNoErr(secrets []string, patterns []redact.Pattern) *redact.Redactor {
	r, _ := redact.New(secrets, patterns)
	return r
}

func TestChainDeniesShortCircuitsAndAudits(t *testing.T) {
	fw, fm := &fakeWriter{}, &fakeMeter{}
	ran := false
	chain := pipeline.Chain(buildStages(denyDeps(fw, fm)), func(context.Context, *domain.Call) (*domain.Result, error) {
		ran = true
		return &domain.Result{Content: []byte("{}")}, nil
	})

	_, err := chain(context.Background(), ghCall())
	if err == nil {
		t.Fatal("a denied call should error")
	}
	if ran {
		t.Error("a denied call must never reach dispatch")
	}
	if fw.got == nil || fw.got.Decision != domain.ReasonDeniedDefault || fw.got.Allowed {
		t.Errorf("denial not audited correctly: %+v", fw.got)
	}
}

func TestChainSuccessRedactsAndAudits(t *testing.T) {
	fw, fm := &fakeWriter{}, &fakeMeter{}
	deps := &stageDeps{
		decider:  policy.New(false, []policy.Rule{{Client: "claude-desktop", Allow: []string{"github__create_issue"}}}),
		redactor: mustRedactorNoErr([]string{"leaked"}, nil),
		breakers: resilience.New(resilience.Config{}),
		writer:   fw,
		meter:    fm,
		maxBytes: 1 << 20,
		timeout:  time.Second,
		now:      func() time.Time { return time.Time{} },
	}
	chain := pipeline.Chain(buildStages(deps), func(context.Context, *domain.Call) (*domain.Result, error) {
		return &domain.Result{Content: []byte(`{"x":"leaked value"}`)}, nil
	})

	res, err := chain(context.Background(), ghCall())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(res.Content), "leaked") {
		t.Errorf("result not redacted: %s", res.Content)
	}
	if fw.got == nil || fw.got.Redactions != 1 || !fw.got.Allowed {
		t.Errorf("success not audited correctly: %+v", fw.got)
	}
	if len(fm.calls) != 1 || fm.calls[0] != "claude-desktop|github|allowed" {
		t.Errorf("meter calls = %v", fm.calls)
	}
}
