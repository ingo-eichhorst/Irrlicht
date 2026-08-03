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

func (bornGit) GetBranch(string) string               { return "main" }
func (bornGit) GetProjectName(string) string          { return "project" }
func (bornGit) GetCWDFromTranscript(string) string    { return "" }
func (bornGit) GetBranchFromTranscript(string) string { return "" }

type bornLog struct{ outbound.Logger }

func (bornLog) LogInfo(component, session, msg string)  {}
func (bornLog) LogError(component, session, msg string) {}

// openTurnMetrics is a metrics collector whose transcript already shows an
// OPEN turn at the moment the session is discovered — a user message with no
// turn-done after it.
type openTurnMetrics struct{ outbound.MetricsCollector }

func (openTurnMetrics) ComputeMetrics(path, adapter string) (*session.SessionMetrics, error) {
	return &session.SessionMetrics{LastEventType: "user_message"}, nil
}

// emptyMetrics is the common case: nothing on disk to classify yet.
type emptyMetrics struct{ outbound.MetricsCollector }

func (emptyMetrics) ComputeMetrics(path, adapter string) (*session.SessionMetrics, error) {
	return nil, nil
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
