package tailer

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestLedger_PersistsLastAssistantText is the issue #705 regression: a session
// whose turn ended on a question is persisted as `waiting`. A daemon restart
// resumes the tailer at LastOffset; when the transcript hasn't grown the pass
// processes zero lines. Unless the ledger carries LastAssistantText, the
// recomputed metrics carry an empty question text, IsWaitingForUserInput()
// returns false, and the seed re-classification demotes the session to `ready`.
func TestLedger_PersistsLastAssistantText(t *testing.T) {
	const question = "Should I proceed with the migration?"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": question},
			},
		}},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	tl1 := newTestTailer(path)
	m, err := tl1.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastAssistantText != question {
		t.Fatalf("pre-restart LastAssistantText = %q, want question text", m.LastAssistantText)
	}

	ledger := tl1.GetLedgerState()
	if ledger.LastAssistantText != question {
		t.Fatalf("ledger LastAssistantText = %q, want question text", ledger.LastAssistantText)
	}

	// Restart: a fresh tailer rehydrated from the ledger resumes at EOF — zero
	// lines processed — and must still recover the question text so the session
	// stays classified as waiting.
	tl2 := newTestTailer(path)
	tl2.SetLedgerState(ledger)
	m, err = tl2.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastAssistantText != question {
		t.Errorf("post-restart LastAssistantText = %q, want question text (resume-at-EOF must not forget the question)", m.LastAssistantText)
	}

	// The restored text must also re-persist so a *second* restart survives.
	// This pins the private-field half of SetLedgerState: restoring only
	// t.metrics would satisfy the assertions above (the empty pass keeps
	// metrics) yet leave GetLedgerState reading an empty t.lastAssistantText.
	if got := tl2.GetLedgerState().LastAssistantText; got != question {
		t.Errorf("re-persisted LastAssistantText = %q, want %q (private field must be restored too)", got, question)
	}

	// The downstream classifier reads LastAssistantText via the domain helper:
	// without the persisted text it would report not-waiting and rule 2b would
	// demote waiting → ready on startup (issue #705).
	dm := &session.SessionMetrics{LastEventType: m.LastEventType, LastAssistantText: m.LastAssistantText}
	if !dm.IsWaitingForUserInput() {
		t.Error("IsWaitingForUserInput() = false after restart, want true (question text must survive the ledger round-trip)")
	}
}
