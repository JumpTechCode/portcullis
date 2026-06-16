package domain

import (
	"fmt"
	"strings"
)

// NamespaceSeparator joins a downstream name and a tool name into the single
// client-facing tool name the gateway presents (for example
// "github__create_issue").
const NamespaceSeparator = "__"

// ToolRef identifies a tool on a specific downstream server.
type ToolRef struct {
	// Downstream is the configured downstream server name (for example "github").
	Downstream string
	// Tool is the tool's own name on that downstream (for example "create_issue").
	Tool string
}

// Namespaced returns the client-facing namespaced tool name.
func (t ToolRef) Namespaced() string {
	return t.Downstream + NamespaceSeparator + t.Tool
}

// String returns the namespaced tool name, so a ToolRef formats readably in
// logs and errors.
func (t ToolRef) String() string { return t.Namespaced() }

// ParseToolRef splits a client-facing namespaced tool name back into its
// downstream and tool components.
//
// The name must contain exactly one separator, with a non-empty downstream and
// a non-empty tool. Any other shape — no separator, an empty side, or a name
// whose downstream or tool itself contains the separator — is rejected, so a
// malformed or ambiguous name can never be silently mis-routed (design §7).
func ParseToolRef(namespaced string) (ToolRef, error) {
	parts := strings.Split(namespaced, NamespaceSeparator)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ToolRef{}, fmt.Errorf(
			"invalid namespaced tool name %q: want exactly one %q separating a non-empty downstream and tool",
			namespaced, NamespaceSeparator,
		)
	}
	return ToolRef{Downstream: parts[0], Tool: parts[1]}, nil
}
