// Package guard enforces Portcullis's internal import DAG (design §3).
//
// The architecture allows only a fixed set of import edges between internal
// packages: domain is a leaf, concrete packages depend only on domain, and a
// single composition root wires them together. This package encodes those
// allowed edges and checks them mechanically, so a forbidden import fails the
// build rather than only being documented. It is the analogue of a structural
// lint rule, expressed as a test that parses the source tree.
package guard

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ModulePath is the Go module path. Internal packages live under
// ModulePath + "/internal/".
const ModulePath = "github.com/JumpTechCode/portcullis"

const internalPrefix = ModulePath + "/internal/"

// allowedEdges maps each internal package (its top-level name under internal/)
// to the set of internal packages it may import. A package absent from this map
// is unclassified and treated as a violation, so adding a new internal package
// forces a deliberate classification here.
var allowedEdges = map[string]map[string]bool{
	"domain":     {},
	"guard":      {},
	"config":     {"domain": true},
	"policy":     {"domain": true},
	"secrets":    {"domain": true},
	"redact":     {"domain": true},
	"resilience": {"domain": true},
	"audit":      {"domain": true},
	"metrics":    {"domain": true},
	"aggregate":  {"domain": true},
	"registry":   {"domain": true},
	"pipeline":   {"domain": true},
	"edge":       {"domain": true, "pipeline": true},
	"watch":      {},
}

// rootPackages are composition roots permitted to import any internal package.
var rootPackages = map[string]bool{
	"app": true,
}

// unclassifiedMarker is placed in a Violation's Import field when the importing
// package itself is not classified in allowedEdges.
const unclassifiedMarker = "<unclassified>"

// Violation is a forbidden internal import edge: package Pkg importing package
// Import, where that edge is not allowed by the DAG.
type Violation struct {
	// Pkg is the importing internal package's top-level name.
	Pkg string
	// Import is the imported internal package's top-level name, or a marker when
	// Pkg itself is unclassified.
	Import string
}

// String renders the violation as a human-readable message.
func (v Violation) String() string {
	if v.Import == unclassifiedMarker {
		return fmt.Sprintf("internal package %q is not classified in the import guard; add it to allowedEdges", v.Pkg)
	}
	return fmt.Sprintf("internal/%s must not import internal/%s", v.Pkg, v.Import)
}

// Check returns the violations for a single internal package given the
// top-level names of the internal packages it imports. A package that is not a
// composition root and is absent from the allowed-edges map yields a single
// "unclassified" violation. Imports of a package by itself are ignored.
func Check(pkg string, internalImports []string) []Violation {
	if rootPackages[pkg] {
		return nil
	}
	allowed, known := allowedEdges[pkg]
	if !known {
		return []Violation{{Pkg: pkg, Import: unclassifiedMarker}}
	}

	var violations []Violation
	for _, imp := range internalImports {
		if imp == pkg || allowed[imp] {
			continue
		}
		violations = append(violations, Violation{Pkg: pkg, Import: imp})
	}
	sort.Slice(violations, func(i, j int) bool {
		return violations[i].Import < violations[j].Import
	})
	return violations
}

// ScanInternal parses every non-test .go file under moduleRoot/internal and
// returns, for each top-level internal package, the sorted unique set of
// internal packages it imports. Test files are excluded: the architecture
// invariant constrains production code, and external test packages legitimately
// import packages a production file may not.
func ScanInternal(moduleRoot string) (map[string][]string, error) {
	root := filepath.Join(moduleRoot, "internal")
	sets := make(map[string]map[string]bool)
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkg := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]

		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}

		if sets[pkg] == nil {
			sets[pkg] = make(map[string]bool)
		}
		for _, spec := range f.Imports {
			impPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("unquoting import in %s: %w", path, err)
			}
			if !strings.HasPrefix(impPath, internalPrefix) {
				continue
			}
			imported := strings.SplitN(strings.TrimPrefix(impPath, internalPrefix), "/", 2)[0]
			if imported != pkg {
				sets[pkg][imported] = true
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	out := make(map[string][]string, len(sets))
	for pkg, set := range sets {
		list := make([]string, 0, len(set))
		for imp := range set {
			list = append(list, imp)
		}
		sort.Strings(list)
		out[pkg] = list
	}
	return out, nil
}
