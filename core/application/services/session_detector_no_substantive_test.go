package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_Activity_NoSubstantiveActivity_HoldsState is the
// regression test for issue #329. When the tailer flags a pass as
// NoSubstantiveActivity=true (every parsed line was Skip=true and
// produced no state-relevant change), the detector must NOT re-run the
// state machine — the stale LastEventType from the prior turn would
// otherwise bounce a ready session back to working via the force-bounce
// at session_detector_activity.go:316.
//
// The pass still counts as activity for UI purposes: LastEvent,
// EventCount, and UpdatedAt are bumped, and the session is rebroadcast.
func TestSessionDetector_Activity_NoSubstantiveActivity_HoldsState(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	rec := &mockRecorder{}
	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	// Session in ready with a fully-formed prior turn — these metrics would
	// satisfy the force-bounce predicate (LastEventType != "") if the
	// short-circuit didn't apply.
	//
	// PID is set non-zero: #329's real scenario is a Claude Code session (which
	// is PID-bound) emitting a post-turn `system/away_summary` recap, so it keeps
	// the UI-freshness activity bump on a non-substantive pass. A PID==0
	// transcript-first session (Antigravity IDE) intentionally suppresses that
	// bump so it can age out — see #735 and
	// TestSessionDetector_Activity_PID0_NonSubstantiveGrowth_DoesNotBumpUpdatedAt.
	beforeUpdate := time.Now().Add(-10 * time.Second).Unix()
	repo.states["away1"] = &session.SessionState{
		SessionID:      "away1",
		State:          session.StateReady,
		PID:            4242,
		TranscriptPath: "/home/.claude/projects/-Users-test/away1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      beforeUpdate,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:         "turn_done",
			HasOpenToolCall:       false,
			NoSubstantiveActivity: true,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Let seedFromDisk complete before injecting the activity event.
	time.Sleep(20 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "away1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/away1.jsonl",
	}

	// Poll for the activity pass to land (EventCount bumped 5→6) instead of a
	// fixed sleep — the session intentionally stays ready so waitForSessionState
	// can't observe a transition; EventCount is the race-free completion signal
	// (issue #606).
	waitForCondition(func() bool { return repo.eventCountOf("away1") >= 6 }, time.Second)
	cancel()
	<-done

	state, _ := repo.Load("away1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (NoSubstantiveActivity must not trigger force-bounce)", state.State)
	}
	if state.EventCount != 6 {
		t.Errorf("EventCount: got %d, want 6 (activity must bump counter)", state.EventCount)
	}
	if state.LastEvent != "transcript_activity" {
		t.Errorf("LastEvent: got %q, want transcript_activity", state.LastEvent)
	}
	if state.UpdatedAt <= beforeUpdate {
		t.Errorf("UpdatedAt: got %d, want > %d (activity must bump timestamp)", state.UpdatedAt, beforeUpdate)
	}

	for _, ev := range rec.snapshot() {
		if ev.SessionID == "away1" && ev.Kind == lifecycle.KindStateTransition {
			t.Errorf("unexpected state transition recorded: %+v", ev)
		}
	}
}

// TestSessionDetector_Activity_SubstantivePass_StillClassifies guards the
// inverse case: when NoSubstantiveActivity is false (a real event arrived),
// the existing force-bounce + ClassifyState path must still fire. Without
// this, the short-circuit could swallow legitimate transitions.
func TestSessionDetector_Activity_SubstantivePass_StillClassifies(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	rec := &mockRecorder{}
	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	repo.states["real1"] = &session.SessionState{
		SessionID:      "real1",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/real1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:         "assistant",
			HasOpenToolCall:       false,
			NoSubstantiveActivity: false,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "real1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/real1.jsonl",
	}

	// Poll the (mutex-safe) recorder for the transition instead of a fixed
	// sleep — under parallel load the event may not be processed within a
	// fixed window (issue #606).
	recordedTransition := func() bool {
		for _, ev := range rec.snapshot() {
			if ev.SessionID == "real1" && ev.Kind == lifecycle.KindStateTransition {
				return true
			}
		}
		return false
	}
	waitForCondition(recordedTransition, time.Second)
	cancel()
	<-done

	// At least one state-transition lifecycle event must be recorded —
	// proves the force-bounce / classifier path ran. The final state is
	// not asserted: it depends on classifier rules that aren't the subject
	// of this test.
	if !recordedTransition() {
		t.Errorf("expected at least one state-transition lifecycle event for real1, got none")
	}
}
