package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeShardRepo writes a t.TempDir repo with a consolidated scenarios.json
// (meta + the named scenarios). Agent cell data is written via writeAgentCell.
// `shards` maps scenario name → its scenario-object JSON (global fields only).
func writeShardRepo(t *testing.T, shards map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	rd := filepath.Join(dir, "replaydata")
	if err := os.MkdirAll(filepath.Join(rd, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	parts := make([]string, 0, len(shards))
	for _, body := range shards {
		parts = append(parts, body)
	}
	catalog := `{"meta":{"min_versions":{"aider":"1.0.0","codex":"1.0.0"}},"scenarios":[` +
		strings.Join(parts, ",") + `]}`
	mustWrite(t, filepath.Join(rd, "agents", "scenarios.json"), catalog)
	return dir
}

// writeAgentCell writes a metadata.json for one (adapter, folder) cell.
func writeAgentCell(t *testing.T, repoRoot, adapter, folder, body string) {
	t.Helper()
	d := filepath.Join(repoRoot, "replaydata", "agents", adapter, "scenarios", folder)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(d, "metadata.json"), body)
}

// TestLoadRecipeMap covers the recipe index: each scenario is one coverage_id
// row; each agent's recipe block + recording folder come from its metadata.json
// (keyed by scenario_id).
func TestLoadRecipeMap(t *testing.T) {
	root := writeShardRepo(t, map[string]string{
		"x": `{"id": "1.1", "name": "x", "section": "S", "feature": "X"}`,
	})
	writeAgentCell(t, root, "aider", "1-1_x",
		`{"scenario_id": "x", "details": {"recipe": {"applicable": true, "script": [{"type":"send"}]}}}`)
	writeAgentCell(t, root, "codex", "1-1_x",
		`{"scenario_id": "x", "details": {"recipe": {"applicable": false}}}`)
	idx := loadRecipeMap(root)

	rec, ok := idx.canonical["x"]
	if !ok {
		t.Fatalf("canonical[x] missing")
	}
	aider, ok := rec.ByAdapter["aider"]
	if !ok || aider.Applicable == nil || !*aider.Applicable || len(aider.Script) != 1 {
		t.Errorf("aider recipe wrong: %+v", aider)
	}
	codex, ok := rec.ByAdapter["codex"]
	if !ok || codex.Applicable == nil || *codex.Applicable {
		t.Errorf("codex recipe should be applicable:false, got: %+v", codex)
	}
	// Folder resolves from the cell's on-disk folder name (the single source of
	// truth) — present for any agent that has a cell.
	if got, ok := resolveScenarioFolderForAgent(idx, "aider", "x"); !ok || got != "1-1_x" {
		t.Errorf("folder(aider,x) = %q,%v; want 1-1_x,true", got, ok)
	}
	if got, ok := resolveScenarioFolderForAgent(idx, "codex", "x"); !ok || got != "1-1_x" {
		t.Errorf("folder(codex,x) = %q,%v; want 1-1_x,true", got, ok)
	}
	// An agent with no cell for the scenario → ok=false (callers must skip).
	if got, ok := resolveScenarioFolderForAgent(idx, "pi", "x"); ok || got != "" {
		t.Errorf("folder(pi,x) = %q,%v; want \"\",false", got, ok)
	}
}

// TestHandleRecipes checks the shard-backed /api/recipes surface: one entry per
// coverage_id, each carrying a by_adapter map of recipes.
func TestHandleRecipes(t *testing.T) {
	// aider records under the coverage_id folder; codex records under a
	// VARIANT folder (basename != coverage_id) — exercises folder_by_agent.
	root := writeShardRepo(t, map[string]string{
		"a": `{"id":"1.1","name":"a","section":"S","feature":"A"}`,
	})
	writeAgentCell(t, root, "aider", "1-1_a",
		`{"scenario_id":"a","details":{"recipe":{"script":[]}}}`)
	writeAgentCell(t, root, "codex", "1-1_a-variant",
		`{"scenario_id":"a","details":{"recipe":{"script":[]}}}`)
	srv := &Server{RepoRoot: root}
	req := httptest.NewRequest(http.MethodGet, "/api/recipes", nil)
	rec := httptest.NewRecorder()
	srv.handleRecipes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var doc struct {
		Scenarios []struct {
			Name          string                     `json:"name"`
			CoverageID    string                     `json:"coverage_id"`
			ByAdapter     map[string]json.RawMessage `json:"by_adapter"`
			FolderByAgent map[string]string          `json:"folder_by_agent"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Scenarios) != 1 {
		t.Fatalf("want 1 scenario, got %d", len(doc.Scenarios))
	}
	s := doc.Scenarios[0]
	if s.Name != "a" || s.CoverageID != "a" {
		t.Errorf("name/coverage_id = %q/%q; want a/a", s.Name, s.CoverageID)
	}
	if _, ok := s.ByAdapter["aider"]; !ok {
		t.Errorf("by_adapter missing aider: %+v", s.ByAdapter)
	}
	// folder_by_agent resolves the variant folder per agent (the #511 fix the
	// viewer.js recording link depends on): coverage_id for aider, the variant
	// basename for codex.
	if got := s.FolderByAgent["aider"]; got != "1-1_a" {
		t.Errorf("folder_by_agent[aider] = %q; want 1-1_a", got)
	}
	if got := s.FolderByAgent["codex"]; got != "1-1_a-variant" {
		t.Errorf("folder_by_agent[codex] = %q; want 1-1_a-variant", got)
	}
}

// mustWrite is a tiny test helper, shared with server_test.go.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
