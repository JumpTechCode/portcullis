package domain_test

import (
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

func sampleCatalog() domain.Catalog {
	return domain.Catalog{Tools: []domain.Tool{
		{Ref: domain.ToolRef{Downstream: "github", Tool: "create_issue"}, Description: "Open an issue"},
		{Ref: domain.ToolRef{Downstream: "search", Tool: "query"}, Description: "Search the web"},
	}}
}

func TestCatalogLookupHit(t *testing.T) {
	got, ok := sampleCatalog().Lookup("github__create_issue")
	if !ok {
		t.Fatal("Lookup did not find an existing tool")
	}
	if got.Ref.Tool != "create_issue" {
		t.Errorf("Lookup returned %+v, want the create_issue tool", got.Ref)
	}
}

func TestCatalogLookupMiss(t *testing.T) {
	if _, ok := sampleCatalog().Lookup("github__delete_repo"); ok {
		t.Error("Lookup reported a tool that is not in the catalog")
	}
}

func TestCatalogLookupEmpty(t *testing.T) {
	var empty domain.Catalog
	if _, ok := empty.Lookup("github__create_issue"); ok {
		t.Error("Lookup on an empty catalog reported a hit")
	}
}
