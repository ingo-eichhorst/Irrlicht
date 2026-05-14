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
// /api/scenarios returns both with the correct flags.
func TestScenariosList_walksReplaydataTree(t *testing.T) {
	root := t.TempDir()
	mkRecording(t, root, "claudecode", "scenarios", "baseline-hello",
		map[string]string{"signals.jsonl": "", "ground_truth.jsonl": "{}"})
	mkRecording(t, root, "aider", "regression", "llm-error",
		map[string]string{"transcript.md": ""}) // no signals, no gt
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
	if entries[1].Agent != "claudecode" || !entries[1].HasGroundTruth {
		t.Errorf("entries[1]=%+v", entries[1])
	}
}

func TestScenarioDetail_returnsMetaAndGroundTruth(t *testing.T) {
	root := t.TempDir()
	gt := `{"schema_version":1,"agent":"x","scenario":"y","recording_started_at":"2026-05-14T12:00:00Z"}
{"ts_offset_ms":0,"marker":"a","expected_state":"ready"}
`
	mkRecording(t, root, "x", "scenarios", "y", map[string]string{
		"recording-meta.json": `{"agent":"x","scenario":"y"}`,
		"ground_truth.jsonl":  gt,
		"signals.jsonl":       `{"ts":"2026-05-14T12:00:00Z","sensor":"transcript","kind":"line","payload":{"line":"hi"}}` + "\n",
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
	if d.GroundTruth == nil || len(d.GroundTruth.Labels) != 1 {
		t.Errorf("ground truth missing or wrong: %+v", d.GroundTruth)
	}
	if len(d.Signals) != 1 || len(d.Transitions) != 1 {
		t.Errorf("signals/transitions wrong: %d / %d", len(d.Signals), len(d.Transitions))
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
		"claudecode", "scenarios", "multi-turn-conversation", "events.jsonl")
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
	// duration should be ~23.8s = 23814ms.
	if dur, _ := doc["duration_ms"].(float64); dur < 20000 || dur > 30000 {
		t.Errorf("duration unexpected: %v ms", dur)
	}
	if kinds, _ := doc["kinds"].(map[string]any); kinds["state_transition"] == nil {
		t.Errorf("kinds breakdown missing state_transition: %+v", kinds)
	}
	if sc, _ := doc["session_count"].(map[string]any); sc["presession"] == nil || sc["real"] == nil {
		t.Errorf("session_count missing presession/real breakdown: %+v", sc)
	}
}

func mkRecording(t *testing.T, root, agent, subtree, id string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "replaydata", "agents", agent, subtree, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
