package viewer

import (
	"encoding/json"
	"testing"
)

// TestDedupeRecipesByCoverageID covers the coverage_id merge that the
// client's last-wins recipesByCoverageId map depends on (server.go finding
// #6 — previously untested). Two entries sharing a coverage_id but covering
// different agents must collapse into one entry whose by_adapter carries
// BOTH agents. Entries without a coverage_id pass through, and non-scenario
// top-level fields are preserved.
func TestDedupeRecipesByCoverageID(t *testing.T) {
	// repoRoot has no replaydata, so every expected.jsonl presence check is
	// false → first-occurrence wins and per-agent blocks merge by first set.
	repoRoot := t.TempDir()

	src := []byte(`{
	  "orchestrator_scenarios": ["passthrough"],
	  "scenarios": [
	    {"name":"a","coverage_id":"shared","by_adapter":{"codex":{"script":[1]}}},
	    {"name":"b","coverage_id":"shared","by_adapter":{"claudecode":{"script":[2]}}},
	    {"name":"solo","coverage_id":"only","by_adapter":{"pi":{"script":[3]}}},
	    {"name":"nocid","by_adapter":{"aider":{"script":[4]}}}
	  ]
	}`)

	out, err := dedupeRecipesByCoverageID(src, repoRoot)
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}

	var doc struct {
		OrchestratorScenarios []string `json:"orchestrator_scenarios"`
		Scenarios             []struct {
			Name       string                     `json:"name"`
			CoverageID string                     `json:"coverage_id"`
			ByAdapter  map[string]json.RawMessage `json:"by_adapter"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// Non-scenario field preserved.
	if len(doc.OrchestratorScenarios) != 1 || doc.OrchestratorScenarios[0] != "passthrough" {
		t.Errorf("orchestrator_scenarios not preserved: %+v", doc.OrchestratorScenarios)
	}

	byCid := map[string]map[string]json.RawMessage{}
	var noCidCount int
	for _, sc := range doc.Scenarios {
		if sc.CoverageID == "" {
			noCidCount++
			continue
		}
		if _, dup := byCid[sc.CoverageID]; dup {
			t.Errorf("coverage_id %q emitted more than once", sc.CoverageID)
		}
		byCid[sc.CoverageID] = sc.ByAdapter
	}

	// "shared" collapsed to one entry carrying both agents.
	shared, ok := byCid["shared"]
	if !ok {
		t.Fatal(`expected a "shared" entry`)
	}
	if _, ok := shared["codex"]; !ok {
		t.Error(`merged "shared" by_adapter missing codex`)
	}
	if _, ok := shared["claudecode"]; !ok {
		t.Error(`merged "shared" by_adapter missing claudecode`)
	}

	// Entry without a coverage_id passes through untouched.
	if noCidCount != 1 {
		t.Errorf("expected 1 entry without coverage_id, got %d", noCidCount)
	}
}
