package domain_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// Reset must clear every field so a record returned to a sync.Pool cannot leak
// data from a previous call into the next one (design §5).
func TestAuditRecordReset(t *testing.T) {
	r := &domain.AuditRecord{
		Timestamp:    time.Date(2026, 6, 16, 3, 0, 0, 0, time.UTC),
		ClientID:     "ci-bot",
		ToolName:     "github__create_issue",
		Decision:     domain.ReasonAllowed,
		Allowed:      true,
		LatencyMS:    42,
		BreakerState: "closed",
		Redactions:   3,
		Truncated:    true,
		Error:        "boom",
	}

	r.Reset()

	if !reflect.DeepEqual(*r, domain.AuditRecord{}) {
		t.Errorf("after Reset, record = %+v, want zero value", *r)
	}
}
