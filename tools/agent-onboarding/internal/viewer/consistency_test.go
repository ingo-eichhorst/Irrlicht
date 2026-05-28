package viewer

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAssessmentScenarioConsistency pins the SEMANTIC companion to the
// structural catalog-drift / bijection guards: for every un-recorded cell,
// the assessment.json verdict and scenarios.json by_adapter.applicable must
// tell the same story. The completeness-gate resolves any disagreement
// SILENTLY (its recipe_false branch marks a cell terminal the moment
// scenarios.json says applicable:false, without checking the assessment) — so
// a cell can show daemon=full/driver=ready in the viewer AND applicable:false
// in the matrix at once, recorded by nobody. That is exactly how
// pi/streaming-partial-writes drifted; a 129-file schema migration (#480)
// re-blessed the stale optimistic verdict and no gate caught it.
//
// The authoritative check lives in scripts/lib/consistency-gate.sh (with its
// own bash unit test). This test shells out to it against the COMMITTED repo
// data so a future desync fails CI here too. It skips gracefully when bash/jq
// or the repo tree are unreachable (hermetic build), exactly like the sibling
// catalog tests skip on an unreachable scenarios.json.
func TestAssessmentScenarioConsistency(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	gate := filepath.Join(root, ".claude", "skills", "ir:onboard-agent", "scripts", "lib", "consistency-gate.sh")
	scenarios := filepath.Join(root, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	agentsRoot := filepath.Join(root, "replaydata", "agents")

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH; the consistency gate is bash — skipping")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH; the consistency gate needs jq — skipping")
	}

	cmd := exec.Command("bash", gate, scenarios, agentsRoot)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Exit 2 means the gate could not run (e.g. files unreachable); skip
		// rather than fail, matching the sibling catalog tests' hermetic guard.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 2 {
			t.Skipf("consistency gate could not run (exit 2): %s", strings.TrimSpace(string(out)))
		}
		t.Errorf("assessment ⟺ scenarios consistency gate failed:\n%s", string(out))
	}
}
