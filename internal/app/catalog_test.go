package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/policy"
	"github.com/JumpTechCode/portcullis/internal/registry"
)

func TestCatalogCacheBuildsMergesAndSyncs(t *testing.T) {
	calls := map[string]int{}
	lister := func(_ context.Context, ds string) ([]registry.ToolInfo, error) {
		calls[ds]++
		switch ds {
		case "github":
			return []registry.ToolInfo{{Name: "create_issue", Description: "open"}}, nil
		case "search":
			return []registry.ToolInfo{{Name: "web"}}, nil
		}
		return nil, errors.New("unknown downstream")
	}
	eng := policy.New(false, []policy.Rule{{Client: "c", Allow: []string{"github__*"}}})
	c := &catalogCache{downstreams: []string{"github", "search"}, list: lister, syncer: eng}

	cat, err := c.get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cat.Tools) != 2 {
		t.Fatalf("catalog has %d tools, want 2", len(cat.Tools))
	}
	if _, ok := cat.Lookup("github__create_issue"); !ok {
		t.Error("github__create_issue missing from catalog")
	}
	if _, ok := cat.Lookup("search__web"); !ok {
		t.Error("search__web missing from catalog")
	}

	// Second get is served from cache: no further listing.
	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("second get: %v", err)
	}
	if calls["github"] != 1 || calls["search"] != 1 {
		t.Errorf("listing happened more than once: %v", calls)
	}

	// Sync pinned the wildcard so the client may call the observed tool.
	if !eng.Decide(domain.Identity{ID: "c"}, domain.ToolRef{Downstream: "github", Tool: "create_issue"}).Allow {
		t.Error("wildcard was not pinned to the synced catalog")
	}
}

func TestCatalogCacheCarriesToolMetadata(t *testing.T) {
	lister := func(_ context.Context, _ string) ([]registry.ToolInfo, error) {
		return []registry.ToolInfo{{
			Name:         "create_issue",
			Title:        "Create Issue",
			Description:  "open",
			OutputSchema: json.RawMessage(`{"type":"object"}`),
			Annotations:  json.RawMessage(`{"readOnlyHint":true}`),
			Icons:        json.RawMessage(`[{"src":"https://example.com/i.png"}]`),
		}}, nil
	}
	c := &catalogCache{downstreams: []string{"github"}, list: lister, syncer: policy.New(false, nil)}

	cat, err := c.get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tool, ok := cat.Lookup("github__create_issue")
	if !ok {
		t.Fatal("github__create_issue missing from catalog")
	}
	if tool.Title != "Create Issue" {
		t.Errorf("title = %q, want it threaded through toListing", tool.Title)
	}
	if string(tool.OutputSchema) != `{"type":"object"}` {
		t.Errorf("output schema = %s, want it threaded through toListing", tool.OutputSchema)
	}
	if string(tool.Annotations) != `{"readOnlyHint":true}` {
		t.Errorf("annotations = %s, want them threaded through toListing", tool.Annotations)
	}
	if string(tool.Icons) != `[{"src":"https://example.com/i.png"}]` {
		t.Errorf("icons = %s, want them threaded through toListing", tool.Icons)
	}
}

func TestCatalogCacheRetriesAfterListError(t *testing.T) {
	fail := true
	lister := func(_ context.Context, ds string) ([]registry.ToolInfo, error) {
		if fail {
			return nil, errors.New("downstream down")
		}
		return []registry.ToolInfo{{Name: "t"}}, nil
	}
	c := &catalogCache{downstreams: []string{"github"}, list: lister, syncer: policy.New(false, nil)}

	if _, err := c.get(context.Background()); err == nil {
		t.Fatal("expected the first build to fail")
	}
	fail = false
	cat, err := c.get(context.Background())
	if err != nil {
		t.Fatalf("expected the retry to succeed, got %v", err)
	}
	if len(cat.Tools) != 1 {
		t.Errorf("catalog has %d tools, want 1", len(cat.Tools))
	}
}
