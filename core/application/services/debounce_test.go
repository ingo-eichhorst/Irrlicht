package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

func TestDebounce_FirstEventFiresImmediately(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk to complete before injecting state.
	time.Sleep(20 * time.Millisecond)

	// Pre-populate a session so processActivity has something to update.
	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "deb1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/deb1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     1,
	})

	savesBefore := repo.saves

	// Send a single activity event.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "deb1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/deb1.jsonl",
	}

	// The leading-edge fires immediately — check within 50ms (well before the
	// 2-second debounce window).
	time.Sleep(50 * time.Millisecond)

	repo.mu.Lock()
	savesAfter := repo.saves
	repo.mu.Unlock()

	if savesAfter <= savesBefore {
		t.Errorf("expected at least one save within 50ms (leading-edge), got saves before=%d after=%d", savesBefore, savesAfter)
	}

	cancel()
	<-done
}

func TestDebounce_CoalescesRapidEvents(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk.
	time.Sleep(20 * time.Millisecond)

	// Send a new session event to create the session in the repo.
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "deb2",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/deb2.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	// Record save count after the new session event is processed.
	repo.mu.Lock()
	savesAfterNew := repo.saves
	repo.mu.Unlock()

	// Fire 5 rapid activity events within 100ms.
	for i := 0; i < 5; i++ {
		tw.ch <- agent.Event{
			Type:           agent.EventActivity,
			SessionID:      "deb2",
			ProjectDir:     "-Users-test",
			TranscriptPath: "/home/.claude/projects/-Users-test/deb2.jsonl",
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for the debounce window to expire plus buffer.
	// The timer is reset on each coalesced event, so we need to wait
	// 2 seconds from the last event plus some buffer.
	time.Sleep(2200 * time.Millisecond)

	repo.mu.Lock()
	totalSaves := repo.saves
	repo.mu.Unlock()

	activitySaves := totalSaves - savesAfterNew

	// With debouncing we expect:
	// - 1 save from the leading-edge (first activity event fires immediately)
	// - 1 save from the coalesced trailing event (timer fires after window)
	// Total: ~2 saves for activity, definitely much less than 5.
	if activitySaves >= 5 {
		t.Errorf("debounce should coalesce rapid events: got %d activity saves, want <5 (expected ~2)", activitySaves)
	}
	if activitySaves < 1 {
		t.Errorf("expected at least 1 activity save, got %d", activitySaves)
	}

	cancel()
	<-done
}

func TestDebounce_RemovedCleansUpTimer(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk.
	time.Sleep(20 * time.Millisecond)

	// Create the session.
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "deb3",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/deb3.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	// Send an activity event to start the debounce timer.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "deb3",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/deb3.jsonl",
	}

	time.Sleep(20 * time.Millisecond)

	// Send a removed event while the debounce timer is still pending.
	// This should cancel the timer and clean up without panicking.
	tw.ch <- agent.Event{
		Type:      agent.EventRemoved,
		SessionID: "deb3",
	}

	// Wait for the debounce window to pass — if cleanup didn't work, a
	// stale timer could fire and cause a panic or unexpected behavior.
	time.Sleep(2200 * time.Millisecond)

	// Verify the session transitioned to ready (removed handler behavior).
	state, _ := repo.Load("deb3")
	if state == nil {
		t.Fatal("session should still exist after removal (has transcript path)")
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (removed event transitions to ready)", state.State)
	}

	cancel()
	<-done
}
