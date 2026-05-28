package viewer

import (
	"path/filepath"
	"testing"

	"irrlicht/tools/agent-onboarding/internal/matrix"
)

// TestAssessmentScenarioConsistency pins the SEMANTIC companion to the
// structural catalog-drift / bijection guards: for every un-recorded cell, the
// assessment.json verdict and scenarios.json by_adapter.applicable must tell
// the same story. A cell can otherwise show daemon=full/driver=ready in the
// viewer AND applicable:false in the matrix at once, recorded by nobody — that
// is exactly how pi/streaming-partial-writes drifted (#507).
//
// Since #508 the authoritative check is internal/matrix.Disagreements (the same
// code path the consistency-gate.sh thin client and the matrix CLI run). This
// test runs it against the COMMITTED repo data so a future desync fails CI
// here too. It skips gracefully when the repo tree is unreachable (hermetic
// build), exactly like the sibling catalog tests skip on an unreachable
// scenarios.json.
func TestAssessmentScenarioConsistency(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	m, err := matrix.LoadRepo(root)
	if err != nil {
		t.Skipf("matrix could not load committed repo data (hermetic build?): %v", err)
	}
	for _, d := range m.Disagreements() {
		t.Errorf("assessment ⟺ scenarios disagree: %s", d.Message)
	}
}
