// Package policy is the deny-by-default access engine. It decides whether a
// client may call a tool and filters the catalog a client may see.
//
// Rules are explicit allow lists per client. A bare entry ("github__create_issue")
// allows exactly that tool. A wildcard ("github__*") allows the tools of that
// downstream, but only those observed at the last explicit Sync: a tool that
// appears at runtime is denied until a re-sync admits it, so a downstream cannot
// widen a client's access by exposing a new tool (design §6.5, ADR-0009).
//
// The engine satisfies domain.Decider and domain.CatalogFilter and is safe for
// concurrent use: rules are immutable after New, and the synced wildcard
// expansion is guarded by a read-write mutex.
package policy

import (
	"strings"
	"sync"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// Rule is a client's allow list. Each entry is a namespaced tool name
// ("downstream__tool") or a downstream wildcard ("downstream__*").
type Rule struct {
	Client string
	Allow  []string
}

// clientPolicy holds a single client's compiled rule.
type clientPolicy struct {
	// exact is the set of namespaced tool names allowed outright.
	exact map[string]bool
	// wildcards is the set of downstream names whose tools are allowed, subject
	// to wildcard pinning.
	wildcards map[string]bool
}

// Engine evaluates access decisions and catalog filtering.
type Engine struct {
	defaultAllow bool
	clients      map[string]clientPolicy

	mu sync.RWMutex
	// pinned is the per-client set of namespaced tool names admitted by wildcards
	// as of the last Sync. Guarded by mu.
	pinned map[string]map[string]bool
}

// New builds an engine from compiled rules. defaultAllow makes every call
// allowed (used when the configured default is "allow"); otherwise the engine is
// deny-by-default and only explicit allows pass. Input is assumed already
// validated by the config layer.
func New(defaultAllow bool, rules []Rule) *Engine {
	clients := make(map[string]clientPolicy, len(rules))
	for _, r := range rules {
		cp := clients[r.Client]
		if cp.exact == nil {
			cp = clientPolicy{exact: map[string]bool{}, wildcards: map[string]bool{}}
		}
		for _, entry := range r.Allow {
			if ds, ok := wildcardDownstream(entry); ok {
				cp.wildcards[ds] = true
			} else {
				cp.exact[entry] = true
			}
		}
		clients[r.Client] = cp
	}
	return &Engine{
		defaultAllow: defaultAllow,
		clients:      clients,
		pinned:       make(map[string]map[string]bool),
	}
}

// Decide reports whether the client may call the tool.
func (e *Engine) Decide(client domain.Identity, t domain.ToolRef) domain.Decision {
	if e.defaultAllow {
		return domain.Allowed()
	}
	cp, ok := e.clients[client.ID]
	if !ok {
		return domain.Denied(domain.ReasonDeniedDefault)
	}
	name := t.Namespaced()
	if cp.exact[name] {
		return domain.Allowed()
	}

	e.mu.RLock()
	admitted := e.pinned[client.ID][name]
	e.mu.RUnlock()
	if admitted {
		return domain.Allowed()
	}
	return domain.Denied(domain.ReasonDeniedDefault)
}

// Filter returns the subset of the catalog the client is permitted to see.
// Filtering is security-load-bearing: a tool the client may not call is omitted
// entirely, including its schema and description (design §8).
func (e *Engine) Filter(client domain.Identity, full domain.Catalog) domain.Catalog {
	allowed := make([]domain.Tool, 0, len(full.Tools))
	for _, t := range full.Tools {
		if e.Decide(client, t.Ref).Allow {
			allowed = append(allowed, t)
		}
	}
	return domain.Catalog{Tools: allowed}
}

// Sync pins each client's wildcards to the tools present in the catalog. After
// Sync, a wildcard admits exactly the matching tools observed here; tools that
// appear later are denied until the next Sync.
func (e *Engine) Sync(catalog domain.Catalog) {
	pinned := make(map[string]map[string]bool, len(e.clients))
	for clientID, cp := range e.clients {
		if len(cp.wildcards) == 0 {
			continue
		}
		for _, t := range catalog.Tools {
			if cp.wildcards[t.Ref.Downstream] {
				if pinned[clientID] == nil {
					pinned[clientID] = make(map[string]bool)
				}
				pinned[clientID][t.Ref.Namespaced()] = true
			}
		}
	}
	e.mu.Lock()
	e.pinned = pinned
	e.mu.Unlock()
}

// wildcardDownstream returns the downstream named by a wildcard allow entry
// ("downstream__*") and whether the entry is a wildcard.
func wildcardDownstream(entry string) (string, bool) {
	suffix := domain.NamespaceSeparator + "*"
	if !strings.HasSuffix(entry, suffix) {
		return "", false
	}
	return strings.TrimSuffix(entry, suffix), true
}
