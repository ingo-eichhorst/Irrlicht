package services_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// writeTranscript creates a transcript file at path with the given mtime.
func writeTranscript(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// newPIDManagerForTest builds a PIDManager wired to the shared mockRepo and
// mockLogger from testhelpers_test.go. readyTTL is set large so the normal
// idle sweep doesn't interfere with the fast-delete path under test.
func newPIDManagerForTest(repo *mockRepo) *services.PIDManager {
	return services.NewPIDManager(
		nil, // no ProcessWatcher
		repo,
		&mockLogger{},
		nil, // no broadcaster
		10*time.Minute,
		nil, // no pid discovers
		nil, // no process names
		nil, // no live-CWDs lookup
		func(string) {},
	)
}

// newPIDManagerForTestWithLiveCWDs builds a PIDManager whose startup zombie
// sweep can use the DB-backed-orphan branch — the sweep needs both an
// adapter→process-name map and a live-CWDs lookup.
func newPIDManagerForTestWithLiveCWDs(repo *mockRepo, processNames map[string]string, liveCWDs services.LiveCWDsFunc) *services.PIDManager {
	return services.NewPIDManager(
		nil,
		repo,
		&mockLogger{},
		nil,
		10*time.Minute,
		nil,
		processNames,
		liveCWDs,
		func(string) {},
	)
}

// TestCheckPIDLiveness_FreshTranscript_NotDeleted verifies the Layer 2 fix for
// issue #109: a ready session with PID=0 and a freshly-written transcript must
// NOT be fast-deleted after 30s. PID discovery may still be catching up (e.g.
// Claude hasn't written ~/.claude/sessions/<pid>.json yet).
func TestCheckPIDLiveness_FreshTranscript_NotDeleted(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "fresh.jsonl")
	writeTranscript(t, transcript, time.Now())

	repo := newMockRepo()
	// Updated 60s ago → past the 30s threshold, but transcript is fresh.
	repo.states["fresh"] = &session.SessionState{
		SessionID:      "fresh",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["fresh"] == nil {
		t.Fatal("session was deleted but transcript is fresh — fast-delete guard failed")
	}
}

// TestCheckPIDLiveness_StaleTranscript_Deleted verifies the existing behavior
// still works: a ready session with PID=0 AND a stale transcript (>2m) is
// still fast-deleted after 30s.
func TestCheckPIDLiveness_StaleTranscript_Deleted(t *testing.T) {
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "stale.jsonl")
	writeTranscript(t, transcript, time.Now().Add(-10*time.Minute))

	repo := newMockRepo()
	repo.states["stale"] = &session.SessionState{
		SessionID:      "stale",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-60 * time.Second).Unix(),
	}

	newPIDManagerForTest(repo).CheckPIDLiveness()

	if repo.states["stale"] != nil {
		t.Fatal("session should be deleted (stale transcript + ready + pid=0 + >30s)")
	}
}

// deadPIDForTest spawns and reaps a short-lived process, returning its PID.
// Skips the test if the kernel races us and recycles the PID before we can
// confirm it is dead — keeps the test deterministic.
func deadPIDForTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	if err := syscall.Kill(pid, 0); err == nil {
		t.Skipf("dead PID %d was recycled before test could observe it", pid)
	}
	return pid
}

// TestSeedAlivePIDs_DeletesWhenCWDMissing is the seed-time half of issue
// #321. On daemon startup, a previously-tracked session whose worktree has
// been deleted must be cleaned up even when its transcript was touched
// within the staleness window (e.g. by `claude --resume` from elsewhere).
// A missing cwd directory is the unambiguous orphan signal.
func TestSeedAlivePIDs_DeletesWhenCWDMissing(t *testing.T) {
	tmp := t.TempDir()
	freshTranscript := filepath.Join(tmp, "zombie.jsonl")
	writeTranscript(t, freshTranscript, time.Now())

	// CWD that does not exist (worktree was deleted).
	missingCWD := filepath.Join(tmp, "deleted-worktree")

	repo := newMockRepo()
	repo.states["zombie"] = &session.SessionState{
		SessionID:      "zombie",
		Adapter:        "claude-code",
		State:          session.StateReady,
		PID:            0,
		CWD:            missingCWD,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}

	// Child session with the same missing cwd — should be cleaned up too,
	// via deleteWithChildren, when the parent is removed.
	repo.states["zombie-child"] = &session.SessionState{
		SessionID:       "zombie-child",
		ParentSessionID: "zombie",
		Adapter:         "claude-code",
		State:           session.StateReady,
		PID:             0,
		CWD:             missingCWD,
		TranscriptPath:  freshTranscript,
		UpdatedAt:       time.Now().Unix(),
	}

	states := []*session.SessionState{repo.states["zombie"], repo.states["zombie-child"]}
	newPIDManagerForTest(repo).SeedPIDs(states)

	if repo.states["zombie"] != nil {
		t.Error("zombie session with missing cwd should have been deleted at seed time")
	}
	if repo.states["zombie-child"] != nil {
		t.Error("child of zombie session should have been deleted via deleteWithChildren")
	}
}

