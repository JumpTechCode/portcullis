package domain_test

import (
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

func TestAllowedDecision(t *testing.T) {
	d := domain.Allowed()
	if !d.Allow {
		t.Error("Allowed() produced a decision that does not allow")
	}
	if d.Reason != domain.ReasonAllowed {
		t.Errorf("Allowed() reason = %q, want %q", d.Reason, domain.ReasonAllowed)
	}
}

func TestDeniedDecision(t *testing.T) {
	d := domain.Denied(domain.ReasonDeniedExplicit)
	if d.Allow {
		t.Error("Denied() produced a decision that allows")
	}
	if d.Reason != domain.ReasonDeniedExplicit {
		t.Errorf("Denied() reason = %q, want %q", d.Reason, domain.ReasonDeniedExplicit)
	}
}

// The zero value must be a denial, so a forgotten or default-constructed
// Decision fails closed rather than silently granting access (deny-by-default).
func TestZeroDecisionDenies(t *testing.T) {
	var d domain.Decision
	if d.Allow {
		t.Error("zero-value Decision allows; deny-by-default requires it to deny")
	}
}
