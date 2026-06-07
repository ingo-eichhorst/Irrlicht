package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_ActivityBackfillsEmptyAdapter reproduces issue #643: a
// session created via the no-identity fallback (debounce-coalesced / synthetic
// refresh during the startup race) persists Adapter="" forever, because the
// only backfill lived in onNewSession's existing-session branch and a
// continuously-active transcript never produces another transcript-NEW event.
//
// The next identity-carrying activity event must backfill Adapter — which also
// unblocks PID discovery, since TryDiscoverPID keys off the adapter name.
func TestSessionDetector_ActivityBackfillsEmptyAdapter(t *testing.T) {
	tw := newMockAgentWatcher() // identity = "claude-code"
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	cwd := t.TempDir() // #321 — daemon rejects sessions with missing cwd

	// CWD discovery is registered only for "claude-code". While Adapter=="" the
	// session can never reach it (TryDiscoverPID returns false on a nil
	// discoverFn); once backfilled, the state.PID==0 retry binds the PID.
	cwdFn := func(string, func([]int) int) (int, error) { return 64180, nil }
	det := newDetectorWithCWDDiscovery(tw, pw, repo, cwdFn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk before injecting the poisoned session.
	time.Sleep(20 * time.Millisecond)

	// Seed a session exactly as the no-identity fallback would create it:
	// Adapter="" and PID==0, continuously active (working).
	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		Version:        1,
		SessionID:      "poisoned",
		State:          session.StateWorking,
		Adapter:        "",
		PID:            0,
		TranscriptPath: "/home/.claude/projects/-Users-test/poisoned.jsonl",
		CWD:            cwd,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     1,
	})

	// Deliver an identity-carrying activity event (leading-edge of a debounce
	// window forwards the watcher's identity to processActivity).
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "poisoned",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/poisoned.jsonl",
		CWD:            cwd,
	}

	// PID discovery is the downstream signal that the backfill took effect —
	// it can only succeed once Adapter is set. Poll race-free (#606).
	waitForPID(repo, "poisoned", time.Second)

	cancel()
	<-done

	state, _ := repo.Load("poisoned")
	if state == nil {
		t.Fatal("poisoned session should still exist")
	}
	if state.Adapter != "claude-code" {
		t.Errorf("Adapter: got %q, want %q (backfill on activity path)", state.Adapter, "claude-code")
	}
	if state.PID != 64180 {
		t.Errorf("PID: got %d, want 64180 (discovery unblocked by backfill)", state.PID)
	}
}
