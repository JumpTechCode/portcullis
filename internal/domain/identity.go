package domain

// Identity is an authenticated gateway client, resolved from its API key. Policy
// is evaluated against this identity.
type Identity struct {
	// ID is the configured client identifier (for example "claude-desktop").
	ID string
}

// String returns the client identifier, so an Identity formats readably in logs
// and errors.
func (i Identity) String() string { return i.ID }
