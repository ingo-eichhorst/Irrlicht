package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_PeriodicDedup_RetiresDuplicateMintedAfterSeed is issue
// #1992's acceptance evidence.
//
// dedupeByPID (pid_manager.go) is the only reconciliation pass that matches a
// real-UUID duplicate root session sharing a live PID, and its only caller is
// SeedPIDs, whose only non-test caller is SessionDetector.seedFromDisk
// (session_detector_lifecycle.go), which itself has one non-test call site:
// session_detector.go's Run, invoked synchronously BEFORE the event loop
// begins and BEFORE the SweepDeadPIDs goroutine is even spawned. A duplicate
// that comes into existence after that point — the production shape: a
// Save racing a same-PID Delete re-creates the just-deleted row (documented
// on cleanupStalePIDHolders/removeSessionUntracked; that race is a separate,
// deliberately unfiled defect, not what this test is about) — is invisible
// to every other reaper: no transcript event ever fires for it (nothing
// appends to a superseded transcript), SweepDeadPIDs' dead-PID reap doesn't
// apply (the PID is alive), and sweepSupersededPreSessionsPeriodic only
// matches proc-* rows. It survives until the daemon restarts.
//
// Reproduced here by starting the detector's real event loop against an
// EMPTY repo first — seedFromDisk's once-only dedup runs and finds nothing,
// exactly the "if they exist at seed time, SeedPIDs eats them and the test
// proves nothing about the gap" trap this test is built to avoid — and only
// THEN minting the duplicate directly into the repo. RunPIDLivenessSweepForTest
// drives one sweep tick synchronously, the same CheckPIDLiveness call the
// real 5s SweepDeadPIDs ticker makes, without waiting on the real ticker.
//
// Confirmed RED against pre-fix code (no dedupeByPIDPeriodic exists): the
// older duplicate ("ghost-old") survives the sweep untouched, and the test
// fails at the first assertion below.
func TestSessionDetector_PeriodicDedup_RetiresDuplicateMintedAfterSeed(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)
	det.SetDeletedCooldown(0) // isolate staleness-based suppression (part 2 below) from cooldown-based suppression

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(20 * time.Millisecond) // let seedFromDisk finish against the empty repo

	pid := os.Getpid() // alive — the shared PID both duplicate rows claim
	now := time.Now()
	tmpDir := t.TempDir()

	oldTranscript := filepath.Join(tmpDir, "ghost-old.jsonl")
	writeTranscript(t, oldTranscript, now.Add(-5*time.Minute)) // frozen: nothing appends to a superseded transcript

	newTranscript := filepath.Join(tmpDir, "live-new.jsonl")
	writeTranscript(t, newTranscript, now)

	// Minted directly into the repo AFTER the event loop — and its one-time
	// seed-time dedup — is already running. Save (not a raw map write) keeps
	// this race-free against the detector's own background goroutines
	// (mockRepo.Save takes its mutex; see testhelpers_test.go).
	if err := repo.Save(&session.SessionState{
		SessionID:      "ghost-old",
		Adapter:        "claude-code",
		State:          session.StateWaiting,
		PID:            pid,
		CWD:            tmpDir,
		TranscriptPath: oldTranscript,
		FirstSeen:      now.Add(-time.Hour).Unix(),
		UpdatedAt:      now.Add(-time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("seed ghost-old: %v", err)
	}
	if err := repo.Save(&session.SessionState{
		SessionID:      "live-new",
		Adapter:        "claude-code",
		State:          session.StateWaiting,
		PID:            pid,
		CWD:            tmpDir,
		TranscriptPath: newTranscript,
		FirstSeen:      now.Unix(),
		UpdatedAt:      now.Unix(),
	}); err != nil {
		t.Fatalf("seed live-new: %v", err)
	}

	// Drive one sweep tick synchronously, exactly as SweepDeadPIDs' real
	// ticker would via CheckPIDLiveness.
	det.RunPIDLivenessSweepForTest()

	if state, _ := repo.Load("ghost-old"); state != nil {
		t.Fatal("older duplicate root session sharing a live PID with a newer sibling " +
			"survived the periodic liveness sweep — dedupeByPID only runs once, at " +
			"startup, inside SeedPIDs (issue #1992)")
	}
	if state, _ := repo.Load("live-new"); state == nil {
		t.Fatal("the newer session sharing the PID should survive the periodic dedup sweep")
	}

	// Second half: a late/stale event for the now-deleted duplicate must not
	// resurrect it. The periodic delete must route through
	// removeSessionUntracked -> onSessionDeleted -> removeFromProjectSessions
	// so recentlyDeleted's stale-transcript branch (independent of cooldown,
	// which is 0 here) keeps suppressing re-creation.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "ghost-old",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: oldTranscript, // same file, still frozen
	}

	time.Sleep(50 * time.Millisecond)

	if state, _ := repo.Load("ghost-old"); state != nil {
		t.Fatal("a late transcript event for the deleted duplicate's frozen transcript " +
			"re-created it — the periodic dedup delete must tombstone the id the same " +
			"way every other reconciliation delete does (issue #1992)")
	}
}
