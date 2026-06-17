package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JumpTechCode/portcullis/internal/audit"
	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/redact"
	"github.com/JumpTechCode/portcullis/internal/registry"
	"github.com/JumpTechCode/portcullis/internal/resilience"
)

// deniedError is returned when policy denies a call. It carries the denied tool
// and the reason code so the edge can surface a clean MCP error and the audit
// trail keeps the reason.
type deniedError struct {
	tool   domain.ToolRef
	reason domain.ReasonCode
}

func (e *deniedError) Error() string {
	return fmt.Sprintf("policy denied %q: %s", e.tool.Namespaced(), e.reason)
}

// The per-call security chain is a list of domain.Stage middlewares wrapped
// around the per-session dispatcher, assembled here at the composition root so
// that reordering or inserting a stage is a wiring change rather than an edit to
// any concrete package (design §3). Each stage depends only on injected behavior
// (a Decider, a Redactor, the breakers), which keeps it unit-testable in
// isolation and keeps the concrete packages free of pipeline plumbing.
//
// Stages share per-call metadata by writing through a *domain.AuditRecord
// carried in the context. The outermost audit stage seeds the record and writes
// it out; inner stages fill the fields they own (the policy decision, the
// redaction count). A stage run without a record in context simply skips the
// bookkeeping, so every stage is testable without an audit writer.

// recordKey is the unexported context key under which the audit stage stores the
// in-progress record for inner stages to annotate.
type recordKey struct{}

// withRecord returns a copy of ctx carrying rec for inner stages to fill.
func withRecord(ctx context.Context, rec *domain.AuditRecord) context.Context {
	return context.WithValue(ctx, recordKey{}, rec)
}

// recordFrom returns the audit record the outer stage placed in ctx, if any.
func recordFrom(ctx context.Context) (*domain.AuditRecord, bool) {
	rec, ok := ctx.Value(recordKey{}).(*domain.AuditRecord)
	return rec, ok
}

// annotate runs f against the context's audit record when one is present.
func annotate(ctx context.Context, f func(*domain.AuditRecord)) {
	if rec, ok := recordFrom(ctx); ok {
		f(rec)
	}
}

// policyStage enforces the deny-by-default access policy before a call reaches
// the downstream. A denial short-circuits the chain: the base handler is never
// invoked, and the decision is recorded for audit (design §8 hot path).
type policyStage struct {
	decider domain.Decider
}

// Handle decides whether the call is permitted. A denied call returns a clean
// error without dispatching; an allowed call proceeds to next. Either way the
// decision's reason code is written to the audit record.
func (s policyStage) Handle(ctx context.Context, call *domain.Call, next domain.HandlerFunc) (*domain.Result, error) {
	decision := s.decider.Decide(call.Client, call.Tool)
	annotate(ctx, func(rec *domain.AuditRecord) {
		rec.Allowed = decision.Allow
		rec.Decision = decision.Reason
	})
	if !decision.Allow {
		return nil, &deniedError{tool: call.Tool, reason: decision.Reason}
	}
	return next(ctx, call)
}

// redactOutStage scrubs injected secret values and PII from a call's result and
// from any error before they leave the gateway or are written to the audit log
// (design §8). It also enforces the result size cap (ADR-0011): a result whose
// marshaled form exceeds maxBytes is replaced with a small, valid truncation
// marker rather than buffered and scanned whole, and the call is flagged
// truncated for audit. The redaction count (result plus error) is recorded.
type redactOutStage struct {
	redactor *redact.Redactor
	maxBytes int
}

