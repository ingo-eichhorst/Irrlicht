package services

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Minimal doubles for the three deps buildNewSessionState reaches. Each embeds
// its port interface so only the methods actually exercised need bodies; an
// unexpected call panics loudly rather than silently returning a zero value.
type bornGit struct{ outbound.GitResolver }

func (bornGit) GetBranch(string) (string, bool)      { return "main", true }
func (bornGit) GetProjectName(string) (string, bool) { return "project", true }

type bornLog struct{ outbound.Logger }

func (bornLog) LogInfo(_, _, _ string)  {}
func (bornLog) LogError(_, _, _ string) {}

// openTurnMetrics is a metrics collector whose transcript already shows an
// OPEN turn at the moment the session is discovered — a user message with no
// turn-done after it.
type openTurnMetrics struct{ outbound.MetricsCollector }

func (openTurnMetrics) ComputeMetrics(_, _ string) (*session.SessionMetrics, error) {
	return &session.SessionMetrics{LastEventType: "user_message"}, nil
}

// emptyMetrics is the common case: nothing on disk to classify yet.
type emptyMetrics struct{ outbound.MetricsCollector }

func (emptyMetrics) ComputeMetrics(_, _ string) (*session.SessionMetrics, error) {
	return nil, nil
}

// insubstantialMetrics is what the collector actually returns for an agent
// whose transcript exists and has been parsed at discovery but whose every
// line so far carries no observable signal — the tailer's own
// NoSubstantiveActivity verdict (`linesParsed > 0 && !substantive`).
//
// This is NOT the same as emptyMetrics above, and the difference is the whole
// of issue #1447: emptyMetrics returns a nil pointer, which ClassifyState
// short-circuits on, so it can never reach the rule ladder. Non-nil metrics
// always reach the ladder, and the ladder is total.
type insubstantialMetrics struct{ outbound.MetricsCollector }

func (insubstantialMetrics) ComputeMetrics(_, _ string) (*session.SessionMetrics, error) {
	return &session.SessionMetrics{NoSubstantiveActivity: true}, nil
}

// TestNewTopLevelSession_ClassifiedAgainstItsOwnMetrics pins the "born ready"
// bug found by recording GitHub Copilot's session-end cell (#1256).
//
// A new session was birthed flat `ready` and only RE-classified against its
// freshly-computed metrics when it had a ParentSessionID — child sessions got
// the treatment, top-level ones did not. That is invisible for an agent whose
// transcript file appears empty and fills in later (Claude Code creates the
// file, then writes), because at discovery there is genuinely nothing to
// classify.
//
// It is very visible for GitHub Copilot, which creates events.jsonl only when
// the first prompt is sent: the file therefore ALREADY contains the open turn
// when the watcher first sees it. The whole first turn is inside the discovery
// backlog, so the session sat in `ready` while the agent was demonstrably
// generating — 10.9s in one recording, and a SIGKILLed session never left
// ready at all. Replaying the same transcript offline produced the correct
// ready->working, which is what identified this as a live-path-only gap.
func TestNewTopLevelSession_ClassifiedAgainstItsOwnMetrics(t *testing.T) {
	d := NewSessionDetector(nil, SessionDetectorDeps{
		Log:     bornLog{},
		Git:     bornGit{},
		Metrics: openTurnMetrics{},
	})

	state := d.buildNewSessionState(
		agent.Identity{Name: "copilot"},
		agent.Event{SessionID: "s1", TranscriptPath: "/tmp/x/events.jsonl", CWD: "/tmp/x"},
		1000,
	)

	if state.State != session.StateWorking {
		t.Errorf("State = %q, want %q — the transcript already showed an open turn at "+
			"discovery, so birthing the session `ready` reports a generating agent as idle",
			state.State, session.StateWorking)
	}
}

// TestNewTopLevelSession_StaysReadyWhenNothingToClassify guards the other
// direction: an adapter whose transcript is empty at discovery (the common
// case) must still be born `ready`, not dragged to working by the change
// above. This one passes by construction and is a lock, not a defect test.
func TestNewTopLevelSession_StaysReadyWhenNothingToClassify(t *testing.T) {
	d := NewSessionDetector(nil, SessionDetectorDeps{
		Log:     bornLog{},
		Git:     bornGit{},
		Metrics: emptyMetrics{},
	})

	state := d.buildNewSessionState(
		agent.Identity{Name: "claude-code"},
		agent.Event{SessionID: "s2", TranscriptPath: "/tmp/y/a.jsonl", CWD: "/tmp/y"},
		1000,
	)

	if state.State != session.StateReady {
		t.Errorf("State = %q, want %q — a session with no metrics to classify must stay ready",
			state.State, session.StateReady)
	}
}

