package shard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNoOrphanRecordingFolders asserts every RECORDED scenarios/<folder> (one
// holding events.jsonl) under replaydata/agents/<adapter>/ is referenced by
// some shard — either a shard's name (coverage_id) or an agent cell's
// recording_dir basename. An orphan folder has no shard to --re-record from and
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
	scenDir := Dir(repoRoot)
	if _, err := os.Stat(scenDir); err != nil {
		t.Skipf("no shard dir at %s: %v", scenDir, err)
	}
	shards := LoadAll(repoRoot)
	if len(shards) == 0 {
		t.Fatalf("no shards under %s", scenDir)
	}

	// legit[adapter] = the set of folder names an adapter's recording may use.
	legit := map[string]map[string]bool{}
	for _, sh := range shards {
		for agent, cell := range sh.Agents {
			if legit[agent] == nil {
				legit[agent] = map[string]bool{}
			}
			legit[agent][sh.Name] = true // the coverage_id dir
			if cell.RecordingDir != "" {
				legit[agent][filepath.Base(cell.RecordingDir)] = true // a variant folder
			}
		}
	}

	agentsRoot := filepath.Join(repoRoot, "replaydata", "agents")
	adapters, err := os.ReadDir(agentsRoot)
	if err != nil {
		t.Fatalf("read agents root %s: %v", agentsRoot, err)
	}
	for _, ad := range adapters {
		if !ad.IsDir() {
			continue
		}
		agent := ad.Name()
		scDir := filepath.Join(agentsRoot, agent, "scenarios")
		folders, err := os.ReadDir(scDir)
		if err != nil {
			continue // adapter has no scenarios/ tree (e.g. only regression/)
		}
		for _, fe := range folders {
			if !fe.IsDir() {
				continue
			}
			folder := fe.Name()
			// Only RECORDED folders are load-bearing; a bare/partial dir isn't
			// an orphan recording.
			if _, err := os.Stat(filepath.Join(scDir, folder, "events.jsonl")); err != nil {
				continue
			}
			if !legit[agent][folder] {
				t.Errorf("orphan recording folder replaydata/agents/%s/scenarios/%s — referenced by no shard (neither a shard name nor a recording_dir). Wire it into a shard, or git mv it to regression/.", agent, folder)
			}
		}
	}
}
