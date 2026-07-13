package services_test

import (
	"context"
	"os"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// transitionsFor returns the KindStateTransition events recorded for
// sessionID, in emission order.
func transitionsFor(rec *mockRecorder, sessionID string) []lifecycle.Event {
	var out []lifecycle.Event
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindStateTransition && ev.SessionID == sessionID {
			out = append(out, ev)
		}
	}
	return out
}

// TestSessionDetector_CatchUpTurn_SynthesizesWhenSupersedingLivePreSession is
// the regression test for issue #996: a mistral-vibe-shaped race where the
// daemon binds a pre-session (proc-<pid>) to the running process almost
// instantly, but the real transcript is discovered late enough that its
// first turn has already fully completed. Because this real session is
// superseding a pre-session the daemon was already live-tracking, the
// missing working→ready cycle for that turn must be synthesized rather than
// silently swallowed.
func TestSessionDetector_CatchUpTurn_SynthesizesWhenSupersedingLivePreSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	rec := &mockRecorder{}

	// Simulates a transcript whose full content is already parsed as a
	// completed turn by the time the daemon first looks — mirrors
	// mistral-vibe's 534-byte messages.jsonl in the recorded fixture.
	metrics := &funcMetrics{fn: func(path, adapter string) (*session.SessionMetrics, error) {
		if path == "" {
			return nil, nil
		}
		return &session.SessionMetrics{LastEventType: "turn_done"}, nil
	}}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Pre-session: the process scanner spots the running agent before any
	// transcript exists (mirrors processlifecycle.Scanner minting proc-<pid>).
	tw.ch <- agent.Event{
		Type:       agent.EventNewSession,
		SessionID:  "proc-996",
		ProjectDir: "vibe-project",
	}
	waitForCondition(func() bool { s, _ := repo.Load("proc-996"); return s != nil }, time.Second)

	// The real transcript appears late, already showing a completed turn.
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "session_real_1",
		ProjectDir:     "vibe-project",
		TranscriptPath: "/home/.vibe/logs/session/session_real_1/messages.jsonl",
	}
	waitForCondition(func() bool { s, _ := repo.Load("session_real_1"); return s != nil }, time.Second)
	// cleanupPreSessionsForProject runs synchronously within the same
	// finalizeNewSession call — wait for its actual effect (the pre-session
	// gone) rather than a fixed sleep.
	waitForCondition(func() bool { s, _ := repo.Load("proc-996"); return s == nil }, time.Second)
	cancel()
	<-done

	transitions := transitionsFor(rec, "session_real_1")
	if len(transitions) != 2 {
		t.Fatalf("expected 2 synthesized state transitions for session_real_1, got %d: %+v", len(transitions), transitions)
	}
	if transitions[0].NewState != session.StateWorking || transitions[0].Reason != services.SyntheticCatchUpTurnStartReason {
		t.Errorf("first transition = %+v, want new_state=working reason=%q", transitions[0], services.SyntheticCatchUpTurnStartReason)
	}
	if transitions[1].PrevState != session.StateWorking || transitions[1].NewState != session.StateReady ||
		transitions[1].Reason != services.SyntheticCatchUpTurnDoneReason {
		t.Errorf("second transition = %+v, want working->ready reason=%q", transitions[1], services.SyntheticCatchUpTurnDoneReason)
	}

	state, err := repo.Load("session_real_1")
	if err != nil || state == nil {
		t.Fatalf("session_real_1 not created: %v", err)
	}
	if state.State != session.StateReady {
		t.Errorf("persisted state: got %q, want ready (synthesis only changes recorded history, not the settled state)", state.State)
	}
}

