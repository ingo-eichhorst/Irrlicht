package opencode

import (
	"testing"

	"irrlicht/core/domain/session"
)

// The opencode store fold bypasses the tailer, so it folds the
// execution-confidence accumulator itself (#737). A session whose assistant
// parts hedge heavily must surface a low score through ComputeMetrics.
func TestComputeMetrics_ExecutionConfidence(t *testing.T) {
	db, dbPath := openTestOpencodeDB(t)
	const sid = "ses_test_confidence"
	insertTestSession(t, db, sid, "/tmp/opencode-confidence-test")
	msgData := `{"role":"assistant","time":{"created":1000},"model":{"providerID":"test","modelID":"test-model"}}`
	insertTestMessage(t, db, testMessageRow{id: "msg_1", sid: sid, ts: 1000, data: msgData})
	insertTestPart(t, db, testPartRow{id: "part_1", msgID: "msg_1", sid: sid, ts: 1100,
		data: textPart(t, "I'm not sure what the ticket wants; it is ambiguous and unclear. Maybe the list view?")})

	m, err := ComputeMetrics(dbPath, sid)
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
