package policy_test

import (
	"sync"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/policy"
)

// compile-time proof the engine satisfies the domain ports it is injected as.
var (
	_ domain.Decider       = (*policy.Engine)(nil)
	_ domain.CatalogFilter = (*policy.Engine)(nil)
)

func ref(downstream, tool string) domain.ToolRef {
	return domain.ToolRef{Downstream: downstream, Tool: tool}
}

func tool(downstream, name string) domain.Tool {
	return domain.Tool{Ref: ref(downstream, name), Description: name}
}

const claude = "claude-desktop"

func TestDenyByDefaultWithNoRules(t *testing.T) {
	e := policy.New(false, nil)
	d := e.Decide(domain.Identity{ID: claude}, ref("github", "create_issue"))
	if d.Allow {
		t.Error("a client with no rules was allowed; deny-by-default requires a deny")
	}
	if d.Reason != domain.ReasonDeniedDefault {
		t.Errorf("reason = %q, want %q", d.Reason, domain.ReasonDeniedDefault)
	}
}

func TestExactAllow(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__create_issue"}},
	})
	id := domain.Identity{ID: claude}

	if d := e.Decide(id, ref("github", "create_issue")); !d.Allow {
		t.Error("explicitly allowed tool was denied")
	} else if d.Reason != domain.ReasonAllowed {
		t.Errorf("reason = %q, want %q", d.Reason, domain.ReasonAllowed)
	}

	if d := e.Decide(id, ref("github", "delete_repo")); d.Allow {
		t.Error("a tool not in the allow list was permitted")
	}
}

func TestAllowIsPerClient(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__create_issue"}},
	})
	if d := e.Decide(domain.Identity{ID: "ci-bot"}, ref("github", "create_issue")); d.Allow {
		t.Error("a different client inherited another client's allow rule")
	}
}

// A wildcard must not allow anything until the allowlist is synced against an
// observed catalog (deny-by-default until re-synced; ADR-0009).
func TestWildcardDeniedBeforeSync(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__*"}},
	})
	if d := e.Decide(domain.Identity{ID: claude}, ref("github", "create_issue")); d.Allow {
		t.Error("wildcard allowed a tool before any sync; it must pin to a synced tool set")
	}
}

func TestWildcardAllowsSyncedTools(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__*"}},
	})
	e.Sync(domain.Catalog{Tools: []domain.Tool{
		tool("github", "create_issue"),
		tool("github", "list_issues"),
		tool("search", "query"),
	}})
	id := domain.Identity{ID: claude}

	if d := e.Decide(id, ref("github", "create_issue")); !d.Allow {
		t.Error("wildcard did not allow a tool present at sync")
	}
	// A different downstream is not covered by github__*.
	if d := e.Decide(id, ref("search", "query")); d.Allow {
		t.Error("github__* wildcard leaked to the search downstream")
	}
}

// A tool that appears at runtime (after the last sync) must stay denied even if
// it matches a wildcard, until an explicit re-sync admits it (ADR-0009).
func TestWildcardDoesNotAdmitRuntimeTool(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__*"}},
	})
	e.Sync(domain.Catalog{Tools: []domain.Tool{tool("github", "create_issue")}})
	id := domain.Identity{ID: claude}

	// "delete_repo" was not in the synced catalog.
	if d := e.Decide(id, ref("github", "delete_repo")); d.Allow {
		t.Error("a runtime-appearing tool was auto-admitted by a wildcard")
	}

	// After an explicit re-sync that includes it, it becomes allowed.
	e.Sync(domain.Catalog{Tools: []domain.Tool{tool("github", "delete_repo")}})
	if d := e.Decide(id, ref("github", "delete_repo")); !d.Allow {
		t.Error("re-sync did not admit the newly observed tool")
	}
}

func TestFilterHidesDisallowedTools(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__create_issue"}},
	})
	full := domain.Catalog{Tools: []domain.Tool{
		tool("github", "create_issue"),
		tool("github", "delete_repo"),
		tool("search", "query"),
	}}
	got := e.Filter(domain.Identity{ID: claude}, full)
	if len(got.Tools) != 1 || got.Tools[0].Ref != ref("github", "create_issue") {
		t.Errorf("Filter = %+v, want only github__create_issue", got.Tools)
	}
}

func TestFilterWithWildcardAfterSync(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__*"}},
	})
	full := domain.Catalog{Tools: []domain.Tool{
		tool("github", "create_issue"),
		tool("github", "list_issues"),
		tool("search", "query"),
	}}
	e.Sync(full)
	got := e.Filter(domain.Identity{ID: claude}, full)
	if len(got.Tools) != 2 {
		t.Errorf("Filter returned %d tools, want 2 github tools", len(got.Tools))
	}
	for _, tl := range got.Tools {
		if tl.Ref.Downstream != "github" {
			t.Errorf("Filter leaked a non-github tool: %s", tl.Ref)
		}
	}
}

// Sync must leave exact-only clients unaffected while pinning wildcard clients.
func TestSyncDoesNotDisturbExactOnlyClients(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: "exact-bot", Allow: []string{"github__create_issue"}},
		{Client: claude, Allow: []string{"github__*"}},
	})
	e.Sync(domain.Catalog{Tools: []domain.Tool{
		tool("github", "create_issue"),
		tool("github", "list_issues"),
	}})

	if d := e.Decide(domain.Identity{ID: "exact-bot"}, ref("github", "create_issue")); !d.Allow {
		t.Error("exact allow stopped working after a sync")
	}
	if d := e.Decide(domain.Identity{ID: "exact-bot"}, ref("github", "list_issues")); d.Allow {
		t.Error("exact-only client was widened by another client's wildcard sync")
	}
	if d := e.Decide(domain.Identity{ID: claude}, ref("github", "list_issues")); !d.Allow {
		t.Error("wildcard client was not pinned by the sync")
	}
}

func TestDefaultAllowPermitsEverything(t *testing.T) {
	e := policy.New(true, nil)
	if d := e.Decide(domain.Identity{ID: "anyone"}, ref("github", "anything")); !d.Allow {
		t.Error("default-allow policy denied a call")
	}
}

// The hot path (Decide/Filter) runs concurrently with re-aggregation (Sync).
// This exercises that interleaving so the race detector guards the locking.
func TestConcurrentSyncDecideFilter(t *testing.T) {
	e := policy.New(false, []policy.Rule{
		{Client: claude, Allow: []string{"github__*", "search__query"}},
	})
	cat := domain.Catalog{Tools: []domain.Tool{
		tool("github", "create_issue"),
		tool("github", "list_issues"),
		tool("search", "query"),
	}}
	id := domain.Identity{ID: claude}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(3)
		go func() { defer wg.Done(); e.Sync(cat) }()
		go func() { defer wg.Done(); _ = e.Decide(id, ref("github", "create_issue")) }()
		go func() { defer wg.Done(); _ = e.Filter(id, cat) }()
	}
	wg.Wait()
}
