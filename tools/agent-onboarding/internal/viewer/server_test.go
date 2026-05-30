package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestScenariosList_walksReplaydataTree spins up a temp replaydata tree
// with one agent in `scenarios/` and one in `regression/`, and asserts
// /api/scenarios returns both in sorted order.
func TestScenariosList_walksReplaydataTree(t *testing.T) {
	root := t.TempDir()
	mkRecording(t, root, "claudecode", "scenarios", "basic-turn",
		map[string]string{"events.jsonl": ""})
	mkRecording(t, root, "aider", "regression", "llm-error",
		map[string]string{"transcript.md": ""})
	s := &Server{RepoRoot: root}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/scenarios", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	var entries []ScenarioListEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	// Sorted by agent then subtree.
	if entries[0].Agent != "aider" || entries[0].Subtree != "regression" {
		t.Errorf("entries[0]=%+v", entries[0])
	}
	if entries[1].Agent != "claudecode" || entries[1].ID != "basic-turn" {
		t.Errorf("entries[1]=%+v", entries[1])
	}
}

func TestScenarioDetail_returnsMetaAndTransitions(t *testing.T) {
	root := t.TempDir()
	mkRecording(t, root, "x", "scenarios", "y", map[string]string{
		"recording-meta.json": `{"agent":"x","scenario":"y"}`,
		"events.jsonl":        `{"kind":"state_transition","ts":"2026-05-14T12:00:00Z","new_state":"ready"}` + "\n",
	})
	s := &Server{RepoRoot: root}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/scenarios/x/scenarios/y", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	var d ScenarioDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Transitions) != 1 {
		t.Errorf("expected 1 transition, got %d", len(d.Transitions))
	}
}

func TestScenarioDetail_rejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "replaydata", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{RepoRoot: root}
	// Each of these would, without validation, escape into /etc or ../
	for _, target := range []string{
		"/api/scenarios/..%2Fetc/scenarios/passwd",
		"/api/scenarios/../etc/scenarios/passwd",
		"/api/scenarios/ok/scenarios/..%2F..",
		"/api/scenarios/AGENT/scenarios/id", // uppercase rejected
		"/api/scenarios/a$gent/scenarios/id",
	} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", target, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("target %q was accepted (code=%d) — path traversal not blocked", target, rr.Code)
		}
	}
}

func TestScenarioDetail_404OnMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "replaydata", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{RepoRoot: root}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/scenarios/no/scenarios/where", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestSynthesizeMetaFromEvents_seedScenario(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "replaydata", "agents",
		"claudecode", "regression", "multi-turn-conversation", "events.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("seed corpus not present: %v", err)
	}
	b := synthesizeMetaFromEvents(path)
	if b == nil {
		t.Fatal("synthesizeMetaFromEvents returned nil for a recording that exists")
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["synthesized"] != true {
		t.Errorf("synthesized flag missing: %+v", doc)
	}
	if doc["adapter"] != "claude-code" {
		t.Errorf("adapter wrong: %v", doc["adapter"])
	}
	if total, _ := doc["total_events"].(float64); total < 20 {
		t.Errorf("expected lots of events, got %v", doc["total_events"])
	}
	// duration depends on whether multi-turn-conversation was recorded
	// with inter-turn pauses (post-#268 the scenario sleeps 3s between
	// each wait_turn + 4s trailing, pushing total to ~34s; older
	// recordings without those sleeps were ~24s). Either is fine — just
	// sanity-check we got something plausible.
	if dur, _ := doc["duration_ms"].(float64); dur < 20000 || dur > 60000 {
		t.Errorf("duration unexpected: %v ms", dur)
	}
	if kinds, _ := doc["kinds"].(map[string]any); kinds["state_transition"] == nil {
		t.Errorf("kinds breakdown missing state_transition: %+v", kinds)
	}
	if sc, _ := doc["session_count"].(map[string]any); sc["presession"] == nil || sc["real"] == nil {
		t.Errorf("session_count missing presession/real breakdown: %+v", sc)
	}
}

// mkRecording writes a cell fixture. Cell-level files (expected.jsonl,
// assessment.json, metadata.json, recording-meta.json) land at the cell root;
// every recording artifact (events.jsonl, transcript.*, manifest.json, golden)
// lands under recordings/<one>/ — the on-disk layout where there is no "latest"
// at the root.
func mkRecording(t *testing.T, root, agent, subtree, id string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "replaydata", "agents", agent, subtree, id)
	recDir := filepath.Join(dir, "recordings", "2026-01-01-00-00-00_irrlichd-test")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cellLevel := map[string]bool{
		"expected.jsonl": true, "assessment.json": true,
		"metadata.json": true, "recording-meta.json": true,
	}
	for name, content := range files {
		target := recDir
		if cellLevel[name] {
			target = dir
		}
		if err := os.WriteFile(filepath.Join(target, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
