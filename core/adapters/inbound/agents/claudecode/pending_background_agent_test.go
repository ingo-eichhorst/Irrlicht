package claudecode

import (
	"testing"
)

// The #1037 guard holds a parent `working` while Claude Code reports
// background agents still pending. These tests drive the real parser through
// the real tailer, because the defect they lock is the seam between the two:
// the parser decides what an omitted pendingBackgroundAgentCount means, and
// the tailer's sticky counter can only fall to 0 if the parser ever hands it
// an explicit 0. A stub parser would prove nothing here. See issue #1076.

// TestTailer_PendingBackgroundAgentCount_DrainsToZeroOnFieldlessTurnDone
// replays the drain observed live in session b52ce0f9 (CC 2.1.210),
// compressed: the count ticks down while agents finish, and when the last one
// is done Claude Code omits the field rather than writing 0. Before #1076 the
// counter stuck at the last non-zero value forever, so the parent could never
// be released back to `ready`.
func TestTailer_PendingBackgroundAgentCount_DrainsToZeroOnFieldlessTurnDone(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "assistant", "timestamp": ts(0)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(1), "pendingBackgroundAgentCount": float64(2)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2), "pendingBackgroundAgentCount": float64(1)},
		// Last agent finished: Claude Code omits the field rather than writing 0.
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(3)},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.PendingBackgroundAgentCount != 0 {
		t.Errorf("PendingBackgroundAgentCount = %d, want 0 — the counter must be able to reach zero, or the #1037 hold never releases",
			m.PendingBackgroundAgentCount)
	}
}

// TestTailer_PendingBackgroundAgentCount_UnchangedByFieldlessStopHookSummary
// is the scoping lock's tailer-level counterpart to
// TestParser_SystemEvent_StopHookSummary_PendingCountAbsentStaysNil: a
// stop_hook_summary carries no count, so it must leave a live one alone.
// Zeroing it here would silently release the hold while agents are still
// running — #1036 verbatim.
func TestTailer_PendingBackgroundAgentCount_UnchangedByFieldlessStopHookSummary(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(0), "pendingBackgroundAgentCount": float64(2)},
		{"type": "system", "subtype": "stop_hook_summary", "timestamp": ts(1)},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.PendingBackgroundAgentCount != 2 {
		t.Errorf("PendingBackgroundAgentCount = %d, want 2 — stop_hook_summary carries no count and must not zero it",
			m.PendingBackgroundAgentCount)
	}
}

// TestLedger_PersistsPendingBackgroundAgentCount is the Hole 2 regression
// (issue #1076), mirroring TestLedger_PersistsLastEventType (#649): a daemon
// restart rehydrates the tailer at LastOffset, so the next pass processes zero
// lines and can only report what the ledger carried. Without the count in
// LedgerState the guard goes inert on every restart and the parent is free to
// flip `ready` mid-run.
//
// It lives in the claudecode package rather than next to its tailer-package
// sibling because seeding a non-zero count end-to-end needs the real parser —
// the tailer package's stub parser does not read the field.
func TestLedger_PersistsPendingBackgroundAgentCount(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "assistant", "timestamp": ts(0)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(1), "pendingBackgroundAgentCount": float64(3)},
	})

	tl1 := newCCTailer(path)
	m, err := tl1.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.PendingBackgroundAgentCount != 3 {
		t.Fatalf("pre-restart PendingBackgroundAgentCount = %d, want 3", m.PendingBackgroundAgentCount)
	}

	ledger := tl1.GetLedgerState()
	if ledger.PendingBackgroundAgentCount != 3 {
		t.Fatalf("ledger PendingBackgroundAgentCount = %d, want 3", ledger.PendingBackgroundAgentCount)
	}

	// Restart: a fresh tailer rehydrated from the ledger resumes at EOF —
	// zero lines processed — and must still report three agents pending, or
	// the parent flips ready mid-run (#1036).
	tl2 := newCCTailer(path)
	tl2.SetLedgerState(ledger)
	m, err = tl2.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.PendingBackgroundAgentCount != 3 {
		t.Errorf("post-restart PendingBackgroundAgentCount = %d, want 3 (resume-at-EOF must not forget the hold)",
			m.PendingBackgroundAgentCount)
	}
}
