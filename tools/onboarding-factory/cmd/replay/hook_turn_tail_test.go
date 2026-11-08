package main

import (
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// The sidecar replay's mirror of the daemon's hook-turn-tail rule (#2109
// P5-0). shouldForceReadyToWorking says it mirrors the daemon's force guard,
// and the sidecar replays Stop hooks as held turn-done signals, so the rule
// can hold here too; without the mirror that doc comment would be false and
// the goldens would keep a bounce the daemon no longer records.
//
// These drive runClassifier the way applyHookEvent and classifyAt do — apply
// the hook's effect, overlay the holds, classify — over metrics written out by
// hand, so each test names the one shape it is about. The committed goldens
// are the end-to-end half: tools/replay-fixtures.sh and
// TestFixtureReplayByteIdentity.

var hookTailT0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func newHookTailReplayer(state string) *sidecarReplayer {
	return &sidecarReplayer{
		state:            state,
		prevTransitionAt: hookTailT0,
		stateDurations:   map[string]time.Duration{},
		signals:          session.NewSignalHolds(),
		report:           &replayReport{},
	}
}

// hookPass is applyHookEvent's classify half for one recorded hook.
func hookPass(t *testing.T, r *sidecarReplayer, hook string, m session.SessionMetrics, at time.Time) {
	t.Helper()
	effect, ok := session.HookSignal(hook)
	if !ok {
		t.Fatalf("session.HookSignal has no row for %q", hook)
	}
	r.signals.ApplyHook(replaySessionKey, effect, at)
	r.signals.Overlay(replaySessionKey, &m, at)
	r.runClassifier(&m, transitionCtx{eventIdx: -1, virtTime: at, cause: causeHook}, true)
}

// filePass is classifyAt's classify half for a pass that read new bytes.
func filePass(r *sidecarReplayer, m session.SessionMetrics, at time.Time) {
	r.signals.Overlay(replaySessionKey, &m, at)
	r.runClassifier(&m, transitionCtx{eventIdx: 0, virtTime: at, cause: causeDebounceCoalesce}, true)
}

func forcesFrom(trs []transition) int {
	n := 0
	for _, tr := range trs {
		if tr.Reason == services.ForceReadyToWorkingReason {
			n++
		}
	}
	return n
}

// TestSidecar_HookTurnTail_DoesNotForce is the replay form of the defect: a
// Stop hook ends the turn, and the debounced pass that reads the turn's own
// end afterwards (codex 2-13's task_complete, 3 ms after the hook) forced
// ready → working → ready.
func TestSidecar_HookTurnTail_DoesNotForce(t *testing.T) {
	r := newHookTailReplayer(session.StateWorking)
	hookPass(t, r, session.HookStop, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1}, hookTailT0)
	if r.state != session.StateReady {
		t.Fatalf("after the Stop hook: state = %q, want ready", r.state)
	}
	mark := len(r.report.Transitions)

	filePass(r, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1}, hookTailT0.Add(2*time.Second))

	if got := r.report.Transitions[mark:]; len(got) != 0 {
		t.Fatalf("the tail of a hook-ended turn emitted %d transition(s), want 0: %+v", len(got), got)
	}
}

// The locks below pass before the mirror by construction (the unmirrored
// sidecar forces on every one of these passes); they hold the mirror to the
// daemon's rule.

// TestSidecar_HookTurnTail_NewUserTurnForces: a new user turn after the hook's
// ready is new activity, done or not.
func TestSidecar_HookTurnTail_NewUserTurnForces(t *testing.T) {
	r := newHookTailReplayer(session.StateWorking)
	hookPass(t, r, session.HookStop, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1}, hookTailT0)
	mark := len(r.report.Transitions)

	filePass(r, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 2}, hookTailT0.Add(2*time.Second))

	if n := forcesFrom(r.report.Transitions[mark:]); n != 1 {
		t.Fatalf("a collapsed new turn forced %d time(s), want 1: %+v", n, r.report.Transitions[mark:])
	}
}

