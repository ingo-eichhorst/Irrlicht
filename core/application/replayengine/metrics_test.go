package replayengine

import (
	"testing"

	"irrlicht/core/pkg/tailer"
)

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
