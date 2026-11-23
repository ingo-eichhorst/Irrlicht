package services_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_IdlePromptHook_ReconcilesReadyToWaiting is the end-to-end
// #1173 lifecycle, narrowed by #2176 to a LOCK. The "idle" phase below ends on a
// question, which the agent_done rule already routes to waiting without any hook,
// so this test no longer proves the hook or its hold changes the state (it did,
// for plain-statement turns, until #2176). What it still pins: with the hold in
// place the session stays waiting across a re-evaluation, is released to working
// when the user replies, and the idle signal does not leak into the next turn.
// The defect test for the hook is
// TestSessionDetector_IdlePromptHook_KeepsFinishedStatementTurnReady.
func TestSessionDetector_IdlePromptHook_ReconcilesReadyToWaiting(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const path = "/home/.claude/projects/-Users-test/idle1.jsonl"

	// phase drives what ComputeMetrics reports across the lifecycle:
	//   "idle"    — turn finished on a question (IsAgentDone true, waiting cue)
	//   "done"    — a later turn finished on a plain statement
	//   "replied" — user sent a new message (IsAgentDone false)
	var mu sync.Mutex
	phase := "idle"
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		mu.Lock()
		defer mu.Unlock()
		if phase == "replied" {
			return &session.SessionMetrics{LastEventType: "user"}, nil
		}
		if phase == "done" {
			return &session.SessionMetrics{
				LastEventType:     "turn_done",
				LastAssistantText: "Done. All tests pass.",
			}, nil
		}
		// A turn that ends on a question: turn_done plus a trailing question,
		// so IsWaitingForUserInput is true and the hook rule applies (#2176).
		return &session.SessionMetrics{
			LastEventType:     "turn_done",
			LastAssistantText: "Which option do you want?",
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

	// Defers run LIFO, so registering the drain first makes the teardown order
	// cancel() → wait for Run to return, which is what reaps the goroutine.
	done := make(chan error, 1)
	defer func() { <-done }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	// overlay must not let the agent_done rule leak the session back to ready.
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

	// The new turn completes on a plain statement. With the idle signal correctly
	// cleared, this must route to ready — NOT re-pin waiting from a leaked signal.
	setPhase("done")
	tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: "idle1", ProjectDir: "-Users-test", TranscriptPath: path}
	waitForSessionState(repo, "idle1", session.StateReady, 5*time.Second)

	repo.mu.Lock()
	got = repo.lastSavedState["idle1"]
	repo.mu.Unlock()
	if got != session.StateReady {
		t.Errorf("next turn completed: state = %q, want ready (idle signal must not leak into the next turn)", got)
	}
}

// TestSessionDetector_IdlePromptHook_KeepsFinishedStatementTurnReady is the #2176
// defect test. A turn that ended on a plain statement is ready, and Claude Code
// fires Notification/idle_prompt about 60 s later whether or not anything is
// asked of the user. The hook must not promote that ready session to waiting:
// no transition at all is recorded after the Stop-driven ready, and the state
// is still ready after the hook's synthetic pass has run.
func TestSessionDetector_IdlePromptHook_KeepsFinishedStatementTurnReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const path = "/home/.claude/projects/-Users-test/idle2176.jsonl"

	var passes atomic.Int32
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		passes.Add(1)
		return &session.SessionMetrics{
			LastEventType:     "turn_done",
			LastAssistantText: "Done. All tests pass.",
		}, nil
	}}
	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	repo.states["idle2176"] = &session.SessionState{
		SessionID:      "idle2176",
		State:          session.StateReady,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{LastEventType: "turn_done"},
	}

	done := make(chan error, 1)
	defer func() { <-done }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond) // let seedFromDisk finish

	base := passes.Load()
	det.HandleIdlePromptHook("idle2176", path)
	// Observe the subject: wait until the hook's synthetic pass has run the
	// metrics computation, then a second activity pass behind it, so the first
	// has fully finished (the detector processes events serially).
	waitForCondition(func() bool { return passes.Load() > base }, 5*time.Second)
	tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: "idle2176", ProjectDir: "-Users-test", TranscriptPath: path}
	waitForCondition(func() bool { return passes.Load() > base+1 }, 5*time.Second)
	if passes.Load() <= base+1 {
		t.Fatalf("hook's synthetic pass and the follow-up pass did not both run (passes %d -> %d)", base, passes.Load())
	}

	repo.mu.Lock()
	got := repo.states["idle2176"].State
	repo.mu.Unlock()
	if got != session.StateReady {
		t.Fatalf("after idle_prompt hook on a finished plain-statement turn: state = %q, want ready", got)
	}
	// No transition of any kind: neither ready→waiting nor a ready→working blip
	// from the hook's synthetic activity.
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindStateTransition && ev.SessionID == "idle2176" {
			t.Errorf("unexpected state transition %s → %s (%s) after idle_prompt hook", ev.PrevState, ev.NewState, ev.Reason)
		}
	}
}
