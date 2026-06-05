package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// newClaudeCodeAdapter returns a metrics Adapter wired with the real
// claudecode parser, with HOME pointed at a temp dir so ledger writes stay
// off the developer's real state.
func newClaudeCodeAdapter(t *testing.T) *Adapter {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return New(Registry{
		Parsers: map[string]agents.ParserFactory{
			"claude-code": func() tailer.TranscriptParser { return &claudecode.Parser{} },
		},
		FallbackName: "claude-code",
	})
}

func writeJSONL(t *testing.T, events []map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	return path
}

func ccToolUse(at time.Time, id, name string, input map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":      "assistant",
		"timestamp": at.Format(time.RFC3339),
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": input},
			},
		},
	}
}

func ccText(at time.Time, text string) map[string]interface{} {
	return map[string]interface{}{
		"type":      "assistant",
		"timestamp": at.Format(time.RFC3339),
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
		},
	}
}

func TestComputeMetrics_TasksFallbackEstimate(t *testing.T) {
	// No surviving marker (the claude ≥2.1.162 text-drop, #604): the
	// estimate derives from task completion stamps — 2 of 3 tasks done 60s
	// apart → 2/3 rounds, source "tasks", projected eta.
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	path := writeJSONL(t, []map[string]interface{}{
		ccToolUse(start, "tu_1", "TaskCreate", map[string]interface{}{"subject": "one"}),
		ccToolUse(start, "tu_2", "TaskCreate", map[string]interface{}{"subject": "two"}),
		ccToolUse(start, "tu_3", "TaskCreate", map[string]interface{}{"subject": "three"}),
		ccToolUse(start.Add(60*time.Second), "tu_4", "TaskUpdate", map[string]interface{}{"taskId": "1", "status": "completed"}),
		ccToolUse(start.Add(120*time.Second), "tu_5", "TaskUpdate", map[string]interface{}{"taskId": "2", "status": "completed"}),
	})
	m, err := newClaudeCodeAdapter(t).ComputeMetrics(path, "claude-code")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: m=%v err=%v", m, err)
	}
	if m.TaskEstimate == nil {
		t.Fatal("expected tasks-derived TaskEstimate, got nil")
	}
	if m.TaskEstimate.Source != "tasks" {
		t.Errorf("Source = %q, want \"tasks\"", m.TaskEstimate.Source)
	}
	if m.TaskEstimate.TotalRounds != 3 || m.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/3", m.TaskEstimate.CompletedRounds, m.TaskEstimate.TotalRounds)
	}
	if m.TaskEstimate.UpdatedAt != start.Add(120*time.Second).Unix() {
		t.Errorf("UpdatedAt = %d, want latest completion stamp", m.TaskEstimate.UpdatedAt)
	}
	if m.TaskCompletionEta == nil {
		t.Fatal("expected projected TaskCompletionEta, got nil")
	}
	// Delta rate: 60s per task, 1 remaining → eta = latest completion + 60s.
	if want := start.Add(180 * time.Second).Unix(); *m.TaskCompletionEta != want {
		t.Errorf("TaskCompletionEta = %d, want %d", *m.TaskCompletionEta, want)
	}
}

func TestComputeMetrics_MarkerWinsOverTasks(t *testing.T) {
	// A FRESH in-band marker is the agent's holistic estimate and holds off
	// the task-list derivation even when a task completed after it (#622:
	// grace rule — the marker dominates within TaskEstimateGraceAge).
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	path := writeJSONL(t, []map[string]interface{}{
		ccToolUse(start, "tu_1", "TaskCreate", map[string]interface{}{"subject": "one"}),
		ccText(start.Add(9*time.Minute), `Progress. <!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":4} -->`),
		ccToolUse(start.Add(9*time.Minute+30*time.Second), "tu_2", "TaskUpdate", map[string]interface{}{"taskId": "1", "status": "completed"}),
	})
	m, err := newClaudeCodeAdapter(t).ComputeMetrics(path, "claude-code")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: m=%v err=%v", m, err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.Source != "marker" {
		t.Fatalf("TaskEstimate = %+v, want marker-sourced", m.TaskEstimate)
	}
	if m.TaskEstimate.TotalRounds != 10 || m.TaskEstimate.CompletedRounds != 4 {
		t.Errorf("rounds = %d/%d, want 4/10 (marker, not 1/1 tasks)", m.TaskEstimate.CompletedRounds, m.TaskEstimate.TotalRounds)
	}
}

