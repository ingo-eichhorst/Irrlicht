package matrix

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRollupInSync is the CI guard for the rollup⟺assessment leg (#508 #2):
// agent-scenarios-coverage.json is DERIVED from the assessments, so if an
// assessment's axes change without `matrix rollup` being re-run, the committed
// file goes stale and this test fails. It is the leg #507's consistency-gate
// left uncovered (that gate compares assessment⟺scenarios; this one covers
// rollup⟺assessment). Skips gracefully when the repo tree is unreachable
// (hermetic build), like the sibling catalog tests.
//
// Regenerating is a deterministic fixpoint: editorial notes + legend are
// carried forward from the committed file, axes are re-derived, so an unchanged
// tree reproduces identical bytes.
func TestRollupInSync(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..", "..")
	rollupPath := filepath.Join(repoRoot, ".claude", "skills", "ir:onboard-agent", "agent-scenarios-coverage.json")

	committed, err := os.ReadFile(rollupPath)
	if err != nil {
		t.Skipf("rollup file unreachable (hermetic build?): %v", err)
	}
	m, err := LoadRepo(repoRoot)
	if err != nil {
		t.Skipf("matrix could not load committed repo data: %v", err)
	}

	generated, err := MarshalRollup(m.BuildRollup(ReadOverlay(rollupPath)))
	if err != nil {
		t.Fatalf("marshal rollup: %v", err)
	}
	if !bytes.Equal(committed, generated) {
		t.Errorf("agent-scenarios-coverage.json is STALE — an assessment changed without regenerating the rollup.\n" +
			"Run `matrix rollup` (cmd/matrix) and commit the result.")
	}
}

// TestReadOverlayCarriesGeneratedAt pins the #510-P2 stable-fixpoint mechanism:
// ReadOverlay MUST parse `generated_at` from the committed rollup so BuildRollup
// can carry it forward (a cell re-pointed to a folder with an earlier
// assessed_at must not lower the emitted timestamp and spuriously fail
// `rollup --check`). Regression guard: the carry-forward branch was once dead
// because ReadOverlay didn't parse the field, so it silently fell back to the
// derived max every time.
func TestReadOverlayCarriesGeneratedAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-scenarios-coverage.json")
	const stamp = "2099-01-02T03:04:05Z"
	if err := os.WriteFile(path, []byte(`{
  "generated_at": "`+stamp+`",
  "legend": {"k": "v"},
  "source_catalog": "x",
  "scenarios": [{"id": "s", "coverage": {"ag": {"notes": "n"}}}]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ov := ReadOverlay(path)
	if ov.GeneratedAt != stamp {
		t.Fatalf("ReadOverlay did not parse generated_at: got %q want %q", ov.GeneratedAt, stamp)
	}

	// An empty Matrix derives maxAssessedAt == "" (no assessments), so without
	// the carry-forward BuildRollup would emit "". With it, the overlay stamp wins.
	doc := (&Matrix{}).BuildRollup(ov)
	if doc.GeneratedAt != stamp {
		t.Fatalf("BuildRollup did not carry forward generated_at: got %q want %q (carry-forward is dead)", doc.GeneratedAt, stamp)
	}
}
