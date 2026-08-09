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
	if err := os.MkdirAll(filepath.Join(rd, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rd, "agents", "scenarios.json"), []byte(`{
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
		rec, applic              bool
		want                     string
	}{
		{"no", "full", "ready", true, true, "n/a"},
		{"unknown", "full", "ready", true, true, "unknown"},
		{"yes", "n/a", "ready", true, true, "n/a"},
		{"yes", "incapable", "ready", true, true, "unobservable"},
		{"yes", "bug", "ready", true, true, "blocked-daemon"},
		{"yes", "full", "gap:keys", true, true, "blocked-driver"},
		{"yes", "full", "ready", false, true, "pending-record"},
		// applicable:false (record_blocked deferral), not recorded → n/a.
		{"yes", "full", "ready", false, false, "n/a"},
		{"yes", "full", "ready", true, true, "observed"},
	}
	for _, c := range cases {
		got := deriveDisplayState(c.supports, c.daemon, c.driver, c.rec, c.applic)
		if got != c.want {
			t.Errorf("deriveDisplayState(%q,%q,%q,rec=%v,applic=%v) = %q; want %q", c.supports, c.daemon, c.driver, c.rec, c.applic, got, c.want)
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

// TestCatalogRendersDerivedCellsFromCapabilityModel is the regression test for
// the #1369 review finding: the catalog endpoint re-scans shards and never
// consulted the capability model, so a pair the model declares structurally
// dead — and which therefore has no directory, that being the point — rendered
// as "unknown". `of status` reported n/a/unobservable for the same pair, and
// `unknown` is precisely the state the maturity model reads as "not assessed".
func TestCatalogRendersDerivedCellsFromCapabilityModel(t *testing.T) {
	dir := t.TempDir()
	ag := filepath.Join(dir, "replaydata", "agents")
	if err := os.MkdirAll(ag, 0o755); err != nil {
		t.Fatal(err)
	}
	// user-esc-interrupt is gated by the `interrupt` trait; codex is a real
	// registry adapter, so it appears as a coverage column.
	if err := os.WriteFile(filepath.Join(ag, "scenarios.json"), []byte(`{
  "meta": {"min_versions": {"codex": "1.0.0"}},
  "scenarios": [{"id": "2.20", "name": "user-esc-interrupt", "section": "S", "feature": "Interrupt"}]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ag, "adapters.json"), []byte(`{
  "schema_version": 1,
  "adapters": {"codex": {"maturity": "alpha", "capabilities": {"interrupt": "untraced"}}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &Server{RepoRoot: dir}
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc struct {
		Scenarios []struct {
			Coverage map[string]map[string]any `json:"coverage"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Scenarios) != 1 {
		t.Fatalf("want 1 scenario, got %d", len(doc.Scenarios))
	}
	cell := doc.Scenarios[0].Coverage["codex"]
	if cell == nil {
		t.Fatal("no codex coverage entry")
	}
	// untraced ⇒ the agent HAS the feature but it reaches no Source: the same
	// axes `of status` synthesizes, which derive "unobservable".
	for k, want := range map[string]any{
		"agent_supports": "yes", "daemon_capability": "incapable",
		"driver_capability": "ready", "applicable": false,
	} {
		if cell[k] != want {
			t.Errorf("coverage.codex.%s = %v, want %v (whole cell: %v)", k, cell[k], want, cell)
		}
	}
}
