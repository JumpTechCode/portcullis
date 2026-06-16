// Package aggregate presents many downstream servers as one tool catalog.
//
// It is the gateway's pure topology layer. Given the raw tool listings reported
// by each downstream a client may use, it namespaces every tool name to keep it
// collision-safe (downstream__tool), merges the listings into one deterministic
// [domain.Catalog], cursor-paginates that catalog with an opaque, name-stable
// token, and resolves a namespaced call back to the downstream that owns it.
//
// This package never talks to a downstream session; it operates only on the
// listings handed to it, so it is deterministic and trivially testable. Routing
// of the resolved call to a live session belongs to the registry layer.
//
// Validation fails fast (design §7): a downstream or tool name that contains the
// namespace separator, an empty name, a duplicate downstream, or a duplicate
// tool within a downstream is rejected at [Build] rather than risking silent
// mis-routing later.
package aggregate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// Tool is a single tool exactly as a downstream reports it, before namespacing.
type Tool struct {
	// Name is the tool's own name on its downstream (for example "create_issue").
	Name string
	// Description is the human-readable description the downstream provides.
	Description string
	// InputSchema is the tool's JSON Schema for its arguments, carried through
	// verbatim. It is treated as immutable; callers must not mutate the bytes.
	InputSchema json.RawMessage
}

// Listing is one downstream's complete set of reported tools.
type Listing struct {
	// Downstream is the configured downstream server name (for example "github").
	Downstream string
	// Tools is every tool that downstream reported, un-namespaced.
	Tools []Tool
}

// Build namespaces every tool as "downstream__tool" and merges all listings into
// a single, deterministically ordered [domain.Catalog].
//
// It fails fast, returning an error and an empty catalog, if any downstream name
// or tool name is empty or contains the namespace separator [domain.NamespaceSeparator],
// if two listings share a downstream name, or if a downstream reports the same
// tool twice. Rejecting these at aggregation time prevents an ambiguous
// namespaced name from ever mis-routing a later call (design §7).
//
// The returned catalog is sorted by namespaced name, so the same input always
// yields the same output.
func Build(listings []Listing) (domain.Catalog, error) {
	tools := make([]domain.Tool, 0)
	seenDownstreams := make(map[string]bool, len(listings))

	for _, l := range listings {
		if l.Downstream == "" {
			return domain.Catalog{}, fmt.Errorf("aggregate: downstream name must not be empty")
		}
		if strings.Contains(l.Downstream, domain.NamespaceSeparator) {
			return domain.Catalog{}, fmt.Errorf(
				"aggregate: downstream name %q must not contain the namespace separator %q",
				l.Downstream, domain.NamespaceSeparator,
			)
		}
		if seenDownstreams[l.Downstream] {
			return domain.Catalog{}, fmt.Errorf("aggregate: duplicate downstream %q", l.Downstream)
		}
		seenDownstreams[l.Downstream] = true

		seenTools := make(map[string]bool, len(l.Tools))
		for _, tool := range l.Tools {
			if tool.Name == "" {
				return domain.Catalog{}, fmt.Errorf(
					"aggregate: downstream %q reported a tool with an empty name", l.Downstream,
				)
			}
			if strings.Contains(tool.Name, domain.NamespaceSeparator) {
				return domain.Catalog{}, fmt.Errorf(
					"aggregate: downstream %q tool name %q must not contain the namespace separator %q",
					l.Downstream, tool.Name, domain.NamespaceSeparator,
				)
			}
			if seenTools[tool.Name] {
				return domain.Catalog{}, fmt.Errorf(
					"aggregate: downstream %q reported duplicate tool %q", l.Downstream, tool.Name,
				)
			}
			seenTools[tool.Name] = true

			tools = append(tools, domain.Tool{
				Ref:         domain.ToolRef{Downstream: l.Downstream, Tool: tool.Name},
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
		}
	}

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Ref.Namespaced() < tools[j].Ref.Namespaced()
	})
	return domain.Catalog{Tools: tools}, nil
}

// Page returns one page of catalog, sized at most size, plus an opaque cursor for
// the next page ("" when the returned page is the last one).
//
// Pagination is name-based and stateless: the catalog is read in sorted
// namespaced-name order, and the page is the first size tools whose name is
// strictly greater than the name encoded in cursor. An empty cursor starts at
// the beginning. Because the cursor encodes a name rather than an index, a page
// stays correct even if tools are added or removed between requests: the next
// page always resumes strictly after the last name already returned, never
// skipping or repeating a surviving tool.
//
// The cursor is an opaque base64url token; a cursor that is not valid base64url
// is rejected with an error. size must be positive; a non-positive size is
// rejected with an error rather than silently defaulted, so a caller's mistake
// surfaces immediately.
func Page(catalog domain.Catalog, cursor string, size int) (page domain.Catalog, next string, err error) {
	if size <= 0 {
		return domain.Catalog{}, "", fmt.Errorf("aggregate: page size must be positive, got %d", size)
	}

	after, err := decodeCursor(cursor)
	if err != nil {
		return domain.Catalog{}, "", err
	}

	// Read in sorted order so the name-based cursor is well defined regardless of
	// how the catalog happens to be ordered in memory.
	sorted := make([]domain.Tool, len(catalog.Tools))
	copy(sorted, catalog.Tools)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Ref.Namespaced() < sorted[j].Ref.Namespaced()
	})

	selected := make([]domain.Tool, 0, size)
	for _, t := range sorted {
		if cursor != "" && t.Ref.Namespaced() <= after {
			continue
		}
		selected = append(selected, t)
		if len(selected) == size {
			break
		}
	}

	// A further page exists only if a tool sorts strictly after the last one
	// returned; otherwise next is empty to signal the final page.
	if len(selected) == size {
		last := selected[len(selected)-1].Ref.Namespaced()
		if hasNameAfter(sorted, last) {
			next = encodeCursor(last)
		}
	}

	return domain.Catalog{Tools: selected}, next, nil
}

// Resolve parses a client-facing namespaced tool name and returns the downstream
// and tool it routes to.
//
// It rejects a malformed name (delegating the shape rules to [domain.ParseToolRef])
// and rejects a well-formed name whose downstream is not present-and-true in
// knownDownstreams, so a call to an unknown or removed downstream is a clean
// error rather than a panic or a misroute (design §7). A nil knownDownstreams
// map treats every downstream as unknown.
func Resolve(namespaced string, knownDownstreams map[string]bool) (domain.ToolRef, error) {
	ref, err := domain.ParseToolRef(namespaced)
	if err != nil {
		return domain.ToolRef{}, fmt.Errorf("aggregate: %w", err)
	}
	if !knownDownstreams[ref.Downstream] {
		return domain.ToolRef{}, fmt.Errorf("aggregate: unknown downstream %q", ref.Downstream)
	}
	return ref, nil
}

// hasNameAfter reports whether sorted (ascending by namespaced name) contains any
// tool whose name is strictly greater than name.
func hasNameAfter(sorted []domain.Tool, name string) bool {
	for _, t := range sorted {
		if t.Ref.Namespaced() > name {
			return true
		}
	}
	return false
}

// encodeCursor wraps a last-returned namespaced name as an opaque base64url
// token. Encoding keeps the wire format opaque so clients treat it as a token
// rather than a name to construct by hand.
func encodeCursor(lastName string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(lastName))
}

// decodeCursor reverses encodeCursor. An empty cursor decodes to an empty name
// (start at the beginning). A token that is not valid base64url is an error.
func decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("aggregate: malformed page cursor: %w", err)
	}
	return string(raw), nil
}
