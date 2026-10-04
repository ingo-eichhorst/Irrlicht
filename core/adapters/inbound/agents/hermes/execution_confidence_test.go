package hermes

import (
	"testing"

	"irrlicht/core/domain/session"
)

// The hermes store fold bypasses the tailer, so it folds the
// execution-confidence accumulator itself (#737).
func TestComputeMetrics_ExecutionConfidence(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 2})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", ts: 1001})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant",
		content: "I'm not sure what the ticket wants; it is ambiguous and unclear. Maybe the list view?", finish: "stop", ts: 1002})

	m, err := ComputeMetrics(path, "s1")
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics: %v, %v", m, err)
	}
	requireLowWithTooltip(t, m)
}

func requireLowWithTooltip(t *testing.T, m *session.SessionMetrics) {
	t.Helper()
	if m.ExecutionConfidence == nil {
		t.Fatal("no score, want a low score with a tooltip")
	}
	if !m.ExecutionConfidenceLow {
		t.Errorf("score %d not flagged low, want a low score with a tooltip", *m.ExecutionConfidence)
	}
	if m.ExecutionConfidenceTooltip == "" {
		t.Error("empty tooltip, want a low score with a tooltip")
	}
}
