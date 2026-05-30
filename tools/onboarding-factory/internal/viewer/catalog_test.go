package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestCatalogHandler exercises the shard-backed /api/catalog (#510): the
// skeleton + per-cell coverage come from the per-scenario shards, the row code
// is the shard ID, and the source header advertises "shards".
func TestCatalogHandler(t *testing.T) {
	dir := t.TempDir()
	rd := filepath.Join(dir, "replaydata")
	if err := os.MkdirAll(rd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rd, "scenarios.json"), []byte(`{
  "meta": {"min_versions": {"alphaagent": "1.0.0"}},
  "scenarios": [
    {"id": "1.1", "name": "alpha", "section": "S", "feature": "Alpha"},
    {"id": "1.2", "name": "beta", "section": "S", "feature": "Beta"}
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write alphaagent's cell for alpha as an id-prefixed metadata.json file.
	cellDir := filepath.Join(dir, "replaydata", "agents", "alphaagent", "scenarios", "1-1_alpha")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "metadata.json"),
		[]byte(`{"scenario_id": "alpha", "metadata": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready"}}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	srv := &Server{RepoRoot: dir}
	req := httptest.NewRequest(http.MethodGet, "/api/catalog", nil)
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Catalog-Source"); got != "shards" {
		t.Fatalf("X-Catalog-Source = %q, want shards", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	scns, ok := doc["scenarios"].([]any)
	if !ok || len(scns) != 2 {
		t.Fatalf("want 2 scenarios, got %v", doc["scenarios"])
	}
	first := scns[0].(map[string]any)
	if first["code"] != "1.1" {
		t.Fatalf("want code 1.1, got %v", first["code"])
	}
	if _, ok := first["coverage"].(map[string]any); !ok {
		t.Fatalf("want coverage map, got %v", first["coverage"])
	}
}

// TestDeriveDisplayState pins the display-state rollup the overview renders.
func TestDeriveDisplayState(t *testing.T) {
	cases := []struct {
		supports, daemon, driver string
		rec                      bool
		want                     string
	}{
		{"no", "full", "ready", true, "n.a."},
		{"unknown", "full", "ready", true, "unknown"},
		{"yes", "n/a", "ready", true, "n.a."},
		{"yes", "incapable", "ready", true, "unobservable"},
		{"yes", "bug", "ready", true, "blocked-daemon"},
		{"yes", "full", "gap:keys", true, "blocked-driver"},
		{"yes", "full", "ready", false, "pending-record"},
		{"yes", "full", "ready", true, "observed"},
	}
	for _, c := range cases {
		got := deriveDisplayState(c.supports, c.daemon, c.driver, c.rec)
		if got != c.want {
			t.Errorf("deriveDisplayState(%q,%q,%q,%v) = %q; want %q", c.supports, c.daemon, c.driver, c.rec, got, c.want)
		}
	}
}

// TestAnnotateDisplayState checks the in-place decoration over a catalog doc.
func TestAnnotateDisplayState(t *testing.T) {
	top := map[string]any{
		"scenarios": []any{
			map[string]any{
				"coverage": map[string]any{
					"claudecode": map[string]any{
						"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready",
						"measurement": map[string]any{"status": "pass"},
					},
				},
			},
		},
	}
	annotateDisplayState(top)
	sc := top["scenarios"].([]any)[0].(map[string]any)
	cov := sc["coverage"].(map[string]any)
	cell := cov["claudecode"].(map[string]any)
	if cell["display_state"] != "observed" {
		t.Errorf("display_state = %v; want observed", cell["display_state"])
	}
}

// TestNormalizeAdapter pins the slug map: only the hyphenated "claude-code"
// (and the empty string) collapse to "claudecode"; every other slug is
// returned unchanged.
func TestNormalizeAdapter(t *testing.T) {
	cases := map[string]string{
		"claude-code": "claudecode",
		"":            "claudecode",
		"claudecode":  "claudecode",
		"codex":       "codex",
		"aider":       "aider",
	}
	for in, want := range cases {
		got := normalizeAdapter(in)
		if got != want {
			t.Errorf("normalizeAdapter(%q) = %q; want %q", in, want, got)
		}
	}
}
