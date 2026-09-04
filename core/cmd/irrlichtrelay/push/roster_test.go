package push

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRosterCorruptFileRefusesToLoad pins the fail-loud direction: a
// corrupt roster is a returned error naming the file, never a silent empty
// roster — the watchdog forgetting every offline daemon must be a human
// decision, not a parse failure.
func TestRosterCorruptFileRefusesToLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, rosterFilename), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewService(dir, newTestClock().now)
	if err == nil {
		t.Fatal("NewService loaded a corrupt roster without error")
	}
	if !strings.Contains(err.Error(), rosterFilename) {
		t.Fatalf("error %q does not name the roster file", err)
	}
}

// TestRosterUpsertIgnoresEmptyDaemonID: an empty id is untracked and
// undedupable — the same refusal the hub applies to a daemon hello.
func TestRosterUpsertIgnoresEmptyDaemonID(t *testing.T) {
	svc, clk, _ := newTestService(t)
	svc.RosterUpsert("acme", "", "laptop", clk.now().Unix())
	if got := svc.Roster(); len(got) != 0 {
		t.Fatalf("roster = %+v, want empty", got)
	}
}

// writeRosterFile plants a roster on disk, the way a previous relay run
// would have left it.
func writeRosterFile(t *testing.T, dir string, entries ...RosterEntry) {
	t.Helper()
	data, err := json.MarshalIndent(rosterFile{Version: 1, Daemons: entries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rosterFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRosterForgetsDaemonsUnseenForTooLong: the roster only ever grew.
// Every entry it holds is replayed into the watchdog at startup as
// up-then-down (§6.4 seeding), so a machine retired a year ago produces a
// fresh "disconnected" banner on every relay restart, forever, and the file
// it produces it from never shrinks.
func TestRosterForgetsDaemonsUnseenForTooLong(t *testing.T) {
	dir := t.TempDir()
	clk := newTestClock()
	writeRosterFile(t, dir,
		RosterEntry{Workspace: "acme", DaemonID: "retired", Label: "old-imac", LastSeen: clk.now().Add(-400 * 24 * time.Hour).Unix()},
		RosterEntry{Workspace: "acme", DaemonID: "mac-1", Label: "laptop", LastSeen: clk.now().Add(-time.Hour).Unix()},
	)

	svc, err := NewService(dir, clk.now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	got := svc.Roster()
	if len(got) != 1 || got[0].DaemonID != "mac-1" {
		t.Fatalf("roster = %+v, want only the daemon seen an hour ago", got)
	}

	// The file follows, or the next start replays the retired machine again.
	data, err := os.ReadFile(filepath.Join(dir, rosterFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "retired") {
		t.Fatalf("the retired daemon is still on disk: %s", data)
	}
}

// TestRosterAgeBoundIsExactlyRosterMaxAge pins the cut itself, so the bound
// is a decision rather than whatever the arithmetic happened to produce.
func TestRosterAgeBoundIsExactlyRosterMaxAge(t *testing.T) {
	dir := t.TempDir()
	clk := newTestClock()
	writeRosterFile(t, dir,
		RosterEntry{Workspace: "acme", DaemonID: "one-second-inside", Label: "laptop", LastSeen: clk.now().Add(-rosterMaxAge + time.Second).Unix()},
		RosterEntry{Workspace: "acme", DaemonID: "exactly-at-the-bound", Label: "mini", LastSeen: clk.now().Add(-rosterMaxAge).Unix()},
	)

	svc, err := NewService(dir, clk.now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	got := svc.Roster()
	if len(got) != 1 || got[0].DaemonID != "one-second-inside" {
		t.Fatalf("roster = %+v, want only the daemon one second inside %s", got, rosterMaxAge)
	}
}