// TestNewTopLevelSession_StaysReadyWhenParsedLinesAreAllInsubstantial is the
// defect test for issue #1447: codex sessions became born `working`.
//
// The sibling lock above passes only because its collector returns a NIL
// pointer, and ClassifyState short-circuits on nil (state_classifier.go:
// `if metrics == nil { return currentState }`). That leaves the
// non-nil-but-insubstantial case — the one every header-linked adapter
// actually produces — completely uncovered, and it does not behave the same
// way at all: non-nil metrics enter the rule ladder, the ladder is
// deliberately TOTAL (its last rung `transcript_activity` has a nil `when`
// and always fires), so the verdict is `working` no matter how little
// evidence there is.
//
// Codex hits this on every single session. It is a header-linked adapter
// (#1055), so the fswatcher parks the zero-byte create and emits
// EventNewSession only once the rollout's first line is readable — which
// guarantees the birth event always carries a parseable file and therefore
// non-nil metrics. Both lines codex has written by then, `session_meta` and
// `event_msg`/`task_started`, are explicitly `Skip = true` in its parser, so
// the metrics that reach the ladder describe nothing at all.
//
// Replaying the same transcript offline still produces the correct `ready`
// while the live daemon says `working`, because the replay engine bails on
// insubstantial metrics and the birth path did not.
//
// The guard this fix uses is deliberately NOT the one those other sites use,
// and the asymmetry is the point rather than an oversight. The activity path's
// skipClassification and replayengine.Engine both gate on
// NoSubstantiveActivity, a PER-PASS flag (`linesParsed > 0 && !substantive`),
// so an EMPTY pass leaves it false. The only pre-existing `LastEventType != ""`
// bails are forceReadyToWorkingIfActive and the replay engine's force-bounce,
// and birth follows those: the question at birth is cumulative ("has anything
// ever been parsed off this transcript") rather than per-pass, and
// applySkippedEvent can mark a pass substantive without ever setting
// LastEventType. Do not "restore consistency" by switching this to
// NoSubstantiveActivity — TestSession_BornReadyOnAZeroByteTranscript in
// core/e2e is the lock that fails when you do.
func TestNewTopLevelSession_StaysReadyWhenParsedLinesAreAllInsubstantial(t *testing.T) {
	d := NewSessionDetector(nil, SessionDetectorDeps{
		Log:     bornLog{},
		Git:     bornGit{},
		Metrics: insubstantialMetrics{},
	})

	state := d.buildNewSessionState(
		agent.Identity{Name: "codex"},
		agent.Event{SessionID: "s3", TranscriptPath: "/tmp/z/rollout.jsonl", CWD: "/tmp/z"},
		1000,
	)

	if state.State != session.StateReady {
		t.Errorf("State = %q, want %q — every parsed line was insubstantial, so there is no "+
			"evidence of an open turn; birthing `working` reports an idle agent as generating",
			state.State, session.StateReady)
	}
}

// TestNewChildSession_StillBornWorkingOnInsubstantialMetrics is a LOCK, not a
// defect test: it passes on main by construction, because on main every
// session is classified at birth. It exists because the #1447 fix's first
// draft broke it, and nothing else in the suite noticed.
//
// A child is classified at birth for a reason that has nothing to do with
// evidence — it is about being COUNTED. hasActiveChildren ignores a child
// sitting in `ready`, so a parent whose own turn-done metrics are unchanged
// has nothing holding it, and RunStaleSessionRefresh flips it back to `ready`
// while the subagent is still running. That is #889 verbatim, and
// holdParentWorkingForNewChild does not cover it: that hold only moves the
// parent in memory for an instant, and it is the child's PERSISTED `working`
// that survives the sweep.
//
// The existing #889 tests cannot catch this, which is the whole reason this
// one is here: workflowChildMetrics hands its child LastEventType "tool_use",
// so an evidence-based guard passes and they stay green. The population at
// risk is the child whose transcript is empty or all-skip at discovery — and
// that is the common case, because the fswatcher parks a zero-byte create only
// for header-linked adapters and codex is the only one, so a Claude Code
// subagent transcript emits its birth event at size zero.
func TestNewChildSession_StillBornWorkingOnInsubstantialMetrics(t *testing.T) {
	d := NewSessionDetector(nil, SessionDetectorDeps{
		Log:     bornLog{},
		Git:     bornGit{},
		Metrics: insubstantialMetrics{},
	})

	state := d.buildNewSessionState(
		agent.Identity{Name: "claude-code"},
		agent.Event{
			SessionID:       "child1",
			ParentSessionID: "parent1",
			TranscriptPath:  "/tmp/w/child.jsonl",
			CWD:             "/tmp/w",
		},
		1000,
	)

	if state.State != session.StateWorking {
		t.Errorf("State = %q, want %q — a child born `ready` does not count in "+
			"hasActiveChildren, so the stale-session sweep releases its parent while the "+
			"subagent is still running (#889)",
			state.State, session.StateWorking)
	}
}
