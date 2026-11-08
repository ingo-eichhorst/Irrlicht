package services

import "irrlicht/core/domain/session"

// HookTurnTailReason is logged when forceReadyToWorkingIfActive skips the
// force for the tail of a turn a hook already ended (#2109 P5-0). The
// orchestrator's live checker for that phase matches this text exactly in
// daemon.log, so it is logged on its own, with nothing appended.
const HookTurnTailReason = "turn-end tail after a hook turn end — not new activity"

// HookTurnEnd is what a detector keeps about the hook verdict that put a
// session in ready: the session's user-turn count at that moment. The live
// detector and the sidecar replay each keep one per session, set it from
// HookTurnEndOf when a transition is applied, and drop it when the session
// leaves ready.
type HookTurnEnd struct {
	// UserTurns is SessionMetrics.UserTurns on the pass the hook decided.
	UserTurns int
}

// HookTurnEndOf returns the marker a transition to newState leaves behind:
// non-nil only when a hook's turn-done decided a ready, that is, the agent_done
// rule at the tier tierAgentDone gives it when HookTurnDone is set. A
// transcript-decided ready, or any other state, returns nil, which drops an
// earlier marker.
func HookTurnEndOf(newState string, v StateVerdict, m *session.SessionMetrics) *HookTurnEnd {
	if newState != session.StateReady || m == nil {
		return nil
	}
	if v.Rule != ruleAgentDone || v.Tier != session.TierOf(session.SignalTurnDone) {
		return nil
	}
	return &HookTurnEnd{UserTurns: m.UserTurns}
}

// IsHookTurnTail reports whether a pass over a ready session is only the tail
// of the turn a hook already ended, so forcing it ready → working would be a
// bounce rather than new activity. The maintainer's rule for #2109: a turn-end
// store event on a session a hook already ended does not count as new
// activity. All four must hold:
//
//  1. The pass is not a hook's own (synthetic is false). Every hook keeps the
//     force — PreToolUse, PermissionRequest, PreCompact (#307, #657), Stop.
//  2. A hook decided the session's current ready (end is non-nil).
//  3. No user turn has started since: the count is the one stored with the
//     marker, and that stored count is above zero. Zero means the metrics
//     source does not count user turns (hermes) or has read none yet, and the
//     force runs as it did before.
//  4. The metrics still read the turn as done, with no background process.
//     A pass that reads not done is new work the classifier moves to working
//     on its own rule; a background process needs the force so the liveness
//     probe, which only runs for working sessions, sees it (#937).
//
// ev.Terminal is deliberately not a condition. Among non-hook passes only the
// opencode and hermes store watchers set it (their watcher.go files), and
// claudecode's and codex's tails arrive without it.
//
// Called by SessionDetector.forceReadyToWorkingIfActive and by the sidecar
// replay's mirror of it (tools/onboarding-factory/cmd/replay), so the two
// apply one rule. Each keeps its own marker, set from HookTurnEndOf.
func IsHookTurnTail(end *HookTurnEnd, synthetic bool, m *session.SessionMetrics) bool {
	if synthetic {
		return false
	}
	if end == nil || m == nil {
		return false
	}
	if end.UserTurns <= 0 {
		return false
	}
	if m.UserTurns != end.UserTurns {
		return false
	}
	return m.IsAgentDone() && m.BackgroundProcessCount == 0
}

// noteHookTurnEnd stores the marker a just-applied transition leaves behind
// (see HookTurnEndOf), or drops the session's marker when it leaves none.
func (d *SessionDetector) noteHookTurnEnd(sessionID string, end *HookTurnEnd) {
	if end == nil {
		d.forgetHookTurnEnd(sessionID)
		return
	}
	d.hookTurnMu.Lock()
	d.hookTurnEnds[sessionID] = *end
	d.hookTurnMu.Unlock()
}

// hookTurnEnd returns the session's marker, or nil when it has none.
func (d *SessionDetector) hookTurnEnd(sessionID string) *HookTurnEnd {
	d.hookTurnMu.Lock()
	defer d.hookTurnMu.Unlock()
	end, ok := d.hookTurnEnds[sessionID]
	if !ok {
		return nil
	}
	return &end
}

// forgetHookTurnEnd drops the session's marker. Called wherever a session
// leaves ready, and on session teardown.
func (d *SessionDetector) forgetHookTurnEnd(sessionID string) {
	d.hookTurnMu.Lock()
	delete(d.hookTurnEnds, sessionID)
	d.hookTurnMu.Unlock()
}
