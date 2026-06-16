package audit

import (
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// line is the on-disk JSONL shape of an audit record. The wire format is owned
// by this package, so domain.AuditRecord stays free of serialization concerns.
type line struct {
	Timestamp  string `json:"timestamp,omitempty"`
	Client     string `json:"client"`
	Tool       string `json:"tool"`
	Decision   string `json:"decision"`
	Allowed    bool   `json:"allowed"`
	LatencyMS  int64  `json:"latency_ms"`
	Breaker    string `json:"breaker_state,omitempty"`
	Redactions int    `json:"redactions"`
	Truncated  bool   `json:"truncated,omitempty"`
	Error      string `json:"error,omitempty"`
}

func toLine(r *domain.AuditRecord) line {
	l := line{
		Client:     r.ClientID,
		Tool:       r.ToolName,
		Decision:   string(r.Decision),
		Allowed:    r.Allowed,
		LatencyMS:  r.LatencyMS,
		Breaker:    r.BreakerState,
		Redactions: r.Redactions,
		Truncated:  r.Truncated,
		Error:      r.Error,
	}
	if !r.Timestamp.IsZero() {
		l.Timestamp = r.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return l
}