// TestSidecar_HookTurnTail_HookPassStillForces: a hook's own pass keeps the
// force, as in the daemon.
func TestSidecar_HookTurnTail_HookPassStillForces(t *testing.T) {
	r := newHookTailReplayer(session.StateWorking)
	hookPass(t, r, session.HookStop, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1}, hookTailT0)
	mark := len(r.report.Transitions)

	hookPass(t, r, session.HookPreToolUse, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1}, hookTailT0.Add(time.Second))

	if n := forcesFrom(r.report.Transitions[mark:]); n != 1 {
		t.Fatalf("a PreToolUse after a hook stop forced %d time(s), want 1: %+v", n, r.report.Transitions[mark:])
	}
}

// TestSidecar_HookTurnTail_MarksByVerdictNotCause: the marker is set by a
// hook-tier turn-done verdict, the daemon's condition, not by a pass that
// happens to be a hook's. A PostToolUse pass that reaches ready decides it
// from the transcript, so a later done pass still forces, as it would live.
func TestSidecar_HookTurnTail_MarksByVerdictNotCause(t *testing.T) {
	r := newHookTailReplayer(session.StateWorking)
	hookPass(t, r, session.HookPostToolUse, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1}, hookTailT0)
	if r.state != session.StateReady {
		t.Fatalf("after the PostToolUse pass: state = %q, want ready", r.state)
	}
	mark := len(r.report.Transitions)

	filePass(r, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1}, hookTailT0.Add(2*time.Second))

	if n := forcesFrom(r.report.Transitions[mark:]); n != 1 {
		t.Fatalf("after a transcript-decided ready on a hook pass, the done pass forced %d time(s), want 1: %+v", n, r.report.Transitions[mark:])
	}
}

// TestSidecar_HookTurnTail_MarkerEndsWithTheReady: once the session leaves the
// hook's ready, the marker is gone, so a later transcript ready and its tail
// are ordinary passes again.
func TestSidecar_HookTurnTail_MarkerEndsWithTheReady(t *testing.T) {
	r := newHookTailReplayer(session.StateWorking)
	hookPass(t, r, session.HookStop, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1}, hookTailT0)
	filePass(r, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1}, hookTailT0.Add(time.Second))
	if r.state != session.StateWorking {
		t.Fatalf("after a not-done pass: state = %q, want working", r.state)
	}
	filePass(r, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1}, hookTailT0.Add(2*time.Second))
	if r.state != session.StateReady {
		t.Fatalf("after the transcript turn end: state = %q, want ready", r.state)
	}
	mark := len(r.report.Transitions)

	filePass(r, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1}, hookTailT0.Add(3*time.Second))

	if n := forcesFrom(r.report.Transitions[mark:]); n != 1 {
		t.Fatalf("after a transcript-only ready the done pass forced %d time(s), want 1: %+v", n, r.report.Transitions[mark:])
	}
}

// TestSidecar_HookTurnTail_TeardownDropsTheMarker: a recorded process exit
// starts the next lifetime in ready without a classifier transition, so the
// marker has to be dropped there or the new lifetime's first done pass would
// be read as the old turn's tail.
func TestSidecar_HookTurnTail_TeardownDropsTheMarker(t *testing.T) {
	r := newHookTailReplayer(session.StateWorking)
	hookPass(t, r, session.HookStop, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1}, hookTailT0)
	if err := r.applyProcessTeardown(timelineEntry{ts: hookTailT0.Add(time.Second)}, &debounceState{}); err != nil {
		t.Fatalf("applyProcessTeardown: %v", err)
	}
	mark := len(r.report.Transitions)

	filePass(r, session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1}, hookTailT0.Add(2*time.Second))

	if n := forcesFrom(r.report.Transitions[mark:]); n != 1 {
		t.Fatalf("the next lifetime's first done pass forced %d time(s), want 1: %+v", n, r.report.Transitions[mark:])
	}
}