// TestSessionDetector_CatchUpTurn_NoSynthesisWithoutLivePreSession is the
// critical regression guard for issue #996's design decision: a cold-started
// daemon rediscovering an already-finished, historical session (no live
// pre-session was ever tracked for it — nobody is watching this process)
// must NOT get a synthetic bounce. Firing unconditionally on
// metrics.IsAgentDone() alone would flood the lifecycle stream every time a
// daemon with a large backlog starts up, which is exactly the scenario that
// exposed this issue's own multi-second discovery delay.
func TestSessionDetector_CatchUpTurn_NoSynthesisWithoutLivePreSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	rec := &mockRecorder{}

	metrics := &funcMetrics{fn: func(path, adapter string) (*session.SessionMetrics, error) {
		if path == "" {
			return nil, nil
		}
		return &session.SessionMetrics{LastEventType: "turn_done"}, nil
	}}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// No pre-session event at all — this transcript is discovered cold,
	// with no evidence the daemon was ever live-tracking its process.
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "session_old_1",
		ProjectDir:     "old-project",
		TranscriptPath: "/home/.vibe/logs/session/session_old_1/messages.jsonl",
	}
	waitForCondition(func() bool { s, _ := repo.Load("session_old_1"); return s != nil }, time.Second)
	time.Sleep(30 * time.Millisecond) // let any (undesired) second transition land before asserting
	cancel()
	<-done

	transitions := transitionsFor(rec, "session_old_1")
	if len(transitions) != 1 {
		t.Fatalf("expected 1 (unchanged) state transition for session_old_1, got %d: %+v", len(transitions), transitions)
	}
	if transitions[0].NewState != session.StateReady || transitions[0].Reason != "new session created" {
		t.Errorf("transition = %+v, want the ordinary flat new_state=ready reason=\"new session created\"", transitions[0])
	}
}

// TestSessionDetector_CatchUpTurn_NoSynthesisWhenTurnNotYetDone is the
// other regression guard: the ordinary, non-buggy fast-discovery path (a
// pre-session superseded by a real session whose transcript does NOT yet
// show a completed turn) must also stay unchanged — synthesis only applies
// when a turn was actually swallowed.
func TestSessionDetector_CatchUpTurn_NoSynthesisWhenTurnNotYetDone(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	rec := &mockRecorder{}

	metrics := &funcMetrics{fn: func(path, adapter string) (*session.SessionMetrics, error) {
		if path == "" {
			return nil, nil
		}
		// Turn is still in flight — no turn_done yet.
		return &session.SessionMetrics{LastEventType: "user"}, nil
	}}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:       agent.EventNewSession,
		SessionID:  "proc-997",
		ProjectDir: "cc-project",
	}
	waitForCondition(func() bool { s, _ := repo.Load("proc-997"); return s != nil }, time.Second)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "session_real_2",
		ProjectDir:     "cc-project",
		TranscriptPath: "/home/.claude/projects/cc-project/session_real_2.jsonl",
	}
	waitForCondition(func() bool { s, _ := repo.Load("session_real_2"); return s != nil }, time.Second)
	waitForCondition(func() bool { s, _ := repo.Load("proc-997"); return s == nil }, time.Second)
	cancel()
	<-done

	transitions := transitionsFor(rec, "session_real_2")
	if len(transitions) != 1 {
		t.Fatalf("expected 1 (unchanged) state transition for session_real_2, got %d: %+v", len(transitions), transitions)
	}
	if transitions[0].NewState != session.StateReady || transitions[0].Reason != "new session created" {
		t.Errorf("transition = %+v, want the ordinary flat new_state=ready reason=\"new session created\"", transitions[0])
	}
}

