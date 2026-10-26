package services

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/domain/agent"
)

// Lock (#2107) for the exemption agent.Process.SharedPIDOwner's doc states.
// Declaring a SharedPIDOwner makes the detector ask PIDManager's
// StaleTranscriptWriter about a stale transcript at admission (#2081), which
// is why the doc requires a writer-probe PIDForSession — and opencode's is a
// cwd match. That admission never runs for a DB-backed key: isStaleTranscript
// is false for a `?session=` path, so admitStaleTranscript admits on its first
// line and the adapter's PID discovery is never asked. The WAL behind the key
// is aged past orphanTranscriptAge, and the control shows that age makes the
// bare WAL path stale: what keeps the key fresh is its shape, not its mtime.
func TestAdmitStaleTranscript_DBBackedKeyNeverAsksTheWriterProbe(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "opencode.db-wal")
	if err := os.WriteFile(wal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * orphanTranscriptAge)
	if err := os.Chtimes(wal, old, old); err != nil {
		t.Fatal(err)
	}
	if !isStaleTranscript(wal) {
		t.Fatalf("control: the bare WAL path aged %v is not stale, so the check below proves nothing", 2*orphanTranscriptAge)
	}
	key := wal + "?session=ses_x"
	if isStaleTranscript(key) {
		t.Fatalf("isStaleTranscript(%q) = true for a DB-backed key", key)
	}

	var asked atomic.Int32
	d := NewSessionDetector(nil, SessionDetectorDeps{
		Repo: &diagFakeRepo{}, Log: bornLog{}, Git: bornGit{},
		PIDDiscovers: map[string]agent.PIDDiscoverFunc{
			opencode.AdapterName: func(string, string, func([]int) int) (int, error) {
				asked.Add(1)
				return 4242, nil
			},
		},
		SharedPIDOwners: agents.SharedPIDOwners([]agent.Agent{opencode.Agent()}),
	})
	ev := agent.Event{Type: agent.EventNewSession, SessionID: "ses_x", TranscriptPath: key, CWD: t.TempDir()}

	if !d.admitStaleTranscript(agent.Identity{Name: opencode.AdapterName}, &ev) {
		t.Fatal("admitStaleTranscript declined a DB-backed key")
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("admitting a DB-backed key asked opencode's PID discovery %d time(s), want 0", n)
	}
}
