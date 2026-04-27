package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// TestStartupCleanup_DeletesZombieFromPriorDaemonRun is the regression test
// for issue #242: a session persisted by a previous daemon run whose process
// has already exited must be deleted at startup, before the API serves any
// requests, instead of lingering in the UI until a slow fallback eventually
// kicks in.
//
// The scenario mirrors the user-reported failure mode: the daemon shut down
// while a `claude` process was still alive and persisted the session record,
// then the user quit `claude`, then the daemon was restarted. On restart, the
// stored PID is dead — CleanupZombies must catch this synchronously.
func TestStartupCleanup_DeletesZombieFromPriorDaemonRun(t *testing.T) {
	tmp := realTempDir(t)
	transcript := filepath.Join(tmp, "zombie.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// Spawn and reap a process so we have a PID known to be dead. Skip the
	// test if the kernel races us and recycles the PID before we observe it.
	deadCmd := exec.Command("true")
	if err := deadCmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	deadPID := deadCmd.Process.Pid
	_ = deadCmd.Wait()
	if err := syscall.Kill(deadPID, 0); err == nil {
		t.Skipf("dead PID %d was recycled before test could observe it", deadPID)
	}

	repo := newMemRepo()
	// Seed a session as if a prior daemon had persisted it.
	zombieID := "abc-123-zombie"
	if err := repo.Save(&session.SessionState{
		SessionID:      zombieID,
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            deadPID,
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Add(-2 * time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("seed zombie: %v", err)
	}
	// And a session whose process is still alive — must be left untouched.
	livingID := "def-456-living"
	if err := repo.Save(&session.SessionState{
		SessionID:      livingID,
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            os.Getpid(),
		TranscriptPath: transcript,
		UpdatedAt:      time.Now().Unix(),
	}); err != nil {
		t.Fatalf("seed living: %v", err)
	}

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil,
	)

	deleted := detector.CleanupZombies()
	if deleted != 1 {
		t.Errorf("CleanupZombies returned %d, want 1", deleted)
	}

	if s, _ := repo.Load(zombieID); s != nil {
		t.Errorf("zombie session %q persisted after startup cleanup: %+v", zombieID, s)
	}
	if s, _ := repo.Load(livingID); s == nil {
		t.Errorf("living session %q was deleted by startup cleanup", livingID)
	}
}