// TestSessionDetector_CatchUpTurn_ChildSynthesizesWhenParentProcessLive is the
// regression test for issue #999: a child (subagent) session never gets a
// pre-session of its own, so finalizeNewSession's synthesis gate can't use
// supersedingLivePreSession for it as it does for a top-level session (see
// TestSessionDetector_CatchUpTurn_SynthesizesWhenSupersedingLivePreSession
// above). Instead it uses parentProcessLive — proof the parent's OS process
// is still running right now. When the parent is alive and the child's very
// first observation already shows a completed turn, the missing
// working->ready cycle must be synthesized, mirroring the top-level case.
func TestSessionDetector_CatchUpTurn_ChildSynthesizesWhenParentProcessLive(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	rec := &mockRecorder{}

	metrics := &funcMetrics{fn: func(path, adapter string) (*session.SessionMetrics, error) {
		if path == "" {
			return nil, nil
		}
		return &session.SessionMetrics{LastEventType: "turn_done"}, nil
	}}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)

	// The parent is a real, live OS process — PID = the test process's own
	// PID, the reliably-alive convention already used elsewhere in this
	// suite (see pid_manager_test.go's livePID).
	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "parent-999-live",
		State:          session.StateWorking,
		PID:            os.Getpid(),
		TranscriptPath: "/home/.claude/projects/-Users-test/parent-999-live.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// The child's transcript is discovered late, already showing a completed
	// turn — the same swallowed-first-turn shape as #996, one level down.
	tw.ch <- agent.Event{
		Type:            agent.EventNewSession,
		SessionID:       "child-999-live",
		ProjectDir:      "subagents",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent-999-live/subagents/child-999-live.jsonl",
		ParentSessionID: "parent-999-live",
	}
	waitForCondition(func() bool { s, _ := repo.Load("child-999-live"); return s != nil }, time.Second)
	cancel()
	<-done

	transitions := transitionsFor(rec, "child-999-live")
	if len(transitions) != 2 {
		t.Fatalf("expected 2 synthesized state transitions for child-999-live, got %d: %+v", len(transitions), transitions)
	}
	if transitions[0].NewState != session.StateWorking || transitions[0].Reason != services.SyntheticCatchUpTurnStartReason {
		t.Errorf("first transition = %+v, want new_state=working reason=%q", transitions[0], services.SyntheticCatchUpTurnStartReason)
	}
	if transitions[1].PrevState != session.StateWorking || transitions[1].NewState != session.StateReady ||
		transitions[1].Reason != services.SyntheticCatchUpTurnDoneReason {
		t.Errorf("second transition = %+v, want working->ready reason=%q", transitions[1], services.SyntheticCatchUpTurnDoneReason)
	}
}

// TestSessionDetector_CatchUpTurn_ChildNoSynthesisWithoutLiveParent is the
// critical regression guard for issue #999's design decision, mirroring
// #996's own NoSynthesisWithoutLivePreSession guard one level down: a child
// whose parent's OS process is confirmed dead, or whose parent session is
// unknown altogether — exactly what a daemon restart rediscovering old
// historical subagents looks like — must NOT get a synthetic bounce even
// though its own first observation already shows a completed turn. Firing
// unconditionally on metrics.IsAgentDone() alone would flood the lifecycle
// stream with spurious bounces for every already-finished historical
// subagent within the backlog-scan's age cutoff.
func TestSessionDetector_CatchUpTurn_ChildNoSynthesisWithoutLiveParent(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	rec := &mockRecorder{}

	metrics := &funcMetrics{fn: func(path, adapter string) (*session.SessionMetrics, error) {
		if path == "" {
			return nil, nil
		}
		return &session.SessionMetrics{LastEventType: "turn_done"}, nil
	}}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)

	// Parent exists in the repo but its OS process is confirmed dead — a
	// historical subagent's parent, long gone.
	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "parent-999-dead",
		State:          session.StateReady,
		PID:            deadPIDForTest(t),
		TranscriptPath: "/home/.claude/projects/-Users-test/parent-999-dead.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	tw.ch <- agent.Event{
		Type:            agent.EventNewSession,
		SessionID:       "child-999-dead-parent",
		ProjectDir:      "subagents",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent-999-dead/subagents/child-999-dead-parent.jsonl",
		ParentSessionID: "parent-999-dead",
	}
	waitForCondition(func() bool { s, _ := repo.Load("child-999-dead-parent"); return s != nil }, time.Second)

	// No parent session at all — an even colder case than a dead PID.
	tw.ch <- agent.Event{
		Type:            agent.EventNewSession,
		SessionID:       "child-999-no-parent",
		ProjectDir:      "subagents",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent-999-missing/subagents/child-999-no-parent.jsonl",
		ParentSessionID: "parent-999-missing",
	}
	waitForCondition(func() bool { s, _ := repo.Load("child-999-no-parent"); return s != nil }, time.Second)

	time.Sleep(30 * time.Millisecond) // let any (undesired) second transition land before asserting
	cancel()
	<-done

	for _, sid := range []string{"child-999-dead-parent", "child-999-no-parent"} {
		transitions := transitionsFor(rec, sid)
		if len(transitions) != 1 {
			t.Fatalf("%s: expected 1 (unchanged) state transition, got %d: %+v", sid, len(transitions), transitions)
		}
		if transitions[0].NewState != session.StateReady || transitions[0].Reason != "new session created" {
			t.Errorf("%s: transition = %+v, want the ordinary flat new_state=ready reason=\"new session created\"", sid, transitions[0])
		}
	}
}