// Handle dispatches, then redacts the result and error on the way back. It sits
// just inside the audit stage so the record observes the post-redaction view,
// and outside resilience so the breaker classifies the raw transport error
// before its message is rewritten.
func (s redactOutStage) Handle(ctx context.Context, call *domain.Call, next domain.HandlerFunc) (*domain.Result, error) {
	res, err := next(ctx, call)

	total := 0
	if res != nil && len(res.Content) > 0 {
		if s.maxBytes > 0 && len(res.Content) > s.maxBytes {
			res = truncatedResult(len(res.Content), s.maxBytes)
			annotate(ctx, func(rec *domain.AuditRecord) { rec.Truncated = true })
		} else {
			redacted, n := s.redactor.Redact(res.Content)
			res = &domain.Result{Content: redacted, IsError: res.IsError}
			total += n
		}
	}
	if err != nil {
		redacted, n := s.redactor.Redact([]byte(err.Error()))
		err = errors.New(string(redacted))
		total += n
	}
	if total > 0 {
		annotate(ctx, func(rec *domain.AuditRecord) { rec.Redactions += total })
	}
	return res, err
}

// truncatedResult builds a valid CallToolResult standing in for an oversized
// payload, so the client receives well-formed JSON it can decode and the
// oversized blob is neither scanned nor forwarded.
func truncatedResult(size, limit int) *domain.Result {
	msg := fmt.Sprintf("[result truncated: %d bytes exceeded the %d-byte cap]", size, limit)
	result := &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
	body, err := json.Marshal(result)
	if err != nil {
		// Marshaling a fixed text result cannot fail; fall back defensively.
		body = []byte(`{"content":[{"type":"text","text":"[result truncated]"}],"isError":true}`)
	}
	return &domain.Result{Content: body, IsError: true}
}

// meter is the subset of the metrics surface the stages emit to. The composition
// root passes *metrics.Metrics; tests pass a fake. Keeping it an interface lets a
// stage be exercised without standing up a Prometheus registry.
type meter interface {
	RecordCall(client, downstream, decision string, latency time.Duration)
	PoolExhausted(downstream string)
}

// fullMeter adds the fail-closed audit counter the audit stage needs on top of
// the call/exhaustion meter the resilience stage uses.
type fullMeter interface {
	meter
	AuditUnrecordable()
}

// redactInStage scrubs injected secret values and PII from a call's arguments
// before they reach the downstream, so a credential or PII value a client
// included in its arguments is not forwarded verbatim (design §8 hot path). It
// replaces the call's arguments with freshly redacted bytes rather than mutating
// them in place. Inbound redactions are not counted toward the audit record,
// whose Redactions field is defined over the outbound result, error, and
// notifications.
type redactInStage struct {
	redactor *redact.Redactor
}

// Handle redacts the arguments, then dispatches.
func (s redactInStage) Handle(ctx context.Context, call *domain.Call, next domain.HandlerFunc) (*domain.Result, error) {
	if len(call.Args) > 0 {
		if redacted, n := s.redactor.Redact(call.Args); n > 0 {
			call.Args = redacted
		}
	}
	return next(ctx, call)
}

// resilienceStage wraps dispatch with the failure-class-aware breaker and a
// per-call timeout (design §5, ADR-0005). It fast-fails before dispatch when the
// breaker is open, and on the way back classifies the outcome: a timeout counts
// against the per-(downstream, tool) breaker, a transport error against the
// per-target breaker, a client cancellation against neither (disconnect is not a
// failure, ADR-0008), and pool exhaustion is a load-shedding signal metered
// separately rather than charged to the target's health.
type resilienceStage struct {
	breakers *resilience.Breakers
	meter    meter
	timeout  time.Duration
}

// Handle gates the call on the breaker, bounds it with the call timeout, and
// records the outcome's failure class.
func (s resilienceStage) Handle(ctx context.Context, call *domain.Call, next domain.HandlerFunc) (*domain.Result, error) {
	ds, tool := call.Tool.Downstream, call.Tool.Tool
	if err := s.breakers.Allow(ds, tool); err != nil {
		annotate(ctx, func(rec *domain.AuditRecord) { rec.BreakerState = "open" })
		return nil, err
	}
	annotate(ctx, func(rec *domain.AuditRecord) { rec.BreakerState = "closed" })

	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}

	res, err := next(ctx, call)
	s.classify(ds, tool, err)
	return res, err
}

