package shard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNoOrphanRecordingFolders asserts every RECORDED scenarios/<folder> (one
// holding events.jsonl) under replaydata/agents/<adapter>/ is referenced by
// a live cell — i.e. it holds a metadata.json. An orphan folder has no cell
// descriptor to --re-record from and
// rots silently. #511 retired the pre-existing orphans (some deleted, some
// git-mv'd to regression/) and this guard keeps new ones from creeping back in.
//
// It replaces the orphan-detection that the retired TestScenarioCatalogNoDrift
// gate used to provide, and mirrors the bash gate (lib/cell-integrity.sh +
// shard-lib's shard_recipe_dir_names), which enforces the same invariant at
// record time.
func TestNoOrphanRecordingFolders(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	catalogFile := File(repoRoot)
	if _, err := os.Stat(catalogFile); err != nil {
		t.Skipf("no catalog at %s: %v", catalogFile, err)
	}
	shards := LoadAll(repoRoot)
	if len(shards) == 0 {
		t.Fatalf("no scenarios in %s", catalogFile)
	}

	agentsRoot := filepath.Join(repoRoot, "replaydata", "agents")
	legit := legitCellsByAgent(agentsRoot)

	adapters, err := os.ReadDir(agentsRoot)
	if err != nil {
		t.Fatalf("read agents root %s: %v", agentsRoot, err)
	}
	for _, ad := range adapters {
		if !ad.IsDir() {
			continue
		}
		agent := ad.Name()
		reportOrphanRecordings(t, agentsRoot, agent, legit[agent])
	}
}

// scenarioFolders lists the scenario subdirectory names under
// agentsRoot/agent/scenarios/, or nil if that tree doesn't exist (e.g. an
// adapter with only a regressions/ tree).
func scenarioFolders(agentsRoot, agent string) []string {
	entries, err := os.ReadDir(filepath.Join(agentsRoot, agent, "scenarios"))
	if err != nil {
		return nil
	}
	var out []string
	for _, fe := range entries {
		if fe.IsDir() {
			out = append(out, fe.Name())
		}
	}
	return out
}

// markLegit records that agent/folder carries a cell descriptor, creating
// the agent's inner set on first use.
func markLegit(legit map[string]map[string]bool, agent, folder string) {
	if legit[agent] == nil {
		legit[agent] = map[string]bool{}
	}
	legit[agent][folder] = true
}

// legitCellsByAgent returns, for every adapter under agentsRoot, the set of
// scenario folder names that are LIVE cells — every folder holding a
// metadata.json. A recorded folder without a metadata.json is an orphan (no
// cell descriptor to --re-record from). A missing/unreadable agentsRoot
// yields an empty map rather than an error, matching the caller's original
// silent-best-effort behavior for this lookup.
func legitCellsByAgent(agentsRoot string) map[string]map[string]bool {
	legit := map[string]map[string]bool{}
	adapters, err := os.ReadDir(agentsRoot)
	if err != nil {
		return legit
	}
	for _, ad := range adapters {
		if !ad.IsDir() {
			continue
		}
		agent := ad.Name()
		for _, folder := range scenarioFolders(agentsRoot, agent) {
			if _, err := os.Stat(filepath.Join(agentsRoot, agent, "scenarios", folder, "metadata.json")); err == nil {
				markLegit(legit, agent, folder)
			}
		}
	}
	return legit
}

// reportOrphanRecordings errors on every RECORDED scenario folder (holding
// events.jsonl) under agentsRoot/agent/scenarios/ that isn't in legit — i.e.
// has no metadata.json cell descriptor. A bare/partial (unrecorded) folder
// isn't an orphan recording, so it's silently skipped.
func reportOrphanRecordings(t *testing.T, agentsRoot, agent string, legit map[string]bool) {
	t.Helper()
	for _, folder := range scenarioFolders(agentsRoot, agent) {
		if _, err := os.Stat(filepath.Join(agentsRoot, agent, "scenarios", folder, "events.jsonl")); err != nil {
			continue
		}
		if !legit[folder] {
			t.Errorf("orphan recording folder replaydata/agents/%s/scenarios/%s — holds no metadata.json. Add a cell descriptor, or git mv it to regression/.", agent, folder)
		}
	}
}
