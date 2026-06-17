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

// Engine evaluates access decisions and catalog filtering. Its rule set is
// hot-reloadable: New builds it, Reload atomically swaps it, and every read takes
// the read lock, so a SIGHUP reload never drops a live evaluation (design §5).
type Engine struct {
	mu sync.RWMutex
	// defaultAllow, clients, and pinned are all guarded by mu so a reload can swap
	// them atomically under the write lock while evaluations read under the read
	// lock. pinned is the per-client set of namespaced tool names admitted by
	// wildcards as of the last Sync.
	defaultAllow bool
	clients      map[string]clientPolicy
	pinned       map[string]map[string]bool
}

// New builds an engine from compiled rules. defaultAllow makes every call
// allowed (used when the configured default is "allow"); otherwise the engine is
// deny-by-default and only explicit allows pass. Input is assumed already
// validated by the config layer.
func New(defaultAllow bool, rules []Rule) *Engine {
	return &Engine{
		defaultAllow: defaultAllow,
		clients:      compileRules(rules),
		pinned:       make(map[string]map[string]bool),
	}
}

// Reload atomically swaps the engine's default and rule set, for a SIGHUP/file-
// watch config reload (design §5). It clears the wildcard pins computed for the
// old rules, so a wildcard admits nothing until the caller re-Syncs against the
// current catalog — a fail-closed transient (deny during reload), never an
// over-permit. It is safe to call concurrently with Decide and Filter.
func (e *Engine) Reload(defaultAllow bool, rules []Rule) {
	clients := compileRules(rules)
	e.mu.Lock()
	e.defaultAllow = defaultAllow
	e.clients = clients
	e.pinned = make(map[string]map[string]bool)
	e.mu.Unlock()
}

// compileRules turns the allow lists into per-client exact/wildcard sets.
func compileRules(rules []Rule) map[string]clientPolicy {
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
	return clients
}

// Decide reports whether the client may call the tool. It reads the rule set
// under the read lock so a concurrent Reload swap is observed atomically.
func (e *Engine) Decide(client domain.Identity, t domain.ToolRef) domain.Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
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
	if e.pinned[client.ID][name] {
		return domain.Allowed()
	}
	return domain.Denied(domain.ReasonDeniedDefault)
}

// Filter returns the subset of the catalog the client is permitted to see.
// Filtering is security-load-bearing: a tool the client may not call is omitted
// entirely, including its schema and description (design §8).
func (e *Engine) Filter(client domain.Identity, full domain.Catalog) domain.Catalog {
	allowed := make([]domain.Tool, 0, len(full.Tools))
	for i := range full.Tools {
		if e.Decide(client, full.Tools[i].Ref).Allow {
			allowed = append(allowed, full.Tools[i])
		}
	}
	return domain.Catalog{Tools: allowed}
}

// Sync pins each client's wildcards to the tools present in the catalog. After
// Sync, a wildcard admits exactly the matching tools observed here; tools that
// appear later are denied until the next Sync. It holds the write lock across the
// computation so it reads a consistent rule set even if a Reload runs
// concurrently.
func (e *Engine) Sync(catalog domain.Catalog) {
	e.mu.Lock()
	defer e.mu.Unlock()
	pinned := make(map[string]map[string]bool, len(e.clients))
	for clientID, cp := range e.clients {
		if len(cp.wildcards) == 0 {
			continue
		}
		for i := range catalog.Tools {
			ref := catalog.Tools[i].Ref
			if cp.wildcards[ref.Downstream] {
				if pinned[clientID] == nil {
					pinned[clientID] = make(map[string]bool)
				}
				pinned[clientID][ref.Namespaced()] = true
			}
		}
	}
	e.pinned = pinned
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
