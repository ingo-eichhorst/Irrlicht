package services_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_CompactHook_HoldsWorkingThenReleases is the end-to-end
// #657 lifecycle. A manual /compact writes nothing to the transcript during the
// compaction window, so the PreCompact hook (HandleCompactHook) must force the
// session to working and hold it there across re-evaluations until the
// compact_boundary lands — which surfaces as SawManualCompactBoundary and
// releases the session back to ready (the #656 half).
//
// Waits use a 5s budget, not the usual 1s: this test flaked on loaded Linux CI
// runners under -race (#794), timing out at exactly 1s despite completing in
// ~60ms locally — a tight-budget issue, not a logic bug.
func TestSessionDetector_CompactHook_HoldsWorkingThenReleases(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const path = "/home/.claude/projects/-Users-test/comp1.jsonl"

	// sawBoundary toggles whether ComputeMetrics reports the manual
	// compact_boundary for this pass. Pre-boundary the transcript still ends at
	// the pre-compact turn_done; once the boundary lands we flip it true.
	var mu sync.Mutex
	sawBoundary := false
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		mu.Lock()
		defer mu.Unlock()
		return &session.SessionMetrics{
			LastEventType:            "turn_done",
			HasOpenToolCall:          false,
			SawManualCompactBoundary: sawBoundary,
		}, nil
	}}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)

	// Session sits ready after a clean prior turn (turn_done). Without the hook
	// overlay, ClassifyState would keep it ready forever during compaction.
	repo.states["comp1"] = &session.SessionState{
		SessionID:      "comp1",
		State:          session.StateReady,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{LastEventType: "turn_done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(20 * time.Millisecond) // let seedFromDisk finish

	// PreCompact hook fires for the manual /compact → force working.
	det.HandleCompactHook("comp1", path, "manual")
	waitForSessionState(repo, "comp1", session.StateWorking, 5*time.Second)

	repo.mu.Lock()
	got := repo.lastSavedState["comp1"]
	repo.mu.Unlock()
	if got != session.StateWorking {
		t.Fatalf("after PreCompact hook: state = %q, want working (forced for compaction window)", got)
	}

	// A re-evaluation mid-window (no boundary yet) must HOLD working — the
	// stale turn_done must not leak the session back to ready.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "comp1",
		ProjectDir:     "-Users-test",
		TranscriptPath: path,
	}
	// Give the pass time to land, then assert it's still working.
	waitForCondition(func() bool { return repo.eventCountOf("comp1") >= 1 }, 5*time.Second)
	repo.mu.Lock()
	got = repo.lastSavedState["comp1"]
	repo.mu.Unlock()
	if got != session.StateWorking {
		t.Fatalf("mid-window re-eval: state = %q, want working (hold must survive re-evaluation)", got)
	}

	// Compaction finishes: the boundary lands. The next pass clears the hold
	// and releases working → ready (#656).
	mu.Lock()
	sawBoundary = true
	mu.Unlock()
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "comp1",
		ProjectDir:     "-Users-test",
		TranscriptPath: path,
	}
	waitForSessionState(repo, "comp1", session.StateReady, 5*time.Second)

	repo.mu.Lock()
	got = repo.lastSavedState["comp1"]
	repo.mu.Unlock()
	if got != session.StateReady {
		t.Errorf("after compact_boundary: state = %q, want ready (working→ready release)", got)
	}
}

// TestSessionDetector_CompactHook_AutoIgnored guards the gate at the detector
// level: an auto-compaction PreCompact hook must not force working — the
// session is already mid-turn and a forced blip would be spurious (#657).
func TestSessionDetector_CompactHook_AutoIgnored(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const path = "/home/.claude/projects/-Users-test/comp2.jsonl"

	det := newDetector(tw, pw, repo)

	repo.states["comp2"] = &session.SessionState{
		SessionID:      "comp2",
		State:          session.StateReady,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &session.SessionMetrics{LastEventType: "turn_done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(20 * time.Millisecond)

	det.HandleCompactHook("comp2", path, "auto")

	// Auto compaction must not transition the session. Give the loop a beat,
	// then confirm it never left ready.
	waitForSessionState(repo, "comp2", session.StateWorking, 200*time.Millisecond)
	repo.mu.Lock()
	got := repo.lastSavedState["comp2"]
	repo.mu.Unlock()
	if got == session.StateWorking {
		t.Errorf("auto PreCompact forced working; want session to stay ready")
	}
}
