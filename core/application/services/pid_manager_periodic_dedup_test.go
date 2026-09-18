package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// seedFromDiskStartedMsg is the exact LogInfo message session_detector.go's
// Run logs immediately after seedFromDisk() returns and before entering the
// event loop's select (see the "started — listening for transcript events"
// call right after the seedFromDisk() call in Run). Polling for it is a
// precise, race-free signal that the once-only seed-time dedup has already
// run — replacing a fixed sleep that could otherwise let the test's
// duplicate rows land BEFORE seedFromDisk, which would let SeedPIDs'
// dedupeByPID silently do the job dedupeByPIDPeriodic exists to do and pass
// green even with dedupeByPIDPeriodic deleted outright.
const seedFromDiskStartedMsg = "started — listening for transcript events"

// waitForLogMessage polls log's captured LogInfo messages for want until it
// appears or timeout elapses, then reports whether it was actually seen.
// waitForCondition itself returns silently on timeout, so callers MUST check
// this return value — an unchecked call would let "never observed" and
// "arrived instantly" look identical.
func waitForLogMessage(log *mockLogger, want string, timeout time.Duration) bool {
	seen := func() bool {
		for _, msg := range log.infoSnapshot() {
			if msg == want {
				return true
			}
		}
		return false
	}
	waitForCondition(seen, timeout)
	return seen()
}

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
	log := &mockLogger{} // caller-owned (not newDetector's internal one) so we can poll its messages below

	// Same construction as newDetector (testhelpers_test.go), but with our
	// own Log so seedFromDisk's completion can be observed instead of guessed
	// at with a sleep.
	det := services.NewSessionDetector([]inbound.Watcher{tw}, services.SessionDetectorDeps{
		PW:           pw,
		Repo:         repo,
		Log:          log,
		Git:          &mockGit{},
		Metrics:      &mockMetrics{},
		Broadcaster:  nil,
		Version:      "test",
		ReadyTTL:     0,
		PIDDiscovers: nil,
		ProcessNames: nil,
		LiveCWDs:     nil,
	})
	det.SetDeletedCooldown(0) // isolate staleness-based suppression (part 2 below) from cooldown-based suppression

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// Observe seedFromDisk's completion instead of guessing at it with a fixed
	// sleep: if the duplicate rows below were saved before seedFromDisk runs,
	// SeedPIDs' own once-only dedupeByPID would delete "ghost-old" itself,
	// and this test would pass even with dedupeByPIDPeriodic deleted outright
	// — exactly the trap this test exists to avoid.
	seedWaitStart := time.Now()
	if !waitForLogMessage(log, seedFromDiskStartedMsg, 2*time.Second) {
		t.Fatalf("detector never logged %q within %v — seedFromDisk may not have completed",
			seedFromDiskStartedMsg, time.Since(seedWaitStart))
	}

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

	// Sequencing barrier: the detector processes events from tw.ch in order,
	// on a single event-loop goroutine (session_detector.go's Run: one
	// `case idEv := <-d.merged` per iteration). Sending a second event for a
	// brand-new, freshly-transcripted session and waiting for ITS effect
	// (the row appearing in the repo) proves the first (ghost-old) event has
	// already been fully processed — a fixed sleep here would instead make
	// the absence assertion below vacuously true on a slow runner, passing
	// before the ghost-old event was ever handled.
	sentinelID := "sentinel-after-ghost"
	sentinelTranscript := filepath.Join(tmpDir, "sentinel-after-ghost.jsonl")
	writeTranscript(t, sentinelTranscript, time.Now()) // fresh — must be admitted as a new session
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sentinelID,
		ProjectDir:     "-Users-test-project",
		TranscriptPath: sentinelTranscript,
	}

	barrierWaitStart := time.Now()
	waitForCondition(func() bool {
		state, _ := repo.Load(sentinelID)
		return state != nil
	}, 2*time.Second)
	if state, _ := repo.Load(sentinelID); state == nil {
		t.Fatalf("sequencing-barrier session %q was never admitted within %v — cannot conclude "+
			"the earlier ghost-old event was processed", sentinelID, time.Since(barrierWaitStart))
	}

	if state, _ := repo.Load("ghost-old"); state != nil {
		t.Fatal("a late transcript event for the deleted duplicate's frozen transcript " +
			"re-created it — the periodic dedup delete must tombstone the id the same " +
			"way every other reconciliation delete does (issue #1992)")
	}
}
