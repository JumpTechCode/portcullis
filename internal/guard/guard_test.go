package guard_test

import (
	"strings"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/guard"
)

func TestCheckAllowsDeclaredEdges(t *testing.T) {
	cases := []struct {
		pkg     string
		imports []string
	}{
		{"domain", nil},
		{"policy", []string{"domain"}},
		{"redact", []string{"domain"}},
		{"pipeline", []string{"domain"}},
		{"edge", []string{"domain", "pipeline"}},
		{"app", []string{"domain", "policy", "redact", "pipeline", "edge", "registry"}},
	}
	for _, c := range cases {
		t.Run(c.pkg, func(t *testing.T) {
			if vs := guard.Check(c.pkg, c.imports); len(vs) != 0 {
				t.Errorf("Check(%q, %v) = %v, want no violations", c.pkg, c.imports, vs)
			}
		})
	}
}

func TestCheckRejectsForbiddenEdges(t *testing.T) {
	cases := []struct {
		name    string
		pkg     string
		imports []string
		badImp  string
	}{
		{"leaf importing a sibling", "domain", []string{"config"}, "config"},
		{"concrete importing a concrete", "policy", []string{"domain", "aggregate"}, "aggregate"},
		{"edge importing a concrete", "edge", []string{"pipeline", "secrets"}, "secrets"},
		{"pipeline importing edge", "pipeline", []string{"edge"}, "edge"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := guard.Check(c.pkg, c.imports)
			if len(vs) == 0 {
				t.Fatalf("Check(%q, %v) = no violations, want one for %q", c.pkg, c.imports, c.badImp)
			}
			found := false
			for _, v := range vs {
				if v.Pkg == c.pkg && v.Import == c.badImp {
					found = true
				}
			}
			if !found {
				t.Errorf("Check(%q, %v) = %v, want a violation importing %q", c.pkg, c.imports, vs, c.badImp)
			}
		})
	}
}

// A package importing its own subpackage (for example registry importing
// registry/pool) is the same top-level package and must not be a violation.
func TestCheckIgnoresSelfImport(t *testing.T) {
	if vs := guard.Check("registry", []string{"registry", "domain"}); len(vs) != 0 {
		t.Errorf("Check(registry, [registry domain]) = %v, want no violations", vs)
	}
}

func TestViolationStringNamesBothPackages(t *testing.T) {
	got := guard.Violation{Pkg: "policy", Import: "aggregate"}.String()
	if !strings.Contains(got, "policy") || !strings.Contains(got, "aggregate") {
		t.Errorf("String() = %q, want it to name both internal/policy and internal/aggregate", got)
	}
}

// An internal package missing from the allowed-edges map must itself be a
// violation, so adding a new package forces a deliberate classification rather
// than silently escaping the guard.
func TestCheckRejectsUnclassifiedPackage(t *testing.T) {
	vs := guard.Check("brandnew", []string{"domain"})
	if len(vs) == 0 {
		t.Fatal("Check on an unclassified package returned no violations")
	}
	if !strings.Contains(strings.ToLower(vs[0].String()), "brandnew") {
		t.Errorf("violation %q does not name the unclassified package", vs[0].String())
	}
}
