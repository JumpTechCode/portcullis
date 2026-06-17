package registry

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolInfoCarriesMetadata(t *testing.T) {
	src := &mcp.Tool{
		Name:         "create_issue",
		Title:        "Create Issue",
		Description:  "Open an issue",
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`),
		Annotations:  &mcp.ToolAnnotations{Title: "Create Issue", ReadOnlyHint: true},
		Icons:        []mcp.Icon{{Source: "https://example.com/i.png", MIMEType: "image/png"}},
	}

	got, err := toolInfo(src)
	if err != nil {
		t.Fatalf("toolInfo: %v", err)
	}

	if got.Name != "create_issue" || got.Title != "Create Issue" || got.Description != "Open an issue" {
		t.Errorf("scalar fields not carried: %+v", got)
	}
	if string(got.InputSchema) != `{"type":"object"}` {
		t.Errorf("input schema = %s, want it carried through unchanged", got.InputSchema)
	}
	if string(got.OutputSchema) != `{"type":"object","properties":{"url":{"type":"string"}}}` {
		t.Errorf("output schema = %s, want it carried through unchanged", got.OutputSchema)
	}

	var ann mcp.ToolAnnotations
	if err := json.Unmarshal(got.Annotations, &ann); err != nil {
		t.Fatalf("annotations not valid JSON: %v (%s)", err, got.Annotations)
	}
	if !ann.ReadOnlyHint || ann.Title != "Create Issue" {
		t.Errorf("annotations lost data on round-trip: %+v", ann)
	}

	var icons []mcp.Icon
	if err := json.Unmarshal(got.Icons, &icons); err != nil {
		t.Fatalf("icons not valid JSON: %v (%s)", err, got.Icons)
	}
	if len(icons) != 1 || icons[0].Source != "https://example.com/i.png" {
		t.Errorf("icons lost data on round-trip: %+v", icons)
	}
}

func TestToolInfoOmitsAbsentMetadata(t *testing.T) {
	got, err := toolInfo(&mcp.Tool{Name: "plain"})
	if err != nil {
		t.Fatalf("toolInfo: %v", err)
	}
	if got.OutputSchema != nil {
		t.Errorf("absent output schema became %s, want nil", got.OutputSchema)
	}
	if got.Annotations != nil {
		t.Errorf("absent annotations became %s, want nil", got.Annotations)
	}
	if got.Icons != nil {
		t.Errorf("absent icons became %s, want nil", got.Icons)
	}
}
