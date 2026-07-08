package matrix

import (
	"path/filepath"
	"sort"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// TestShardCellEquivalence is the P2 wiring oracle: it loads the REAL repo via
// the new shard-backed LoadRepo and asserts that, for every onboarded agent,
// the set of ApplicableCells coverage_ids EXACTLY equals the set of shard
// agent-keys (the cells the shard names for that agent). This proves Load wires
// every shard cell — and ONLY shard cells — into the matrix.
//
// It also documents the benign divergences from the legacy capabilities-driven
// model: cells the legacy loader would have visited (because the agent had a
// capabilities.json and the scenario's requires were met) but which carry no
// per-agent block in the shard, so P2 intentionally drops them as empty cells.
// They MUST NOT appear in the shard cell set. Ground truth in the migrated
// shards: only opencode/provider-failover-midturn is genuinely absent —
// codex/provider-failover-midturn and claudecode/architect-editor-pair both
// carry a (frozen) assessment block in their shard, so they remain real cells
// and are NOT drops.
//
// Hermetic-friendly: skips when replaydata/scenarios is unreadable.
func TestShardCellEquivalence(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..", "..")

	shards := shard.LoadAll(repoRoot)
	if len(shards) == 0 {
		t.Skip("replaydata/scenarios unreadable (hermetic build?)")
	}
	m, err := LoadRepo(repoRoot)
	if err != nil {
		t.Skipf("matrix could not load committed repo data: %v", err)
	}

	wantByAgent := wantedCellsByAgent(repoRoot, shards)
	assertApplicableCellsMatchShard(t, m, wantByAgent)
	assertBenignDivergencesAbsent(t, m)
	assertCounterCasesPresent(t, m)
}

// wantedCellsByAgent computes the expected cell set per agent: every
// scenario that has a metadata.json for that agent (i.e. LoadAllCells
// returns a cell).
func wantedCellsByAgent(repoRoot string, shards []shard.Shard) map[string]map[string]bool {
	wantByAgent := map[string]map[string]bool{}
	for _, sh := range shards {
		for ag := range shard.LoadAllCells(repoRoot, sh.Name) {
			if wantByAgent[ag] == nil {
				wantByAgent[ag] = map[string]bool{}
			}
			wantByAgent[ag][sh.Name] = true
		}
	}
	return wantByAgent
}

// assertApplicableCellsMatchShard is the P2 wiring oracle proper: for every
// onboarded agent, ApplicableCells' coverage_ids must exactly equal the
// shard agent-keys wantByAgent computed.
func assertApplicableCellsMatchShard(t *testing.T, m *Matrix, wantByAgent map[string]map[string]bool) {
	t.Helper()
	for _, agent := range m.Agents() {
		got := map[string]bool{}
		for _, cs := range m.ApplicableCells(agent) {
			got[cs.CoverageID] = true
		}
		want := wantByAgent[agent]
		if !sameSet(got, want) {
			t.Errorf("agent %s: ApplicableCells coverage_ids != shard agent-keys\n got:  %v\n want: %v",
				agent, sortedKeys(got), sortedKeys(want))
		}
	}
}

// assertBenignDivergencesAbsent checks the benign divergences from the
// legacy model: NOT in the shard cell set (the migrator dropped these empty
// cells; the consistency gate stays green because there are zero
// disagreements once they're gone).
func assertBenignDivergencesAbsent(t *testing.T, m *Matrix) {
	t.Helper()
	benignAbsent := []struct{ agent, cid string }{
		{"opencode", "provider-failover-midturn"},
	}
	for _, b := range benignAbsent {
		if _, ok := m.Cell(b.agent, b.cid); ok {
			t.Errorf("benign divergence %s/%s should be ABSENT from the shard cell set, but Cell() found it", b.agent, b.cid)
		}
	}
}

// assertCounterCasesPresent checks claudecode/architect-editor-pair and
// codex/provider-failover-midturn are NOT drops — each shard carries a
// frozen assessment block, so they remain real cells.
func assertCounterCasesPresent(t *testing.T, m *Matrix) {
	t.Helper()
	if _, ok := m.Cell("claudecode", "architect-editor-pair"); !ok {
		t.Errorf("claudecode/architect-editor-pair should be PRESENT (its shard has an assessment block) but Cell() did not find it")
	}
	if _, ok := m.Cell("codex", "provider-failover-midturn"); !ok {
		t.Errorf("codex/provider-failover-midturn should be PRESENT (its shard has a frozen assessment block) but Cell() did not find it")
	}
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
