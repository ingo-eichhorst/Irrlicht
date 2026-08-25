package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_ParentHeldWorking_WhenChildIsRetryingAfterError is the
// defect #1798 handed forward and #1801 settles: `hasActiveChildren` asked
// "is this child working or waiting", which silently became an INCOMPLETE
// partition the moment a fourth state arrived. A child sitting in `error`
// with Phase=retrying has another attempt scheduled — it is still working —
// yet it answered "not active" and released its parent to ready. The parent
// then reads green while its subagent is red and mid-retry.
//
// The test drives ONE parent through BOTH phases in sequence, deliberately:
//
//	phase 1 (retrying) — the parent must stay working
//	phase 2 (terminal) — the same parent, same wiring, must now reach ready
//
// Phase 2 is what makes phase 1's assertion mean something. "Still working"
// is the parent's starting state, so on its own it is equally consistent with
// "held correctly" and "the activity pass never ran at all" — the exact
// absence-vs-inability-to-look failure AGENTS.md rules out. Watching the same
// parent release the moment the phase flips proves the machinery reaches it,
// and proves the PHASE is what decides rather than the state alone.
func TestSessionDetector_ParentHeldWorking_WhenChildIsRetryingAfterError(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	const parentID = "parent-err"
	const childID = "child-err"
	const parentPath = "/home/.claude/projects/-Users-test/parent-err.jsonl"
	const childPath = "/home/.claude/projects/-Users-test/parent-err/subagents/child-err.jsonl"

	saveParent := func() {
		repo.Save(&session.SessionState{
			SessionID:      parentID,
			State:          session.StateWorking,
			TranscriptPath: parentPath,
			FirstSeen:      now,
			UpdatedAt:      now,
			EventCount:     5,
			Metrics: &session.SessionMetrics{
				LastEventType:     "turn_done",
				HasOpenToolCall:   false,
				LastAssistantText: "Done.",
			},
		})
	}
	saveChild := func(phase session.ErrorPhase) {
		repo.Save(&session.SessionState{
			SessionID:       childID,
			State:           session.StateError,
			ParentSessionID: parentID,
			TranscriptPath:  childPath,
			FirstSeen:       now,
			UpdatedAt:       now,
			EventCount:      3,
			Metrics: &session.SessionMetrics{
				LastEventType: "assistant",
				SessionError: &session.SessionError{
					Phase:   phase,
					Class:   "rate_limit",
					Message: "API Error: 429 rate limited",
				},
			},
		})
	}
	pokeParent := func() {
		tw.ch <- agent.Event{
			Type:           agent.EventActivity,
			SessionID:      parentID,
			ProjectDir:     "-Users-test",
			TranscriptPath: parentPath,
		}
	}

	// runPass seeds the pair, fires one activity event at the parent, and does
	// not return until the detector has actually persisted something — never on
	// a bare sleep. The repo's own save counter is the readiness signal because
	// it is the one thing that ticks whichever way the verdict goes: a released
	// parent is saved as ready, a held one is still saved (UpdatedAt/EventCount
	// advance on every activity pass). Polling for a STATE instead would be the
	// AGENTS.md trap in both directions — "still working" is also the parent's
	// starting value, and after the first phase a stale "ready" would satisfy
	// the poll before the second phase's pass had begun.
	runPass := func(phase session.ErrorPhase) *session.SessionState {
		t.Helper()
		saveParent()
		saveChild(phase)
		repo.mu.Lock()
		baseline := repo.saves
		repo.mu.Unlock()

		pokeParent()

		deadline := time.Now().Add(3 * time.Second)
		for {
			repo.mu.Lock()
			saved := repo.saves
			repo.mu.Unlock()
			if saved > baseline {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("detector persisted nothing within 3s of the %q activity event (saves stuck at %d) — the pass under test never ran, so any state assertion below would be meaningless", phase, baseline)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// The save that unblocked the poll can be the child's or an
		// intermediate one; give the parent's own verdict a bounded window to
		// land, then read it.
		waitForSessionState(repo, parentID, session.StateReady, 300*time.Millisecond)
		got, _ := repo.Load(parentID)
		if got == nil {
			t.Fatalf("parent session vanished after the %q pass", phase)
		}
		return got
	}

	// --- phase 1: the child is retrying → the parent must NOT be released ---
	if got := runPass(session.ErrorPhaseRetrying); got.State != session.StateWorking {
		t.Errorf("parent state with a RETRYING errored child: got %q, want %q — another attempt is scheduled, so the child is still working and must hold its parent",
			got.State, session.StateWorking)
	}

	// --- phase 2: the same child goes terminal → the parent IS released ---
	// Nothing else changes. If phase 1 above passed because the activity pass
	// never reached this parent, this half cannot pass either.
	if got := runPass(session.ErrorPhaseTerminal); got.State != session.StateReady {
		t.Errorf("parent state with a TERMINAL errored child: got %q, want %q — no further attempt is coming, so the parent is free to finish (and this half is what proves the pass runs at all)",
			got.State, session.StateReady)
	}
}
