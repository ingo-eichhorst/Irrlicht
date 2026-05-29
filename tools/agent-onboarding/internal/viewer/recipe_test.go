package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeShardRepo writes a t.TempDir repo with _meta.json + the named shards so
// the shard-backed recipe code (loadRecipeMap / handleRecipes) has data to read.
func writeShardRepo(t *testing.T, shards map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	scen := filepath.Join(dir, "replaydata", "scenarios")
	if err := os.MkdirAll(scen, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(scen, "_meta.json"), `{"min_versions":{"aider":"1.0.0","codex":"1.0.0"}}`)
	for name, body := range shards {
		mustWrite(t, filepath.Join(scen, name+".json"), body)
	}
	return dir
}

// TestLoadRecipeMap covers the shard-backed recipe index (#510): each shard is
// one coverage_id row; each agent's recipe block + recording folder come from
// the shard's per-agent Details.Recipe / RecordingDir.
func TestLoadRecipeMap(t *testing.T) {
	root := writeShardRepo(t, map[string]string{
		"x": `{
  "id": "1.1", "name": "x", "section": "S", "feature": "X",
  "agents": {
    "aider": {"recording_dir": "aider/scenarios/x", "details": {"recipe": {"applicable": true, "script": [{"type":"send"}]}}},
    "codex": {"details": {"recipe": {"applicable": false}}}
  }
}`,
	})
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
	// Folder resolves from the recording-dir basename.
	if got := resolveScenarioFolderForAgent(idx, "aider", "x"); got != "x" {
		t.Errorf("folder(aider,x) = %q; want x", got)
	}
	// codex has no recording → no folder.
	if got := resolveScenarioFolderForAgent(idx, "codex", "x"); got != "" {
		t.Errorf("folder(codex,x) = %q; want empty", got)
	}
}

// TestHandleRecipes checks the shard-backed /api/recipes surface: one entry per
// coverage_id, each carrying a by_adapter map of recipes.
func TestHandleRecipes(t *testing.T) {
	root := writeShardRepo(t, map[string]string{
		"a": `{"id":"1.1","name":"a","section":"S","feature":"A","agents":{"aider":{"details":{"recipe":{"script":[]}}}}}`,
	})
	srv := &Server{RepoRoot: root}
	req := httptest.NewRequest(http.MethodGet, "/api/recipes", nil)
	rec := httptest.NewRecorder()
	srv.handleRecipes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var doc struct {
		Scenarios []struct {
			Name       string                     `json:"name"`
			CoverageID string                     `json:"coverage_id"`
			ByAdapter  map[string]json.RawMessage `json:"by_adapter"`
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
