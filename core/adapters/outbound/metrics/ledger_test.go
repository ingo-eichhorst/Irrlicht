package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// TestLoadLedger_RejectsOldSchema pins the #649 migration: a ledger written
// by an older daemon (schema 3, no last_event_type) must be discarded on
// load, forcing a full transcript re-scan under the current parser. The
// re-scan is what heals sessions stranded in `working` by pre-#642 parsers.
func TestLoadLedger_RejectsOldSchema(t *testing.T) {
	dir := t.TempDir()
	lp := filepath.Join(dir, "old.ledger.json")
	v3 := []byte(`{"schema_version":3,"last_offset":3115960,"background_procs":{"bbw7rzpa0":"/tmp/x/tasks/bbw7rzpa0.output"}}`)
	if err := os.WriteFile(lp, v3, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := loadLedger(lp); s != nil {
		t.Errorf("loadLedger accepted a schema-3 ledger: %+v (must discard → full re-scan)", s)
	}
}

func TestLoadLedger_AcceptsCurrentSchema(t *testing.T) {
	dir := t.TempDir()
	lp := filepath.Join(dir, "current.ledger.json")
	data, err := json.Marshal(tailer.LedgerState{
		SchemaVersion: tailer.LedgerSchemaVersion,
		LastOffset:    42,
		LastEventType: "turn_done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadLedger(lp)
	if s == nil {
		t.Fatal("loadLedger rejected a current-schema ledger")
	}
	if s.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", s.LastEventType)
	}
}

// TestPurgeDeadBackgroundProcs_ClearsTailerAndLedger covers the adapter half
// of the #649 dead-verdict cleanup: the tailer's open set is dropped and the
// ledger is rewritten immediately, so the phantom processes cannot resurrect
// on a restart that happens before the next TailAndProcess pass.
func TestPurgeDeadBackgroundProcs_ClearsTailerAndLedger(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := filepath.Join(tmpHome, ".local", "share", "irrlicht", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(tmpHome, "transcript.jsonl")

	tl := tailer.NewTranscriptTailer(transcript, nil, "claude-code")
	tl.SetLedgerState(tailer.LedgerState{
		SchemaVersion:   tailer.LedgerSchemaVersion,
		BackgroundProcs: map[string]string{"bbw7rzpa0": "/tmp/x/tasks/bbw7rzpa0.output"},
	})

	a := New(Registry{})
	lp := ledgerPath(transcript)
	a.tailers[transcript] = &lockedTailer{t: tl, lp: lp}

	a.PurgeDeadBackgroundProcs(transcript, []string{"/tmp/x/tasks/bbw7rzpa0.output"})

	if got := tl.GetLedgerState().BackgroundProcs; len(got) != 0 {
		t.Errorf("tailer BackgroundProcs after purge = %v, want empty", got)
	}
	saved := loadLedger(lp)
	if saved == nil {
		t.Fatal("expected the purge to write the ledger immediately")
	}
	if len(saved.BackgroundProcs) != 0 {
		t.Errorf("persisted BackgroundProcs after purge = %v, want empty", saved.BackgroundProcs)
	}
}

func TestPurgeDeadBackgroundProcs_NoTailerNoop(t *testing.T) {
	a := New(Registry{})
	outputs := []string{"/tmp/x/tasks/x.output"}
	a.PurgeDeadBackgroundProcs("/never/seen.jsonl", outputs) // must not panic
	a.PurgeDeadBackgroundProcs("", outputs)                  // must not panic
	a.PurgeDeadBackgroundProcs("/never/seen.jsonl", nil)     // must not panic
}