// TestCleanupZombies covers the two predicates plus the happy-path
// exemptions: dead PID is deleted; live PID is kept (regardless of how old
// the record is — see the comment on isStartupZombie about the deliberate
// absence of recycled-PID detection); PID=0 + stale transcript + no parent
// is deleted; PID=0 + fresh transcript is kept; PID=0 child is kept.
func TestCleanupZombies(t *testing.T) {
	tmp := t.TempDir()
	freshTranscript := filepath.Join(tmp, "fresh.jsonl")
	staleTranscript := filepath.Join(tmp, "stale.jsonl")
	writeTranscript(t, freshTranscript, time.Now())
	writeTranscript(t, staleTranscript, time.Now().Add(-10*time.Minute))

	deadPID := deadPIDForTest(t)
	livePID := os.Getpid() // the test process itself is reliably alive

	repo := newMockRepo()
	// 1. Known PID, dead → deleted.
	repo.states["dead-pid"] = &session.SessionState{
		SessionID:      "dead-pid",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            deadPID,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}
	// 2. Known PID, alive, recent → kept.
	repo.states["alive-fresh"] = &session.SessionState{
		SessionID:      "alive-fresh",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            livePID,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}
	// 3. Known PID, alive, idle for a long time + stale transcript → KEPT.
	// Documents the explicit non-goal: we never delete a session whose
	// process is alive, because reliably distinguishing a recycled PID from
	// a long-idle agent needs an adapter-specific process-name check that
	// this sweep doesn't do.
	repo.states["alive-idle"] = &session.SessionState{
		SessionID:      "alive-idle",
		Adapter:        "claude-code",
		State:          session.StateWaiting,
		PID:            livePID,
		TranscriptPath: staleTranscript,
		UpdatedAt:      time.Now().Add(-10 * time.Minute).Unix(),
	}
	// 4. PID=0, stale transcript, no parent → deleted (orphan).
	repo.states["orphan"] = &session.SessionState{
		SessionID:      "orphan",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            0,
		TranscriptPath: staleTranscript,
		UpdatedAt:      time.Now().Add(-10 * time.Minute).Unix(),
	}
	// 5. PID=0, fresh transcript → kept (PID discovery may still resolve).
	repo.states["pid0-fresh"] = &session.SessionState{
		SessionID:      "pid0-fresh",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            0,
		TranscriptPath: freshTranscript,
		UpdatedAt:      time.Now().Unix(),
	}
	// 6. PID=0 child session → kept (subagent runs inside parent's process).
	repo.states["child"] = &session.SessionState{
		SessionID:       "child",
		ParentSessionID: "alive-fresh",
		Adapter:         "claude-code",
		State:           session.StateWorking,
		PID:             0,
		TranscriptPath:  staleTranscript,
		UpdatedAt:       time.Now().Add(-10 * time.Minute).Unix(),
	}

	deleted := newPIDManagerForTest(repo).CleanupZombies()
	if deleted != 2 {
		t.Errorf("CleanupZombies returned %d, want 2 (dead-pid, orphan)", deleted)
	}

	wantDeleted := []string{"dead-pid", "orphan"}
	for _, id := range wantDeleted {
		if repo.states[id] != nil {
			t.Errorf("session %q should have been deleted but is still present", id)
		}
	}
	wantKept := []string{"alive-fresh", "alive-idle", "pid0-fresh", "child"}
	for _, id := range wantKept {
		if repo.states[id] == nil {
			t.Errorf("session %q should have been kept but is gone", id)
		}
	}
}

