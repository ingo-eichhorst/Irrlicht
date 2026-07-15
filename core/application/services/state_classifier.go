// StateClassifier provides pure functions for session state classification.
// These functions encapsulate the decision tree used to determine whether a
// session is working, waiting, or ready based on transcript metrics.
package services

import (
	"fmt"

	"irrlicht/core/domain/session"
)

// ClassifyState applies the decision tree to determine what state a session
// should be in based on its current state and latest metrics.
//
// Decision order (the body's rule numbering in parentheses):
//   - PermissionPending → waiting (0)
//   - CompactInProgress → working (0b)
//   - NeedsUserAttention → waiting (1)
//   - OpenToolStalled → waiting (1b)
//   - IsAgentDone → ready, from working/waiting only (2)
//   - ESC cancellation / tool denial (user + error + no open tools) → ready (3)
//   - Default → working (4)
//
// Returns (newState, reason). An empty reason means no transition occurred.
func ClassifyState(currentState string, metrics *session.SessionMetrics) (string, string) {
	if metrics == nil {
		return currentState, ""
	}

	// 0. Permission prompt is open (hook-based signal) → waiting.
	// This is the most authoritative signal — deterministic from Claude Code's
	// own hook system. Checked before NeedsUserAttention because it doesn't
	// depend on HasOpenToolCall (avoids race where hook fires before fswatcher
	// processes the tool_use JSONL event).
	if metrics.PermissionPending {
		return transitionTo(currentState, session.StateWaiting, "permission prompt open → waiting")
	}

	// 0b. Manual /compact in progress (PreCompact hook) → working, regardless
	// of the stale pre-compact turn_done that IsAgentDone() would otherwise read
	// as ready. Compaction writes nothing to the transcript for tens of seconds
	// to minutes; this overlay holds the session busy for that window (#657).
	// The detector clears it the pass the manual compact_boundary lands, which
	// then routes to ready via rule 2 (#656).
	if metrics.CompactInProgress {
		return transitionTo(currentState, session.StateWorking, "manual /compact in progress → working")
	}

	// 1. User-blocking tool open → waiting.
	if metrics.NeedsUserAttention() {
		return transitionTo(currentState, session.StateWaiting, "user-blocking tool open → waiting")
	}

	// 1b. A permission-gated file-edit tool has been open and idle long enough
	// that the agent is almost certainly blocked on a permission prompt →
	// waiting. Transcript-based fallback for when the curl-delivered
	// PermissionRequest hook can't reach the daemon (#488). The detector sets
	// OpenToolStalled only after the open tool has lingered with no transcript
	// progress, so this never fires on a tool that is actively executing.
	if metrics.OpenToolStalled {
		return transitionTo(currentState, session.StateWaiting, "stalled edit tool → likely permission prompt → waiting")
	}

	// 2. Agent finished turn — check if waiting for user input first.
	if metrics.IsAgentDone() {
		return classifyAgentDone(currentState, metrics)
	}

	// 3. User interruption: ESC or tool-permission denial while
	// working/waiting with no open tool calls → ready.
	//
	// ESC signal: "[Request interrupted by user]" (LastWasUserInterrupt).
	// Denial signal: "[Request interrupted by user for tool use]"
	// (LastWasToolDenial). After denial, Claude Code typically returns to
	// the prompt — the agent's turn is over. If the agent does continue
	// (writes a new assistant message), the next activity will transition
	// back to working.
	if isUserInterruptReady(currentState, metrics) {
		reason := "user ESC interrupt"
		if metrics.LastWasToolDenial {
			reason = "tool permission denied"
		}
		return session.StateReady,
			fmt.Sprintf("%s while %s → ready", reason, currentState)
	}

	// 4. Default: transcript activity → working.
	return transitionTo(currentState, session.StateWorking,
		fmt.Sprintf("transcript activity (%s → working)", currentState))
}

