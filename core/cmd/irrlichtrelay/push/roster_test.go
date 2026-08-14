package push

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
