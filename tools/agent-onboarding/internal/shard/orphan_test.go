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

	// legit[adapter] = the set of recording folders that are LIVE cells — every
	// folder holding a metadata.json. A recorded folder without a metadata.json
	// is an orphan (no cell descriptor to --re-record from).
	legit := map[string]map[string]bool{}
	agentsRoot := filepath.Join(repoRoot, "replaydata", "agents")
	if adapters, err := os.ReadDir(agentsRoot); err == nil {
		for _, ad := range adapters {
			if !ad.IsDir() {
				continue
			}
			agent := ad.Name()
			scDir := filepath.Join(agentsRoot, agent, "scenarios")
			folders, err := os.ReadDir(scDir)
			if err != nil {
				continue
			}
			for _, fe := range folders {
				if !fe.IsDir() {
					continue
				}
				folder := fe.Name()
				if _, err := os.Stat(filepath.Join(scDir, folder, "metadata.json")); err == nil {
					if legit[agent] == nil {
						legit[agent] = map[string]bool{}
					}
					legit[agent][folder] = true
				}
			}
		}
	}

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
				t.Errorf("orphan recording folder replaydata/agents/%s/scenarios/%s — holds no metadata.json. Add a cell descriptor, or git mv it to regression/.", agent, folder)
			}
		}
	}
}