func TestComputeMetrics_StaleMarkerYieldsToFresherTasks(t *testing.T) {
	// The orchestration failure mode (#622): the agent emitted one early
	// marker, went heads-down, and the task list kept moving. Once the
	// marker is older than TaskEstimateGraceAge, the strictly newer
	// tasks-derived estimate takes over.
	start := time.Now().Add(-20 * time.Minute).Truncate(time.Second)
	path := writeJSONL(t, []map[string]interface{}{
		ccText(start, `Plan. <!-- {"marker":"irrlicht-eta","total_rounds":8,"completed_rounds":2} -->`),
		ccToolUse(start, "tu_1", "TaskCreate", map[string]interface{}{"subject": "one"}),
		ccToolUse(start, "tu_2", "TaskCreate", map[string]interface{}{"subject": "two"}),
		ccToolUse(start, "tu_3", "TaskCreate", map[string]interface{}{"subject": "three"}),
		ccToolUse(start.Add(10*time.Minute), "tu_4", "TaskUpdate", map[string]interface{}{"taskId": "1", "status": "completed"}),
		ccToolUse(start.Add(15*time.Minute), "tu_5", "TaskUpdate", map[string]interface{}{"taskId": "2", "status": "completed"}),
	})
	m, err := newClaudeCodeAdapter(t).ComputeMetrics(path, "claude-code")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: m=%v err=%v", m, err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.Source != "tasks" {
		t.Fatalf("TaskEstimate = %+v, want tasks takeover from stale marker", m.TaskEstimate)
	}
	if m.TaskEstimate.TotalRounds != 3 || m.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/3", m.TaskEstimate.CompletedRounds, m.TaskEstimate.TotalRounds)
	}
	if m.TaskCompletionEta == nil {
		t.Error("expected projected eta from the tasks-derived estimate")
	}
}

func TestIngestTaskEstimate_SurfacesOnNextComputeMetrics(t *testing.T) {
	// A hook-delivered marker (#604) lands in the session's tailer and
	// shows up — marker-sourced — on the next ComputeMetrics pass.
	start := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	path := writeJSONL(t, []map[string]interface{}{
		ccText(start, "Working without persisted markers."),
		// A second event so the session has nonzero elapsed time — the
		// single-marker forecast path rates against session elapsed.
		ccText(start.Add(60*time.Second), "Still working."),
	})
	a := newClaudeCodeAdapter(t)

	// Unknown path: no tailer yet → silent no-op (matches IngestRateLimit).
	a.IngestTaskEstimate("/nonexistent.jsonl", &session.TaskEstimate{TotalRounds: 5, CompletedRounds: 1})

	if _, err := a.ComputeMetrics(path, "claude-code"); err != nil {
		t.Fatal(err)
	}
	a.IngestTaskEstimate(path, &session.TaskEstimate{
		TotalRounds: 5, CompletedRounds: 2, UpdatedAt: start.Add(30 * time.Second).Unix(),
	})
	m, err := a.ComputeMetrics(path, "claude-code")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: m=%v err=%v", m, err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.CompletedRounds != 2 || m.TaskEstimate.Source != "marker" {
		t.Fatalf("TaskEstimate = %+v, want hook-ingested 2/5 source=marker", m.TaskEstimate)
	}
	if m.TaskCompletionEta == nil {
		t.Error("expected projected eta from the ingested estimate")
	}
}

func TestComputeMetrics_NoMarkerNoTasksNoEstimate(t *testing.T) {
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	path := writeJSONL(t, []map[string]interface{}{
		ccText(start, "Just working, no tasks, no marker."),
	})
	m, err := newClaudeCodeAdapter(t).ComputeMetrics(path, "claude-code")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: m=%v err=%v", m, err)
	}
	if m.TaskEstimate != nil || m.TaskCompletionEta != nil {
		t.Errorf("estimate = %+v eta = %v, want nil/nil", m.TaskEstimate, m.TaskCompletionEta)
	}
}