// TestCleanupZombies_DBBackedOrphan covers the carryover-state path for
// DB-backed adapters (OpenCode): a PID=0 session whose TranscriptPath
// contains "?session=" is deleted iff the adapter's process name has no
// live process owning the session's CWD. This is the cleanup half of the
// v0.3.12 ghost-sessions fix — the watcher gates new emissions, but
// existing on-disk state from the buggy daemon needs this branch to clear
// without a manual wipe.
func TestCleanupZombies_DBBackedOrphan(t *testing.T) {
	wal := "/Users/test/.local/share/opencode/opencode.db-wal"
	repo := newMockRepo()

	// 1. DB-backed session whose CWD is NOT held by any live process → deleted.
	repo.states["opencode-orphan"] = &session.SessionState{
		SessionID:      "opencode-orphan",
		Adapter:        "opencode",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/orphan-project",
		TranscriptPath: wal + "?session=ses_orphan",
		UpdatedAt:      time.Now().Unix(),
	}
	// 2. DB-backed session whose CWD IS held by a live process → kept.
	repo.states["opencode-live"] = &session.SessionState{
		SessionID:      "opencode-live",
		Adapter:        "opencode",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/active-project",
		TranscriptPath: wal + "?session=ses_live",
		UpdatedAt:      time.Now().Unix(),
	}
	// 3. DB-backed session whose adapter has no registered process name →
	//    kept (lookup is inconclusive; safer not to delete).
	repo.states["unknown-adapter"] = &session.SessionState{
		SessionID:      "unknown-adapter",
		Adapter:        "future-db-adapter",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/somewhere",
		TranscriptPath: wal + "?session=ses_unknown",
		UpdatedAt:      time.Now().Unix(),
	}
	// 4. DB-backed session whose lookup fails (lookup returns error) →
	//    kept (we don't delete on uncertain liveness signals).
	repo.states["opencode-lookup-error"] = &session.SessionState{
		SessionID:      "opencode-lookup-error",
		Adapter:        "opencode-flaky",
		State:          session.StateWorking,
		PID:            0,
		CWD:            "/home/user/whatever",
		TranscriptPath: wal + "?session=ses_err",
		UpdatedAt:      time.Now().Unix(),
	}

	processNames := map[string]string{
		"opencode":       "opencode",
		"opencode-flaky": "opencode-flaky", // mapped, but lookup will fail
	}
	calls := make(map[string]int)
	liveCWDs := func(name string) (map[string]struct{}, error) {
		calls[name]++
		switch name {
		case "opencode":
			return map[string]struct{}{
				"/home/user/active-project": {},
			}, nil
		case "opencode-flaky":
			return nil, errors.New("pgrep failed")
		}
		return nil, nil
	}

	deleted := newPIDManagerForTestWithLiveCWDs(repo, processNames, liveCWDs).CleanupZombies()
	if deleted != 1 {
		t.Errorf("CleanupZombies returned %d, want 1 (only opencode-orphan)", deleted)
	}
	if repo.states["opencode-orphan"] != nil {
		t.Error("opencode-orphan should have been deleted (no live process for its CWD)")
	}
	for _, id := range []string{"opencode-live", "unknown-adapter", "opencode-lookup-error"} {
		if repo.states[id] == nil {
			t.Errorf("session %q should have been kept", id)
		}
	}

	// Cache assertion: each registered adapter is looked up at most once,
	// even though "opencode" has two ghost candidates and "opencode-flaky"
	// errors out. Without the per-sweep cache this would be 3 calls.
	if calls["opencode"] != 1 {
		t.Errorf("liveCWDs(opencode) call count = %d, want 1 (cached)", calls["opencode"])
	}
	if calls["opencode-flaky"] != 1 {
		t.Errorf("liveCWDs(opencode-flaky) call count = %d, want 1 (cached error)", calls["opencode-flaky"])
	}
}

// TestHandlePIDAssigned_LauncherCaptureIsIdempotent verifies the reader runs
// exactly once when a session first gets a launcher, and is never invoked
// again even if a different PID later arrives.
func TestHandlePIDAssigned_LauncherCaptureIsIdempotent(t *testing.T) {
	repo := newMockRepo()
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		State:     session.StateWorking,
		UpdatedAt: time.Now().Unix(),
	}

	pm := newPIDManagerForTest(repo)
	var calls int
	pm.SetLauncherEnvReader(func(pid int) *session.Launcher {
		calls++
		return &session.Launcher{TermProgram: "iTerm.app"}
	})

	pm.HandlePIDAssigned(42, "s")
	if calls != 1 {
		t.Fatalf("first assign: reader calls = %d, want 1", calls)
	}
	if repo.states["s"].Launcher == nil {
		t.Fatal("first assign: launcher not captured")
	}

	// Same PID, same session: HandlePIDAssigned early-returns at the
	// `state.PID == pid` guard before reaching captureLauncher.
	pm.HandlePIDAssigned(42, "s")
	if calls != 1 {
		t.Errorf("repeat same PID: reader re-invoked (%d calls)", calls)
	}

	// Different PID on a session that already has a launcher: state.PID
	// changes, but captureLauncher's `state.Launcher != nil` guard prevents
	// a re-read — preserving the original launcher identity even across
	// process restarts / /clear scenarios.
	pm.HandlePIDAssigned(99, "s")
	if calls != 1 {
		t.Errorf("new PID with existing launcher: reader re-invoked (%d calls)", calls)
	}
	if repo.states["s"].Launcher == nil || repo.states["s"].Launcher.TermProgram != "iTerm.app" {
		t.Errorf("launcher clobbered by later PID assignment: %+v", repo.states["s"].Launcher)
	}
}
