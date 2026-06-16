package domain

// ReasonCode is a stable, machine-readable code attached to a policy decision.
// Every decision carries one so outcomes are auditable and metered, and no
// outcome is ever silently dropped (design §7: "reason code + metric").
type ReasonCode string

const (
	// ReasonAllowed: an explicit allow rule matched the client and tool.
	ReasonAllowed ReasonCode = "allowed"
	// ReasonDeniedDefault: no rule allowed the tool, so deny-by-default applies.
	ReasonDeniedDefault ReasonCode = "denied_default"
	// ReasonDeniedExplicit: a deny rule matched, overriding any allow rule
	// (most-restrictive-wins).
	ReasonDeniedExplicit ReasonCode = "denied_explicit"
)

// Decision is the outcome of evaluating a client's access to a tool. The zero
// value is a denial, so a default-constructed Decision fails closed; to express
// an explicit deny, use Denied with a reason rather than a bare zero value, so
// the outcome always carries a reason code (design §7).
type Decision struct {
	Allow  bool
	Reason ReasonCode
}

// Allowed builds an allow decision.
func Allowed() Decision { return Decision{Allow: true, Reason: ReasonAllowed} }

// Denied builds a deny decision carrying the given reason.
func Denied(reason ReasonCode) Decision { return Decision{Allow: false, Reason: reason} }
