package replayengine

import (
	"testing"

	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// fakeCompactor tags its output with the requested kind so tests can assert the
// converter routes each source to the right CompactKind.
type fakeCompactor struct{}

func (fakeCompactor) Compact(text string, kind outbound.CompactKind) string {
	switch kind {
	case outbound.CompactIntent:
		return "intent:" + text
	case outbound.CompactQuestion:
		return "question:" + text
	default:
		return text
	}
}

func TestTailerToDomain_TaskSummary_MarkerWins(t *testing.T) {
	m := &tailer.SessionMetrics{
		TaskSummary:   &tailer.TaskSummary{Text: "add the logout button", ObservedAt: 100},
		FirstUserText: "please help me with the navbar",
	}
	got := TailerToDomain(m)
	if got.TaskSummary != "add the logout button" {
		t.Errorf("TaskSummary = %q, want the marker to win over the heuristic", got.TaskSummary)
	}
}

func TestTailerToDomain_TaskSummary_HeuristicFallback(t *testing.T) {
	m := &tailer.SessionMetrics{
		FirstUserText: "please help me with the navbar",
	}
	got := TailerToDomain(m)
	if got.TaskSummary != "please help me with the navbar" {
		t.Errorf("TaskSummary = %q, want the heuristic fallback when no marker", got.TaskSummary)
	}
}

func TestTailerToDomain_TaskSummary_EmptyWhenNeither(t *testing.T) {
	got := TailerToDomain(&tailer.SessionMetrics{})
	if got.TaskSummary != "" {
		t.Errorf("TaskSummary = %q, want empty", got.TaskSummary)
	}
}

func TestConvert_IntentHeadline_CompactsTaskSummary(t *testing.T) {
	m := &tailer.SessionMetrics{
		TaskSummary: &tailer.TaskSummary{Text: "add the logout button", ObservedAt: 100},
	}
	got := NewMetricsConverter(fakeCompactor{}).Convert(m)
	if got.IntentHeadline != "intent:add the logout button" {
		t.Errorf("IntentHeadline = %q, want it compacted from the summary", got.IntentHeadline)
	}
	// The full summary is preserved for the tooltip.
	if got.TaskSummary != "add the logout button" {
		t.Errorf("TaskSummary = %q, want the full text preserved", got.TaskSummary)
	}
}

func TestConvert_QuestionHeadline_MarkerWinsOverLastAssistantText(t *testing.T) {
	m := &tailer.SessionMetrics{
		LastAssistantText: "a long rambling message ending in a question?",
		TaskQuestion:      &tailer.TaskQuestion{Text: "run the migration?", ObservedAt: 100},
	}
	got := NewMetricsConverter(fakeCompactor{}).Convert(m)
	if got.QuestionHeadline != "question:run the migration?" {
		t.Errorf("QuestionHeadline = %q, want the marker to win", got.QuestionHeadline)
	}
	// The full last-assistant text is preserved for the tooltip + classifier.
	if got.LastAssistantText != "a long rambling message ending in a question?" {
		t.Errorf("LastAssistantText = %q, want the full text preserved (not overwritten)", got.LastAssistantText)
	}
}

func TestConvert_QuestionHeadline_FallsBackToLastAssistantText(t *testing.T) {
	m := &tailer.SessionMetrics{
		LastAssistantText: "should I proceed?",
	}
	got := NewMetricsConverter(fakeCompactor{}).Convert(m)
	if got.QuestionHeadline != "question:should I proceed?" {
		t.Errorf("QuestionHeadline = %q, want fallback to last-assistant text", got.QuestionHeadline)
	}
}

func TestTailerToDomain_NilCompactor_HeadlinesAreIdentity(t *testing.T) {
	m := &tailer.SessionMetrics{
		TaskSummary:       &tailer.TaskSummary{Text: "add the logout button", ObservedAt: 100},
		LastAssistantText: "should I proceed?",
	}
	got := TailerToDomain(m)
	// The free wrapper uses identity compaction — headlines carry the full text,
	// and LastAssistantText is no longer overwritten with the question snippet.
	if got.IntentHeadline != "add the logout button" {
		t.Errorf("IntentHeadline = %q, want identity (full text)", got.IntentHeadline)
	}
	if got.QuestionHeadline != "should I proceed?" {
		t.Errorf("QuestionHeadline = %q, want identity (full text)", got.QuestionHeadline)
	}
	if got.LastAssistantText != "should I proceed?" {
		t.Errorf("LastAssistantText = %q, want full text preserved", got.LastAssistantText)
	}
}
