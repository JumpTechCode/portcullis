package domain_test

import (
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

func TestToolRefNamespaced(t *testing.T) {
	ref := domain.ToolRef{Downstream: "github", Tool: "create_issue"}
	if got, want := ref.Namespaced(), "github__create_issue"; got != want {
		t.Errorf("Namespaced() = %q, want %q", got, want)
	}
}

func TestToolRefStringMatchesNamespaced(t *testing.T) {
	ref := domain.ToolRef{Downstream: "search", Tool: "query"}
	if got, want := ref.String(), ref.Namespaced(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParseToolRefValid(t *testing.T) {
	ref, err := domain.ParseToolRef("github__create_issue")
	if err != nil {
		t.Fatalf("ParseToolRef returned unexpected error: %v", err)
	}
	if ref.Downstream != "github" || ref.Tool != "create_issue" {
		t.Errorf("ParseToolRef = %+v, want {github create_issue}", ref)
	}
}

func TestParseToolRefRoundTrip(t *testing.T) {
	want := domain.ToolRef{Downstream: "github", Tool: "create_issue"}
	got, err := domain.ParseToolRef(want.Namespaced())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// A downstream tool name that itself contains the separator must be rejected so
// it can never be silently mis-routed (design §7: fail fast at load).
func TestParseToolRefRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"no separator":            "createissue",
		"empty":                   "",
		"empty downstream":        "__create_issue",
		"empty tool":              "github__",
		"tool contains separator": "github__create__issue",
		"only separator":          "__",
		"three parts":             "a__b__c",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := domain.ParseToolRef(input); err == nil {
				t.Errorf("ParseToolRef(%q) = nil error, want error", input)
			}
		})
	}
}