// transitionTo returns (target, reason) when currentState differs from
// target, or (currentState, "") as a no-op when it's already there — the
// repeated "transition or no-op" shape used by every ClassifyState rule.
func transitionTo(currentState, target, reason string) (string, string) {
	if currentState != target {
		return target, reason
	}
	return currentState, ""
}

// classifyAgentDone handles rule 2 of ClassifyState: the agent has finished
// its turn. It routes to waiting first when the turn ended with a question
// or imperative cue (issue #381), otherwise to ready.
func classifyAgentDone(currentState string, metrics *session.SessionMetrics) (string, string) {
	if metrics.IsWaitingForUserInput() {
		return transitionTo(currentState, session.StateWaiting, "turn ended with question or cue → waiting")
	}
	if currentState == session.StateWorking || currentState == session.StateWaiting {
		return session.StateReady, "agent finished turn → ready"
	}
	return currentState, ""
}

// isUserInterruptReady reports whether rule 3 of ClassifyState applies: the
// session was working/waiting with no open tool call, the last transcript
// event was from the user, and that event was an ESC interrupt or
// tool-permission denial — meaning the agent's turn is effectively over.
func isUserInterruptReady(currentState string, metrics *session.SessionMetrics) bool {
	if currentState != session.StateWorking && currentState != session.StateWaiting {
		return false
	}
	return !metrics.HasOpenToolCall && metrics.LastEventType == "user" &&
		(metrics.LastWasUserInterrupt || metrics.LastWasToolDenial)
}

// SyntheticWaitingReason is the reason string used for the working→waiting
// transition synthesised when a user-blocking tool's tool_use and
// tool_result are processed in the same tailer pass (issue #150).
const SyntheticWaitingReason = "user-blocking tool opened and closed in one pass → synthetic waiting"

// ForceReadyToWorkingReason is the reason string used when a ready session's
// metrics show fresh activity — the classifier forces the working transition
// so the next step can emit the eventual working→ready in the same pass.
const ForceReadyToWorkingReason = "force ready→working on first activity"

// ShouldSynthesizeCollapsedWaiting reports whether the caller should emit
// a synthetic working→waiting transition before applying the classifier's
// result. This recovers the brief waiting episode that fswatcher collapsed
// when it coalesced the tool_use and tool_result writes of a user-blocking
// tool (AskUserQuestion / ExitPlanMode) into one event.
//
// Fires only when the session was already working (so the classifier has
// no natural way to route through waiting) and the classifier is NOT
// already transitioning to waiting on its own. Two concrete same-pass
// collapse variants reach this:
//
//   - Case A: tool_result carries is_error=true AND a trailing user text
//     "[Request interrupted by user for tool use]" sets LastWasToolDenial.
//     Classifier returns ready via rule 3.
//   - Case B: the denial user text is followed by another user event in
//     the same pass, which clears LastWasToolDenial. Classifier then
//     returns working via rule 4 default. Without this helper the user
//     never sees the waiting episode at all.
//
// Callers (SessionDetector.processActivity and the replay harness) should,
// on true: emit working→waiting with SyntheticWaitingReason, set the
// effective current state to waiting, and re-run ClassifyState so the
// next transition carries the correct "while waiting" phrasing.
func ShouldSynthesizeCollapsedWaiting(currentState, newState string, metrics *session.SessionMetrics) bool {
	if currentState != session.StateWorking || newState == session.StateWaiting {
		return false
	}
	if metrics == nil {
		return false
	}
	return metrics.SawUserBlockingToolClosedThisPass
}

// SyntheticTurnSettleReason is the reason string for the synthetic
// working→ready transition emitted when a tailer pass collapses a
// genuinely distinct queued turn boundary (issue #988): a turn completed
// and a follow-up turn began in the same pass, with no observable ready
// gap between them (e.g. mistral-vibe's in-memory message queue, which
// drains a follow-up prompt the instant the prior turn clears).
const SyntheticTurnSettleReason = "turn completed, queued follow-up began in the same pass → synthetic settle"