// classify records the call outcome against the right breaker scope.
func (s resilienceStage) classify(downstream, tool string, err error) {
	switch {
	case err == nil:
		s.breakers.RecordSuccess(downstream, tool)
	case errors.Is(err, context.Canceled):
		// A client cancellation (or disconnect) is not a downstream fault.
	case errors.Is(err, context.DeadlineExceeded):
		s.breakers.RecordToolTimeout(downstream, tool)
	case errors.Is(err, registry.ErrPoolExhausted):
		s.meter.PoolExhausted(downstream)
	case errors.Is(err, registry.ErrBroken):
		// The supervisor's own breaker is already shedding spawns; the target
		// breaker takes no further action.
	default:
		s.breakers.RecordTransportFailure(downstream)
	}
}

// errUnauditable is returned to the client when a security-relevant call could
// not be audited and so must not be allowed to proceed (design §5 fail-closed).
var errUnauditable = errors.New("portcullis: request rejected: security audit unavailable")

// auditWriter is the subset of the audit pipeline the audit stage drives. The
// composition root passes *audit.Writer; tests pass a fake.
type auditWriter interface {
	Get() *domain.AuditRecord
	Record(rec *domain.AuditRecord) error
}

// auditStage is the outermost stage. It seeds the per-call audit record that
// inner stages annotate, times the call, then writes the post-redaction record
// and meters the call. When a security record cannot be buffered the audit
// pipeline returns ErrUnrecordable; the stage then fails the call closed so an
// un-auditable security decision never reaches the client (design §5).
type auditStage struct {
	writer auditWriter
	meter  fullMeter
	now    func() time.Time
}

// Handle records every call. It captures the metric labels before handing the
// record to the writer, since the writer may recycle the record as soon as
// Record returns.
func (s auditStage) Handle(ctx context.Context, call *domain.Call, next domain.HandlerFunc) (*domain.Result, error) {
	rec := s.writer.Get()
	start := s.now()
	rec.Timestamp = start
	rec.ClientID = call.Client.ID
	rec.ToolName = call.Tool.Namespaced()

	res, err := next(withRecord(ctx, rec), call)

	latency := s.now().Sub(start)
	rec.LatencyMS = latency.Milliseconds()
	if err != nil {
		rec.Error = err.Error()
	}

	client, downstream, decision := rec.ClientID, call.Tool.Downstream, string(rec.Decision)
	recErr := s.writer.Record(rec)
	s.meter.RecordCall(client, downstream, decision, latency)
	if errors.Is(recErr, audit.ErrUnrecordable) {
		s.meter.AuditUnrecordable()
		return nil, errUnauditable
	}
	return res, err
}

// stageDeps gathers the behavior the per-call security chain is built from. The
// composition root fills it from the loaded config and the constructed concretes.
type stageDeps struct {
	decider  domain.Decider
	redactor *redact.Redactor
	breakers *resilience.Breakers
	writer   auditWriter
	meter    fullMeter
	maxBytes int
	timeout  time.Duration
	now      func() time.Time
}

// buildStages assembles the per-call security chain in outermost-to-innermost
// order, ready to wrap a per-session dispatcher via pipeline.Chain (design §4
// hot path):
//
//		audit → redact(out) → policy → redact(in) → resilience → dispatch
//
//	  - audit is outermost so it observes the final, post-redaction view of every
//	    call, including one denied or fast-failed before dispatch.
//	  - redact(out) sits just inside audit so the result and any error are scrubbed
//	    before they leave the gateway or are logged, and outside resilience so the
//	    breaker classifies the raw transport error before its message is rewritten.
//	  - policy denies before any downstream work; redact(in) scrubs arguments before
//	    dispatch; resilience gates and times the dispatch itself.
func buildStages(d *stageDeps) []domain.Stage {
	return []domain.Stage{
		auditStage{writer: d.writer, meter: d.meter, now: d.now},
		redactOutStage{redactor: d.redactor, maxBytes: d.maxBytes},
		policyStage{decider: d.decider},
		redactInStage{redactor: d.redactor},
		resilienceStage{breakers: d.breakers, meter: d.meter, timeout: d.timeout},
	}
}
