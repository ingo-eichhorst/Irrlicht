package services_test

import (
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
