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
