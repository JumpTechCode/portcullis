package guard_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/guard"
)

// moduleRoot returns the repository root, located relative to this test file so
// the scan works regardless of the working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file via runtime.Caller")
	}
	// thisFile = <root>/internal/guard/scan_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func TestScanInternalErrorsOnMissingRoot(t *testing.T) {
	if _, err := guard.ScanInternal(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("ScanInternal on a missing root returned a nil error")
	}
}

func TestScanInternalReportsDomainAsLeaf(t *testing.T) {
	graph, err := guard.ScanInternal(moduleRoot(t))
	if err != nil {
		t.Fatalf("ScanInternal: %v", err)
	}
	imports, ok := graph["domain"]
	if !ok {
		t.Fatal("scan did not find the domain package")
	}
	if len(imports) != 0 {
		t.Errorf("domain imports internal packages %v, want none (it is the leaf)", imports)
	}
}

// The authoritative check: the real internal import graph obeys the allowed-edges
// DAG, with every internal package classified.
func TestInternalImportGraphObeysDAG(t *testing.T) {
	graph, err := guard.ScanInternal(moduleRoot(t))
	if err != nil {
		t.Fatalf("ScanInternal: %v", err)
	}
	var violations []guard.Violation
	for pkg, imports := range graph {
		violations = append(violations, guard.Check(pkg, imports)...)
	}
	for _, v := range violations {
		t.Errorf("forbidden import edge: %s", v.String())
	}
}
