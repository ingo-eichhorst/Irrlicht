package driverteardown

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const verdictsGolden = "testdata/verdicts.golden"

// TestVerdictsGolden pins the complete public verdict of every committed
// fixture and every real recording driver. The focused tests in
// driverteardown_test.go pin which invariant fires; this golden also pins the
// Detail and refusal text that tells a driver author what to fix.
//
// Refresh with:
//
//	UPDATE_REPLAY_GOLDENS=1 go test ./tools/onboarding-factory/internal/driverteardown/... -run TestVerdictsGolden -count=1
func TestVerdictsGolden(t *testing.T) {
	got := renderAllVerdicts(t)
	if os.Getenv("UPDATE_REPLAY_GOLDENS") == "1" {
		if err := os.WriteFile(verdictsGolden, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", verdictsGolden, err)
		}
		return
	}

	want, err := os.ReadFile(verdictsGolden)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_REPLAY_GOLDENS=1 to create)",
			verdictsGolden, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("driver-teardown verdicts differ from %s\n"+
			"run UPDATE_REPLAY_GOLDENS=1 go test ./tools/onboarding-factory/internal/driverteardown/... -run TestVerdictsGolden -count=1 to refresh\n"+
			"first difference: %s", verdictsGolden, firstVerdictDiff(got, want))
	}
}

func renderAllVerdicts(t *testing.T) []byte {
	t.Helper()
	var out strings.Builder

	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}
	var fixtures []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sh") {
			fixtures = append(fixtures, entry.Name())
		}
	}
	sort.Strings(fixtures)
	if len(fixtures) == 0 {
		t.Fatal("no driver-teardown fixtures discovered; the golden would grade nothing")
	}
	for _, name := range fixtures {
		driver, libs := loadFixture(t, name, fixtureLibs(name)...)
		findings, checkErr := CheckDriver(driver, libs)
		writeVerdict(&out, "fixture/"+name, findings, checkErr)
	}

	root := repoRoot(t)
	adapters, err := Adapters(root)
	if err != nil {
		t.Fatalf("enumerate real recording drivers: %v", err)
	}
	if len(adapters) == 0 {
		t.Fatal("no real recording drivers discovered; the golden would grade no live inputs")
	}
	for _, adapter := range adapters {
		driver, libs, err := LoadDriver(root, adapter)
		if err != nil {
			t.Fatalf("load real recording driver %s: %v", adapter, err)
		}
		findings, checkErr := CheckDriver(driver, libs)
		writeVerdict(&out, "driver/"+adapter, findings, checkErr)
	}

	// Findings from real drivers carry absolute paths. None exists in the
	// approved baseline, but normalize the root so a future intentional golden
	// update cannot commit one developer's checkout path.
	rendered := strings.TrimSuffix(out.String(), "\n")
	normalized := strings.ReplaceAll(rendered, root+string(filepath.Separator), "<repo>/")
	return []byte(normalized)
}

func writeVerdict(out *strings.Builder, name string, findings []Finding, err error) {
	fmt.Fprintf(out, "=== %s ===\n", name)
	if len(findings) == 0 && err == nil {
		out.WriteString("CLEAN\n\n")
		return
	}
	for _, finding := range findings {
		out.WriteString(finding.String())
		out.WriteByte('\n')
	}
	if err != nil {
		out.WriteString("REFUSED: ")
		out.WriteString(err.Error())
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
}

func firstVerdictDiff(got, want []byte) string {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			return fmt.Sprintf("%s, byte %d: got %q, want %q",
				verdictHeaderAt(got, i), i, got[i], want[i])
		}
	}
	return fmt.Sprintf("%s, length: got %d bytes, want %d",
		verdictHeaderAt(got, limit), len(got), len(want))
}

func verdictHeaderAt(rendered []byte, offset int) string {
	if offset > len(rendered) {
		offset = len(rendered)
	}
	start := bytes.LastIndex(rendered[:offset], []byte("=== "))
	if start < 0 {
		return "before the first verdict header"
	}
	end := bytes.IndexByte(rendered[start:], '\n')
	if end < 0 {
		return string(rendered[start:])
	}
	return string(rendered[start : start+end])
}