// SyntheticQueuedTurnStartReason is the reason string for the synthetic
// ready→working transition immediately following SyntheticTurnSettleReason,
// representing the queued follow-up turn's own start.
const SyntheticQueuedTurnStartReason = "queued follow-up turn began → synthetic re-open"

// ShouldSynthesizeCollapsedTurnBoundary reports whether the caller should
// emit a synthetic working→ready→working pair before applying the
// classifier's result. This recovers the turn boundary that the tailer's
// batch scan collapsed when a queued follow-up turn began (and possibly
// completed) in the same pass as the prior turn's own turn_done — the
// batch-scan analog of ShouldSynthesizeCollapsedWaiting (issue #150), but
// for a turn_done boundary instead of a user-blocking tool.
//
// Fires only when the session was already working (a settle→re-open pair
// only makes sense mid-turn) and no overlay signal says the session is
// actually blocked on something else this pass (a real pending permission
// prompt or an in-progress manual compaction) — those forced states are
// not consistent with "a turn genuinely completed and restarted".
//
// Callers (SessionDetector.classifyAndTransition and the replay engine)
// should, on true: emit working→ready with SyntheticTurnSettleReason, then
// ready→working with SyntheticQueuedTurnStartReason, and set the effective
// current state to working so the classifier's already-computed verdict
// (typically ready, since LastEventType is the queued turn's own
// turn_done) applies as the real final transition.
func ShouldSynthesizeCollapsedTurnBoundary(currentState string, metrics *session.SessionMetrics) bool {
	if currentState != session.StateWorking {
		return false
	}
	if metrics == nil {
		return false
	}
	if metrics.PermissionPending || metrics.CompactInProgress {
		return false
	}
	return metrics.SawMidPassTurnBoundary
}

// SyntheticCatchUpTurnStartReason is the reason string for the synthetic
// ready→working transition emitted when a brand-new session's very first
// observation already shows a completed turn — discovery was delayed long
// enough that the turn finished before the daemon ever looked (issue #996).
// Paired with SyntheticCatchUpTurnDoneReason; see ShouldSynthesizeCatchUpTurn
// for when this fires.
const SyntheticCatchUpTurnStartReason = "new session created (turn already in progress at first discovery)"

// SyntheticCatchUpTurnDoneReason is the reason string for the working→ready
// half of the same synthetic pair (see SyntheticCatchUpTurnStartReason).
const SyntheticCatchUpTurnDoneReason = "turn already complete at first discovery → synthetic catch-up"

// ShouldSynthesizeCatchUpTurn reports whether a brand-new session's initial
// lifecycle record should be a synthetic ready→working→ready pair instead of
// a single flat "new session created" transition — for when discovery was
// delayed long enough that the first turn already completed (metrics.IsAgentDone())
// before the daemon ever looked, which would otherwise silently swallow it
// and mislead downstream turn-boundary consumers about which turn was first
// (issue #996, extended to child/subagent sessions by issue #999).
//
// hasLiveOrigin is the load-bearing half of the gate — proof of a genuinely
// live precursor, not an ordinary cold-start rediscovery of a large backlog
// of old, already-finished sessions (which must never get a spurious
// bounce). Its meaning depends on the caller: for a top-level session it's
// true only when this session is superseding a pre-session (proc-<pid>) the
// daemon was already live-tracking for the same project/cwd (see
// cleanupPreSessionsForProject's doc comment); for a child/subagent session,
// which never gets a pre-session of its own, it's true when the parent
// session's own OS process is still alive right now (see
// PIDManager.parentProcessLive).
func ShouldSynthesizeCatchUpTurn(hasLiveOrigin bool, metrics *session.SessionMetrics) bool {
	return hasLiveOrigin && metrics.IsAgentDone()
}
