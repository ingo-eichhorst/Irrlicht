package tailer

import (
	"testing"
)

// TestLedger_PersistsLastEventType is the issue #649 regression: a daemon
// restart resumes the tailer from the persisted ledger at LastOffset. When
// the transcript hasn't grown, the pass processes zero lines — so unless the
// ledger carries LastEventType, the recomputed metrics can never satisfy
// IsAgentDone and a session persisted as `working` is stranded there forever.
func TestLedger_PersistsLastEventType(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	tl1 := newTestTailer(path)
	m, err := tl1.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Fatalf("pre-restart LastEventType = %q, want turn_done", m.LastEventType)
	}

	ledger := tl1.GetLedgerState()
	if ledger.LastEventType != "turn_done" {
		t.Fatalf("ledger LastEventType = %q, want turn_done", ledger.LastEventType)
	}
	if ledger.SchemaVersion != LedgerSchemaVersion {
		t.Fatalf("ledger SchemaVersion = %d, want %d", ledger.SchemaVersion, LedgerSchemaVersion)
	}

	// Restart: a fresh tailer rehydrated from the ledger resumes at EOF —
	// zero lines processed — and must still report the pre-restart event type.
	tl2 := newTestTailer(path)
	tl2.SetLedgerState(ledger)
	m, err = tl2.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("post-restart LastEventType = %q, want turn_done (resume-at-EOF must not forget the classification anchor)", m.LastEventType)
	}
}

// TestTailer_PurgeBackgroundProcs verifies the dead-verdict cleanup path
// (issue #649): purging drops the open set and the next ledger snapshot no
// longer persists the entries, so they cannot resurrect on a later restart.
func TestTailer_PurgeBackgroundProcs(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
	})

	tl := newTestTailer(path)
	tl.SetLedgerState(LedgerState{
		SchemaVersion:   LedgerSchemaVersion,
		BackgroundProcs: map[string]string{"bbw7rzpa0": "/tmp/x/tasks/bbw7rzpa0.output"},
	})
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatal(err)
	}
	if got := tl.GetLedgerState().BackgroundProcs; len(got) != 1 {
		t.Fatalf("precondition: BackgroundProcs = %v, want the rehydrated entry", got)
	}

	tl.PurgeBackgroundProcs()

	if got := tl.GetLedgerState().BackgroundProcs; len(got) != 0 {
		t.Errorf("BackgroundProcs after purge = %v, want empty", got)
	}
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.BackgroundProcessCount != 0 {
		t.Errorf("BackgroundProcessCount after purge = %d, want 0", m.BackgroundProcessCount)
	}
}
