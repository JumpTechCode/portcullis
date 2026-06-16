package registry_test

import (
	"encoding/json"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/registry"
)

// This file holds the transport-neutral test helpers shared by the stdio and
// HTTP factory suites. It carries no build constraint: the stdio fake server is
// unix-only, but these helpers (and the HTTP suite that also uses them) build on
// every platform, mirroring the production split between the untagged
// session.go and the unix-gated stdio.go.

// greetIn is the input to the greet tool exposed by both fake downstream
// servers.
type greetIn struct {
	Name string `json:"name"`
}

// downstream type-asserts a pool Session to the richer DownstreamSession the
// factory returns, failing the test if the assertion does not hold.
func downstream(t *testing.T, s registry.Session) registry.DownstreamSession {
	t.Helper()
	ds, ok := s.(registry.DownstreamSession)
	if !ok {
		t.Fatalf("factory session %T does not implement DownstreamSession", s)
	}
	return ds
}

// containsString reports whether the raw JSON result content contains the given
// substring after decoding (the content is the marshaled CallToolResult).
func containsString(t *testing.T, content json.RawMessage, sub string) bool {
	t.Helper()
	return len(content) > 0 && indexOf(string(content), sub) >= 0
}

// indexOf is a tiny strings.Index wrapper kept local to avoid importing strings
// solely for one call in tests.
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	if sub == "" {
		return 0
	}
	return -1
}
