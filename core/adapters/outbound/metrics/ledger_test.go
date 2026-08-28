package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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

// preSessionErrorLedgerVersion is the schema a v0.6.0 daemon stamps: the last
// version whose parser had NO session-error concept at all (`git show
// v0.6.0:core/pkg/tailer/parser.go | grep -c SessionError` → 0). Written as a
// LITERAL on purpose. It is the fixed historical fact the test below is about,
// and spelling it symbolically would make the test follow the bump it exists to
// pin — reverting LedgerSchemaVersion to 5 would leave it green.
const preSessionErrorLedgerVersion = 5

// TestLoadLedger_RejectsPreSessionErrorSchema pins the #1815 bump to 6: a ledger
// stamped by a daemon that could not derive session errors must be DISCARDED, so
// the session gets a full transcript re-scan under the current parser.
//
// Why the re-scan is the whole point, and why nothing else supplies it: the
// tailer's sticky error is a pure function of the transcript lines
// (applySessionError is its only non-ledger writer), so a session that failed
// under v0.6.0 has its failure sitting in bytes ALREADY PAST LastOffset. Accept
// that ledger as current and the post-restart pass reads zero new lines, nothing
// re-derives the failure, and the session stays `ready` forever under a daemon
// that would have called it `error`.
//
// This is a guard, so it has no pre-fix red of its own — the mutation it was
// verified against is `LedgerSchemaVersion = 5`, i.e. reverting the bump, which
// makes the payload below current and turns this test red.
func TestLoadLedger_RejectsPreSessionErrorSchema(t *testing.T) {
	// Fail loudly rather than vacuously if the bump is ever reverted: with the
	// two equal, the payload below is a CURRENT ledger and "discarded" would be
	// asserting the opposite of what this test means.
	if tailer.LedgerSchemaVersion <= preSessionErrorLedgerVersion {
		t.Fatalf("LedgerSchemaVersion = %d, must exceed the pre-session-error schema %d — "+
			"the #1815 bump was reverted, so a v0.6.0 ledger is now accepted as current and "+
			"every session that failed under it stays green after upgrading",
			tailer.LedgerSchemaVersion, preSessionErrorLedgerVersion)
	}

	dir := t.TempDir()
	lp := filepath.Join(dir, "pre-session-error.ledger.json")
	// A real v0.6.0 ledger shape: current-looking, mid-session, and carrying no
	// session_error key because that daemon had no field to write one into.
	v5 := []byte(`{"schema_version":` + strconv.Itoa(preSessionErrorLedgerVersion) +
		`,"last_offset":3115960,"last_event_type":"assistant","cum_provider_cost_usd":1.25}`)
	if err := os.WriteFile(lp, v5, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := loadLedger(lp); s != nil {
		t.Errorf("loadLedger accepted a schema-%d ledger: %+v — a session that errored under "+
			"v0.6.0 will resume at LastOffset, read zero new lines, and never go red",
			preSessionErrorLedgerVersion, s)
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
