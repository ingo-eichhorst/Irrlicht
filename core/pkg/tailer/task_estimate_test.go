package tailer

import (
	"testing"

	"irrlicht/core/pkg/capacity"
)

// taskEstimateTestParser is a minimal parser that lifts a synthetic "est"
// field off the line into ParsedEvent.TaskEstimate, so these tests exercise
// only the tailer plumbing (persist across passes, latest wins) — marker
// parsing itself is covered in the claudecode adapter package.
type taskEstimateTestParser struct{}

func (p *taskEstimateTestParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{Timestamp: ParseTimestamp(raw), EventType: "assistant_message"}
	if _, ok := raw["user"]; ok {
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		return ev
	}
	if _, ok := raw["tool_result"]; ok {
		// Claude Code shape: tool results arrive as user-role lines that
		// raise ClearToolNames but also carry ToolResultIDs.
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		ev.ToolResultIDs = []string{"tr-1"}
		return ev
	}
	if v, ok := raw["est"].(map[string]interface{}); ok {
		total, _ := v["total"].(float64)
		done, _ := v["done"].(float64)
		ev.TaskEstimate = &TaskEstimate{
			TotalRounds:     int(total),
			CompletedRounds: int(done),
			ObservedAt:      ev.Timestamp.Unix(),
		}
	}
	return ev
}

func newTaskEstimateTestTailer(path string) *TranscriptTailer {
	tl := NewTranscriptTailer(path, &taskEstimateTestParser{}, "claude-code")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

func TestTailer_TaskEstimate_SurfacedOnMetrics(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0)},
		{"timestamp": ts(1), "est": map[string]interface{}{"total": float64(10), "done": float64(2)}},
	})
	m, err := newTaskEstimateTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate on metrics")
	}
	if m.TaskEstimate.TotalRounds != 10 || m.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/10", m.TaskEstimate.CompletedRounds, m.TaskEstimate.TotalRounds)
	}
}

func TestTailer_TaskEstimate_PersistsAcrossMarkerlessPasses(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "est": map[string]interface{}{"total": float64(10), "done": float64(3)}},
	})
	tl := newTaskEstimateTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}

	// A later pass with new markerless content must not drop the estimate.
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1)})
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.CompletedRounds != 3 {
		t.Fatalf("TaskEstimate = %+v, want persisted CompletedRounds=3", m.TaskEstimate)
	}
}

func TestTailer_TaskEstimate_ResetOnUserMessage(t *testing.T) {
	// A new user message starts a new task — the previous estimate must not
	// survive into the next working episode (issue #558).
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "est": map[string]interface{}{"total": float64(10), "done": float64(9)}},
	})
	tl := newTaskEstimateTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1), "user": true})
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate != nil {
		t.Fatalf("TaskEstimate = %+v, want nil after user message", m.TaskEstimate)
	}

	// A fresh marker after the reset starts a new estimate.
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(2), "est": map[string]interface{}{"total": float64(4), "done": float64(1)}})
	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.TotalRounds != 4 {
		t.Fatalf("TaskEstimate = %+v, want fresh 1/4", m.TaskEstimate)
	}
}

func TestTailer_TaskEstimate_BaseTracking(t *testing.T) {
	// The first marker of the current task is the rate baseline; a marker
	// whose completed count goes BACKWARDS re-anchors it (the agent started
	// a new count without a user prompt), and a real user message clears
	// both (issue #558 multi-task sessions).
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "est": map[string]interface{}{"total": float64(6), "done": float64(0)}},
		{"timestamp": ts(30), "est": map[string]interface{}{"total": float64(6), "done": float64(3)}},
	})
	tl := newTaskEstimateTestTailer(path)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimateBase == nil || m.TaskEstimateBase.CompletedRounds != 0 {
		t.Fatalf("base = %+v, want the 0/6 first marker", m.TaskEstimateBase)
	}

	// completed goes backwards → new task, base re-anchors.
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(60), "est": map[string]interface{}{"total": float64(9), "done": float64(1)}})
	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimateBase == nil || m.TaskEstimateBase.TotalRounds != 9 || m.TaskEstimateBase.CompletedRounds != 1 {
		t.Fatalf("base = %+v, want re-anchored 1/9", m.TaskEstimateBase)
	}

	// Real user message clears base and estimate together.
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(90), "user": true})
	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate != nil || m.TaskEstimateBase != nil {
		t.Fatalf("estimate=%+v base=%+v, want both nil after user message", m.TaskEstimate, m.TaskEstimateBase)
	}
}

func TestTailer_TaskEstimate_SurvivesToolResults(t *testing.T) {
	// Tool results are user-role lines in Claude Code transcripts and raise
	// ClearToolNames — they must NOT reset the estimate or the chip vanishes
	// on every tool call mid-task (issue #558).
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "est": map[string]interface{}{"total": float64(10), "done": float64(4)}},
	})
	tl := newTaskEstimateTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1), "tool_result": true})
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.CompletedRounds != 4 {
		t.Fatalf("TaskEstimate = %+v, want 4/10 to survive a tool-result line", m.TaskEstimate)
	}
}

func TestTailer_TaskEstimate_LatestMarkerWins(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "est": map[string]interface{}{"total": float64(10), "done": float64(1)}},
	})
	tl := newTaskEstimateTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1), "est": map[string]interface{}{"total": float64(12), "done": float64(7)}})
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskEstimate == nil || m.TaskEstimate.CompletedRounds != 7 || m.TaskEstimate.TotalRounds != 12 {
		t.Fatalf("TaskEstimate = %+v, want latest (7/12)", m.TaskEstimate)
	}
}
