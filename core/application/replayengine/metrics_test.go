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

func TestConvert_QuestionHeadline_AwaySummaryUpgradesLastAssistantText(t *testing.T) {
	m := &tailer.SessionMetrics{
		LastAssistantText: "a raw, unhelpful status line",
		AwaySummary:       &tailer.AwaySummary{Text: "Goal was X. Next: merge or wait?", ObservedAt: 100},
	}
	got := NewMetricsConverter(fakeCompactor{}).Convert(m)
	if got.QuestionHeadline != "question:Goal was X. Next: merge or wait?" {
		t.Errorf("QuestionHeadline = %q, want the away_summary recap to win over raw last-assistant text", got.QuestionHeadline)
	}
}

func TestConvert_QuestionHeadline_MarkerWinsOverAwaySummary(t *testing.T) {
	m := &tailer.SessionMetrics{
		LastAssistantText: "a raw, unhelpful status line",
		AwaySummary:       &tailer.AwaySummary{Text: "Goal was X. Next: merge or wait?", ObservedAt: 100},
		TaskQuestion:      &tailer.TaskQuestion{Text: "run the migration?", ObservedAt: 200},
	}
	got := NewMetricsConverter(fakeCompactor{}).Convert(m)
	if got.QuestionHeadline != "question:run the migration?" {
		t.Errorf("QuestionHeadline = %q, want the agent's own marker to win over the away_summary recap", got.QuestionHeadline)
	}
}

// TestConvert_PendingQuestionMarker pins issue #1138: only the deliberate
// irrlicht-question marker sets PendingQuestionMarker (the waiting-state
// classifier's authoritative signal). The away_summary recap and the
// LastAssistantText fallback — both of which can populate QuestionHeadline —
// must NOT set it, or nearly every finished turn would read as waiting.
func TestConvert_PendingQuestionMarker(t *testing.T) {
	t.Run("marker present → set", func(t *testing.T) {
		m := &tailer.SessionMetrics{
			LastAssistantText: "a declarative status line with no question",
			TaskQuestion:      &tailer.TaskQuestion{Text: "run the migration?", ObservedAt: 100},
		}
		if got := TailerToDomain(m); !got.PendingQuestionMarker {
			t.Error("PendingQuestionMarker = false, want true when a TaskQuestion marker is present")
		}
	})

	t.Run("only last-assistant text → not set", func(t *testing.T) {
		m := &tailer.SessionMetrics{LastAssistantText: "should I proceed?"}
		if got := TailerToDomain(m); got.PendingQuestionMarker {
			t.Error("PendingQuestionMarker = true, want false — the LastAssistantText fallback must not set it")
		}
	})

	t.Run("only away_summary → not set", func(t *testing.T) {
		m := &tailer.SessionMetrics{
			LastAssistantText: "a raw status line",
			AwaySummary:       &tailer.AwaySummary{Text: "Goal was X. Next: merge or wait?", ObservedAt: 100},
		}
		if got := TailerToDomain(m); got.PendingQuestionMarker {
			t.Error("PendingQuestionMarker = true, want false — the away_summary recap is not an authoritative question signal")
		}
	})

	t.Run("empty marker text → not set", func(t *testing.T) {
		m := &tailer.SessionMetrics{TaskQuestion: &tailer.TaskQuestion{Text: "", ObservedAt: 100}}
		if got := TailerToDomain(m); got.PendingQuestionMarker {
			t.Error("PendingQuestionMarker = true, want false for an empty marker")
		}
	})
}

// TestConvert_PendingWaitingCue pins issue #1150: the full-text waiting-cue
// verdict the adapter parser computes rides through the tailer→domain
// conversion untouched, so the classifier sees it. The parser derives it from
// the FULL assistant text; the conversion is a plain passthrough.
func TestConvert_PendingWaitingCue(t *testing.T) {
	t.Run("flag set on tailer metrics → copied to domain", func(t *testing.T) {
		m := &tailer.SessionMetrics{
			LastAssistantText: "a declarative status tail with no cue",
			PendingWaitingCue: true,
		}
		if got := TailerToDomain(m); !got.PendingWaitingCue {
			t.Error("PendingWaitingCue = false, want true — the parser's full-text verdict must pass through")
		}
	})

	t.Run("flag unset → stays false", func(t *testing.T) {
		m := &tailer.SessionMetrics{LastAssistantText: "a declarative status tail with no cue"}
		if got := TailerToDomain(m); got.PendingWaitingCue {
			t.Error("PendingWaitingCue = true, want false when the parser set no cue")
		}
	})
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
