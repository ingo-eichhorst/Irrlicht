package services_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_IdlePromptHook_ReconcilesReadyToWaiting is the end-to-end
// #1173 lifecycle. A turn that ended on a plain statement (no question/cue) sits
// ready — waiting_cue can't reach it. When Claude Code's Notification/idle_prompt
// hook fires (HandleIdlePromptHook), the detector must correct the session
// ready→waiting in a single transition (no working blip), hold it there, then
// release it back to working the moment the user replies — without leaking the
// idle signal into the next turn.
func TestSessionDetector_IdlePromptHook_ReconcilesReadyToWaiting(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const path = "/home/.claude/projects/-Users-test/idle1.jsonl"

	// phase drives what ComputeMetrics reports across the lifecycle:
	//   "idle"    — turn finished on a plain statement (IsAgentDone true)
	//   "replied" — user sent a new message (IsAgentDone false)
	var mu sync.Mutex
	phase := "idle"
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		mu.Lock()
		defer mu.Unlock()
		if phase == "replied" {
			return &session.SessionMetrics{LastEventType: "user"}, nil
		}
		// A plain completion: turn_done, no trailing question or cue, so
		// IsWaitingForUserInput is false and rule 2 alone would say ready.
		return &session.SessionMetrics{
			LastEventType:     "turn_done",
			LastAssistantText: "Done. All tests pass.",
		}, nil
	}}
	setPhase := func(p string) { mu.Lock(); phase = p; mu.Unlock() }

	det := newDetectorWithMetrics(tw, pw, repo, metrics)

	repo.states["idle1"] = &session.SessionState{
		SessionID:      "idle1",
		State:          session.StateReady,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{LastEventType: "turn_done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	defer func() { cancel(); <-done }()
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond) // let seedFromDisk finish

	// idle_prompt hook fires → correct ready → waiting.
	det.HandleIdlePromptHook("idle1", path)
	waitForSessionState(repo, "idle1", session.StateWaiting, 5*time.Second)

	repo.mu.Lock()
	got := repo.lastSavedState["idle1"]
	repo.mu.Unlock()
	if got != session.StateWaiting {
		t.Fatalf("after idle_prompt hook: state = %q, want waiting", got)
	}

	// A re-evaluation while still idle must HOLD waiting — the persistent
	// overlay must not let rule 2 leak the session back to ready.
	tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: "idle1", ProjectDir: "-Users-test", TranscriptPath: path}
	waitForCondition(func() bool { return repo.eventCountOf("idle1") >= 1 }, 5*time.Second)
	repo.mu.Lock()
	got = repo.lastSavedState["idle1"]
	repo.mu.Unlock()
	if got != session.StateWaiting {
		t.Fatalf("idle re-eval: state = %q, want waiting (overlay must survive re-evaluation)", got)
	}

	// User replies: new activity, IsAgentDone false → release waiting → working
	// and drop the idle signal.
	setPhase("replied")
	tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: "idle1", ProjectDir: "-Users-test", TranscriptPath: path}
	waitForSessionState(repo, "idle1", session.StateWorking, 5*time.Second)

	// The new turn completes (turn_done again). With the idle signal correctly
	// cleared, this must route to ready — NOT re-pin waiting from a leaked signal.
	setPhase("idle")
	tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: "idle1", ProjectDir: "-Users-test", TranscriptPath: path}
	waitForSessionState(repo, "idle1", session.StateReady, 5*time.Second)

	repo.mu.Lock()
	got = repo.lastSavedState["idle1"]
	repo.mu.Unlock()
	if got != session.StateReady {
		t.Errorf("next turn completed: state = %q, want ready (idle signal must not leak into the next turn)", got)
	}
}
