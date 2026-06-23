package tailer

import (
	"testing"

	"irrlicht/core/pkg/capacity"
)

// taskSummaryTestParser lifts a synthetic "summary" string off the line into
// ParsedEvent.TaskSummary so these tests exercise only the tailer plumbing
// (persist across passes, latest wins, user-message reset) — marker parsing
// itself is covered in tasksummary_scan_test.go.
type taskSummaryTestParser struct{}

func (p *taskSummaryTestParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{Timestamp: ParseTimestamp(raw), EventType: "assistant_message"}
	if u, ok := raw["user"]; ok {
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		if s, ok := u.(string); ok {
			ev.UserText = s
		}
		return ev
	}
	if _, ok := raw["tool_result"]; ok {
		// Tool results arrive as user-role lines that raise ClearToolNames but
		// also carry ToolResultIDs — must NOT reset the summary.
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		ev.ToolResultIDs = []string{"tr-1"}
		return ev
	}
	if s, ok := raw["summary"].(string); ok {
		ev.TaskSummary = &TaskSummary{Text: s, ObservedAt: ev.Timestamp.Unix()}
	}
	return ev
}

func newTaskSummaryTestTailer(path string) *TranscriptTailer {
	tl := NewTranscriptTailer(path, &taskSummaryTestParser{}, "claude-code")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

func TestTailer_TaskSummary_SurfacedOnMetrics(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0)},
		{"timestamp": ts(1), "summary": "refactor the auth flow"},
	})
	m, err := newTaskSummaryTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskSummary == nil || m.TaskSummary.Text != "refactor the auth flow" {
		t.Fatalf("TaskSummary = %+v, want 'refactor the auth flow'", m.TaskSummary)
	}
}

func TestTailer_TaskSummary_PersistsAcrossMarkerlessPasses(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "summary": "build the widget"},
	})
	tl := newTaskSummaryTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1)}) // assistant line, no summary marker
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskSummary == nil || m.TaskSummary.Text != "build the widget" {
		t.Fatalf("TaskSummary = %+v, want last-seen to persist", m.TaskSummary)
	}
}

func TestTailer_TaskSummary_ResetOnRealUserMessage(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "summary": "build the widget"},
	})
	tl := newTaskSummaryTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1), "user": "new prompt"})
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskSummary != nil {
		t.Fatalf("TaskSummary = %+v, want nil after a real user message", m.TaskSummary)
	}
}

func TestTailer_TaskSummary_NotResetByToolResultUserLine(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "summary": "build the widget"},
	})
	tl := newTaskSummaryTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1), "tool_result": true}) // user-role line w/ ToolResultIDs
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskSummary == nil || m.TaskSummary.Text != "build the widget" {
		t.Fatalf("TaskSummary = %+v, want summary kept across a tool-result line", m.TaskSummary)
	}
}

func TestTailer_IngestTaskSummary_LatestWinsStaleIgnored(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{{"timestamp": ts(0)}})
	tl := newTaskSummaryTestTailer(path)
	tl.IngestTaskSummary(&TaskSummary{Text: "first", ObservedAt: 1000})
	tl.IngestTaskSummary(&TaskSummary{Text: "second", ObservedAt: 1060})
	tl.IngestTaskSummary(&TaskSummary{Text: "stale", ObservedAt: 900}) // older — dropped
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskSummary == nil || m.TaskSummary.Text != "second" {
		t.Fatalf("TaskSummary = %+v, want latest 'second'", m.TaskSummary)
	}
}

func TestTailer_FirstUserText_CapturedOnceAndSurfaced(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "user": "fix the login redirect loop"},
	})
	tl := newTaskSummaryTestTailer(path)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.FirstUserText != "fix the login redirect loop" {
		t.Fatalf("FirstUserText = %q", m.FirstUserText)
	}
	// A later user message must NOT overwrite it — it describes what the
	// session was originally about.
	appendTranscriptLine(t, path, map[string]interface{}{"timestamp": ts(1), "user": "now do something else"})
	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.FirstUserText != "fix the login redirect loop" {
		t.Fatalf("FirstUserText = %q, want unchanged (set-once)", m.FirstUserText)
	}
}

func TestTailer_FirstUserText_SurvivesLedgerRoundTrip(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "user": "investigate the memory leak"},
	})
	tl := newTaskSummaryTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	restored := newTaskSummaryTestTailer(path)
	restored.SetLedgerState(tl.GetLedgerState())
	m, err := restored.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.FirstUserText != "investigate the memory leak" {
		t.Fatalf("FirstUserText = %q, want restored after restart", m.FirstUserText)
	}
}

func TestTailer_TaskSummary_SurvivesLedgerRoundTrip(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"timestamp": ts(0), "summary": "ship the release"},
	})
	tl := newTaskSummaryTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	restored := newTaskSummaryTestTailer(path)
	restored.SetLedgerState(tl.GetLedgerState())
	m, err := restored.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.TaskSummary == nil || m.TaskSummary.Text != "ship the release" {
		t.Fatalf("TaskSummary = %+v, want restored after restart", m.TaskSummary)
	}
}
