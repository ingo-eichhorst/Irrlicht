// StateClassifier provides pure functions for session state classification.
// These functions encapsulate the four-way decision tree used to determine
// whether a session is working, waiting, or ready based on transcript metrics.
package services

import (
	"fmt"

	"irrlicht/core/domain/session"
)

// ClassifyState applies the four-way decision tree to determine what state a
// session should be in based on its current state and latest metrics.
//
// Decision order:
//  1. NeedsUserAttention → waiting
//  2. IsAgentDone → ready (from working/waiting only)
//  3. ESC cancellation (user + error + no open tools) → ready
//  4. Default → working
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
