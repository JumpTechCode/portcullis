package aggregate_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/aggregate"
	"github.com/JumpTechCode/portcullis/internal/domain"
)

// listing is a small helper that builds a Listing from a downstream name and a
// set of bare tool names, keeping the test cases terse.
func listing(downstream string, tools ...string) aggregate.Listing {
	l := aggregate.Listing{Downstream: downstream}
	for _, name := range tools {
		l.Tools = append(l.Tools, aggregate.Tool{Name: name})
	}
	return l
}

// namespacedNames extracts the namespaced name of every tool in a catalog, in
// order, so tests can assert on ordering and membership concisely.
func namespacedNames(c domain.Catalog) []string {
	names := make([]string, len(c.Tools))
	for i := range c.Tools {
		names[i] = c.Tools[i].Ref.Namespaced()
	}
	return names
}

func TestBuildNamespacesTools(t *testing.T) {
	got, err := aggregate.Build([]aggregate.Listing{
		{Downstream: "github", Tools: []aggregate.Tool{
			{Name: "create_issue", Description: "Open an issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned an unexpected error: %v", err)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("Build produced %d tools, want 1", len(got.Tools))
	}
	tool := got.Tools[0]
	if tool.Ref.Downstream != "github" || tool.Ref.Tool != "create_issue" {
		t.Errorf("Build produced ref %+v, want github/create_issue", tool.Ref)
	}
	if tool.Ref.Namespaced() != "github__create_issue" {
		t.Errorf("namespaced name = %q, want github__create_issue", tool.Ref.Namespaced())
	}
	if tool.Description != "Open an issue" {
		t.Errorf("description = %q, want %q", tool.Description, "Open an issue")
	}
	if string(tool.InputSchema) != `{"type":"object"}` {
		t.Errorf("input schema = %q, want it carried through unchanged", tool.InputSchema)
	}
}

func TestBuildCarriesToolMetadata(t *testing.T) {
	got, err := aggregate.Build([]aggregate.Listing{
		{Downstream: "github", Tools: []aggregate.Tool{{
			Name:         "create_issue",
			Title:        "Create Issue",
			Description:  "Open an issue",
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`),
			Annotations:  json.RawMessage(`{"readOnlyHint":true}`),
			Icons:        json.RawMessage(`[{"src":"https://example.com/i.png"}]`),
		}}},
	})
	if err != nil {
		t.Fatalf("Build returned an unexpected error: %v", err)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("Build produced %d tools, want 1", len(got.Tools))
	}
	tool := got.Tools[0]
	if tool.Title != "Create Issue" {
		t.Errorf("title = %q, want it carried through", tool.Title)
	}
	if string(tool.OutputSchema) != `{"type":"object","properties":{"url":{"type":"string"}}}` {
		t.Errorf("output schema = %s, want it carried through unchanged", tool.OutputSchema)
	}
	if string(tool.Annotations) != `{"readOnlyHint":true}` {
		t.Errorf("annotations = %s, want them carried through unchanged", tool.Annotations)
	}
	if string(tool.Icons) != `[{"src":"https://example.com/i.png"}]` {
		t.Errorf("icons = %s, want them carried through unchanged", tool.Icons)
	}
}

func TestBuildMergesAcrossDownstreams(t *testing.T) {
	got, err := aggregate.Build([]aggregate.Listing{
		listing("github", "create_issue", "list_repos"),
		listing("search", "query"),
	})
	if err != nil {
		t.Fatalf("Build returned an unexpected error: %v", err)
	}
	want := []string{"github__create_issue", "github__list_repos", "search__query"}
	got2 := namespacedNames(got)
	if len(got2) != len(want) {
		t.Fatalf("Build produced %v, want %v", got2, want)
	}
	for i := range want {
		if got2[i] != want[i] {
			t.Fatalf("Build produced %v, want %v", got2, want)
		}
	}
}

func TestBuildDeterministicOrder(t *testing.T) {
	// Inputs in a non-sorted order must produce a sorted, deterministic catalog.
	got, err := aggregate.Build([]aggregate.Listing{
		listing("zeta", "b", "a"),
		listing("alpha", "z", "m"),
	})
	if err != nil {
		t.Fatalf("Build returned an unexpected error: %v", err)
	}
	want := []string{"alpha__m", "alpha__z", "zeta__a", "zeta__b"}
	got2 := namespacedNames(got)
	for i := range want {
		if i >= len(got2) || got2[i] != want[i] {
			t.Fatalf("Build produced %v, want %v", got2, want)
		}
	}
}

func TestBuildEmptyInputEmptyCatalog(t *testing.T) {
	got, err := aggregate.Build(nil)
	if err != nil {
		t.Fatalf("Build(nil) returned an unexpected error: %v", err)
	}
	if len(got.Tools) != 0 {
		t.Errorf("Build(nil) produced %d tools, want 0", len(got.Tools))
	}

	got, err = aggregate.Build([]aggregate.Listing{})
	if err != nil {
		t.Fatalf("Build([]) returned an unexpected error: %v", err)
	}
	if len(got.Tools) != 0 {
		t.Errorf("Build([]) produced %d tools, want 0", len(got.Tools))
	}
}

func TestBuildRejectsSeparatorInToolName(t *testing.T) {
	_, err := aggregate.Build([]aggregate.Listing{
		listing("github", "create__issue"),
	})
	if err == nil {
		t.Fatal("Build accepted a tool name containing the namespace separator")
	}
	if !strings.Contains(err.Error(), domain.NamespaceSeparator) {
		t.Errorf("error %q does not mention the separator %q", err, domain.NamespaceSeparator)
	}
}

func TestBuildRejectsSeparatorInDownstreamName(t *testing.T) {
	_, err := aggregate.Build([]aggregate.Listing{
		listing("git__hub", "create_issue"),
	})
	if err == nil {
		t.Fatal("Build accepted a downstream name containing the namespace separator")
	}
	if !strings.Contains(err.Error(), domain.NamespaceSeparator) {
		t.Errorf("error %q does not mention the separator %q", err, domain.NamespaceSeparator)
	}
}

func TestBuildRejectsEmptyDownstreamName(t *testing.T) {
	_, err := aggregate.Build([]aggregate.Listing{
		listing("", "create_issue"),
	})
	if err == nil {
		t.Fatal("Build accepted an empty downstream name")
	}
}

func TestBuildRejectsEmptyToolName(t *testing.T) {
	_, err := aggregate.Build([]aggregate.Listing{
		listing("github", ""),
	})
	if err == nil {
		t.Fatal("Build accepted an empty tool name")
	}
}

func TestBuildRejectsDuplicateDownstream(t *testing.T) {
	_, err := aggregate.Build([]aggregate.Listing{
		listing("github", "create_issue"),
		listing("github", "list_repos"),
	})
	if err == nil {
		t.Fatal("Build accepted two listings for the same downstream")
	}
	if !strings.Contains(err.Error(), "github") {
		t.Errorf("error %q does not name the duplicate downstream", err)
	}
}

func TestBuildRejectsDuplicateToolWithinDownstream(t *testing.T) {
	_, err := aggregate.Build([]aggregate.Listing{
		{Downstream: "github", Tools: []aggregate.Tool{
			{Name: "create_issue"},
			{Name: "create_issue"},
		}},
	})
	if err == nil {
		t.Fatal("Build accepted a duplicate tool within one downstream")
	}
	if !strings.Contains(err.Error(), "create_issue") {
		t.Errorf("error %q does not name the duplicate tool", err)
	}
}

func TestPageSinglePage(t *testing.T) {
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	page, next, err := aggregate.Page(cat, "", 10)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want \"\" on the last page", next)
	}
	if len(page.Tools) != 2 {
		t.Errorf("page held %d tools, want 2", len(page.Tools))
	}
}

func TestPageMultiPageRoundTrip(t *testing.T) {
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2", "3"),
		listing("b", "1", "2"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const size = 2

	seen := make(map[string]int)
	cursor := ""
	pages := 0
	for {
		page, next, err := aggregate.Page(cat, cursor, size)
		if err != nil {
			t.Fatalf("Page(cursor=%q): %v", cursor, err)
		}
		if len(page.Tools) > size {
			t.Fatalf("page held %d tools, exceeds size %d", len(page.Tools), size)
		}
		for _, name := range namespacedNames(page) {
			seen[name]++
		}
		pages++
		if pages > 100 {
			t.Fatal("pagination did not terminate")
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != len(cat.Tools) {
		t.Fatalf("saw %d distinct tools across pages, want %d", len(seen), len(cat.Tools))
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("tool %q appeared %d times across pages, want exactly once", name, count)
		}
	}
}

func TestPageCursorIsOpaque(t *testing.T) {
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2", "3"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, next, err := aggregate.Page(cat, "", 1)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if next == "" {
		t.Fatal("expected a non-empty cursor for a multi-page catalog")
	}
	// The cursor must not leak the raw last name; it is an opaque token.
	if next == "a__1" {
		t.Errorf("cursor %q is the raw last name, not an opaque token", next)
	}
	if _, decErr := base64.RawURLEncoding.DecodeString(next); decErr != nil {
		t.Errorf("cursor %q is not valid base64url: %v", next, decErr)
	}
}

func TestPageMalformedCursorErrors(t *testing.T) {
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// "!!!" is not valid base64url.
	if _, _, err := aggregate.Page(cat, "!!!", 10); err == nil {
		t.Error("Page accepted a malformed (non-base64) cursor")
	}
}

func TestPageStableWhenToolAddedBetweenPages(t *testing.T) {
	// Page 1 of the original catalog.
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "2", "4", "6"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	page1, next, err := aggregate.Page(cat, "", 1)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := namespacedNames(page1); len(got) != 1 || got[0] != "a__2" {
		t.Fatalf("page1 = %v, want [a__2]", namespacedNames(page1))
	}

	// The catalog grows between requests: a tool that sorts before the cursor is
	// added. The name-based cursor must still resume strictly after a__2 and must
	// not re-yield anything at or before the cursor.
	grown, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2", "4", "6"),
	})
	if err != nil {
		t.Fatalf("Build grown: %v", err)
	}
	page2, _, err := aggregate.Page(grown, next, 10)
	if err != nil {
		t.Fatalf("Page page2: %v", err)
	}
	for _, name := range namespacedNames(page2) {
		if name <= "a__2" {
			t.Errorf("page2 re-yielded %q which is <= the cursor name a__2", name)
		}
	}
	want := []string{"a__4", "a__6"}
	got := namespacedNames(page2)
	if len(got) != len(want) {
		t.Fatalf("page2 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page2 = %v, want %v", got, want)
		}
	}
}

func TestPageStableWhenToolRemovedBetweenPages(t *testing.T) {
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2", "3", "4"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, next, err := aggregate.Page(cat, "", 2) // returns a__1, a__2
	if err != nil {
		t.Fatalf("Page: %v", err)
	}

	// a__2 (the cursor's tool) is removed before the next request. Resuming must
	// still return everything strictly after a__2 without error.
	shrunk, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "3", "4"),
	})
	if err != nil {
		t.Fatalf("Build shrunk: %v", err)
	}
	page2, nxt, err := aggregate.Page(shrunk, next, 10)
	if err != nil {
		t.Fatalf("Page page2: %v", err)
	}
	if nxt != "" {
		t.Errorf("next = %q, want \"\" on the last page", nxt)
	}
	want := []string{"a__3", "a__4"}
	got := namespacedNames(page2)
	if len(got) != len(want) {
		t.Fatalf("page2 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page2 = %v, want %v", got, want)
		}
	}
}

func TestPageFullPageAtEndHasNoNext(t *testing.T) {
	// A page whose size exactly equals the number of remaining tools must report
	// an empty next cursor: a full page is necessary but not sufficient for a
	// further page to exist.
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	page, next, err := aggregate.Page(cat, "", 2)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page.Tools) != 2 {
		t.Fatalf("page held %d tools, want 2", len(page.Tools))
	}
	if next != "" {
		t.Errorf("next = %q, want \"\" when the full page is also the last page", next)
	}
}

func TestPageRejectsNonPositiveSize(t *testing.T) {
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, _, err := aggregate.Page(cat, "", 0); err == nil {
		t.Error("Page accepted size 0")
	}
	if _, _, err := aggregate.Page(cat, "", -3); err == nil {
		t.Error("Page accepted a negative size")
	}
}

func TestPageEmptyCatalog(t *testing.T) {
	page, next, err := aggregate.Page(domain.Catalog{}, "", 10)
	if err != nil {
		t.Fatalf("Page on empty catalog: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want \"\" for an empty catalog", next)
	}
	if len(page.Tools) != 0 {
		t.Errorf("page held %d tools, want 0", len(page.Tools))
	}
}

func TestPageCursorPastEnd(t *testing.T) {
	cat, err := aggregate.Build([]aggregate.Listing{
		listing("a", "1", "2"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// A valid cursor that sorts after every tool yields an empty final page.
	cursor := base64.RawURLEncoding.EncodeToString([]byte("zzz__zzz"))
	page, next, err := aggregate.Page(cat, cursor, 10)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want \"\"", next)
	}
	if len(page.Tools) != 0 {
		t.Errorf("page held %d tools, want 0", len(page.Tools))
	}
}

func TestResolveKnownDownstream(t *testing.T) {
	ref, err := aggregate.Resolve("github__create_issue", map[string]bool{"github": true})
	if err != nil {
		t.Fatalf("Resolve returned an unexpected error: %v", err)
	}
	if ref.Downstream != "github" || ref.Tool != "create_issue" {
		t.Errorf("Resolve produced %+v, want github/create_issue", ref)
	}
}

func TestResolveUnknownDownstream(t *testing.T) {
	_, err := aggregate.Resolve("ghost__do_thing", map[string]bool{"github": true})
	if err == nil {
		t.Fatal("Resolve accepted an unknown downstream")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the unknown downstream", err)
	}
}

func TestResolveDownstreamPresentButFalse(t *testing.T) {
	// A downstream explicitly mapped to false counts as not known.
	_, err := aggregate.Resolve("github__create_issue", map[string]bool{"github": false})
	if err == nil {
		t.Fatal("Resolve accepted a downstream mapped to false")
	}
}

func TestResolveMalformedName(t *testing.T) {
	cases := []string{
		"nosep",
		"a__b__c",
		"__leadingsep",
		"trailingsep__",
		"",
	}
	for _, name := range cases {
		if _, err := aggregate.Resolve(name, map[string]bool{"a": true}); err == nil {
			t.Errorf("Resolve(%q) succeeded, want an error", name)
		}
	}
}

func TestResolveNilKnownMap(t *testing.T) {
	// A nil map means no downstreams are known; resolution must fail cleanly.
	_, err := aggregate.Resolve("github__create_issue", nil)
	if err == nil {
		t.Fatal("Resolve with a nil known-downstreams map succeeded, want an error")
	}
}

// errString is a tiny helper to keep error-message assertions readable; it
// guards against nil before calling Error.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestBuildErrorsAreDistinct(t *testing.T) {
	// Sanity check that the various failure modes report different messages so an
	// operator can tell them apart.
	_, dupDown := aggregate.Build([]aggregate.Listing{listing("a", "x"), listing("a", "y")})
	_, dupTool := aggregate.Build([]aggregate.Listing{{Downstream: "a", Tools: []aggregate.Tool{{Name: "x"}, {Name: "x"}}}})
	if errString(dupDown) == errString(dupTool) {
		t.Error("duplicate-downstream and duplicate-tool errors are indistinguishable")
	}
	if errors.Is(dupDown, nil) {
		t.Error("expected a non-nil duplicate-downstream error")
	}
}
