package museaccountapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertNoProductionFileContains scans every non-test .go file in this
// package's own directory and fails if any contains forbidden as a literal
// substring, reporting msg for the offending file. Shared (found by review,
// issue #2007) between TestCredentialResolver_NeverCallsReveal and
// TestMuseDestination_NeverDialsInTheOrdinarySuite's directory-scan half —
// both had grown the identical ~20-line walk independently before this
// extraction. Callers must never pass a forbidden string that also appears
// in their OWN test file's source (e.g. inside this very call), since a
// caller lives in a _test.go file and this function already excludes those
// — see TestCredentialResolver_NeverCallsReveal's and
// TestMuseDestination_NeverDialsInTheOrdinarySuite's own doc comments for
// the self-match risk that shape guards against.
func assertNoProductionFileContains(t *testing.T, forbidden, msg string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if strings.Contains(string(data), forbidden) {
			t.Errorf("%s: %s", name, msg)
		}
	}
	if scanned == 0 {
		t.Fatal("vacuity: scanned zero production .go files — this test verified nothing")
	}
}
