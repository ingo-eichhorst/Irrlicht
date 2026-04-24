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
		if currentState != session.StateWaiting {
			return session.StateWaiting, "permission prompt open → waiting"
		}
		return currentState, ""
	}

	// 1. User-blocking tool open → waiting.
	if metrics.NeedsUserAttention() {
		if currentState != session.StateWaiting {
			return session.StateWaiting, "user-blocking tool open → waiting"
		}
		return currentState, ""
	}

	// 2. Agent finished turn — check if waiting for user input first.
	if metrics.IsAgentDone() {
		// 2a. Turn ended with a question → waiting (agent needs user input).
		if metrics.IsWaitingForUserInput() {
			if currentState != session.StateWaiting {
				return session.StateWaiting, "turn ended with question → waiting"
			}
			return currentState, ""
		}
		// 2b. Normal turn completion → ready.
		if currentState == session.StateWorking || currentState == session.StateWaiting {
			return session.StateReady, "agent finished turn → ready"
		}
		return currentState, ""
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
	if (currentState == session.StateWorking || currentState == session.StateWaiting) &&
		!metrics.HasOpenToolCall && metrics.LastEventType == "user" &&
		(metrics.LastWasUserInterrupt || metrics.LastWasToolDenial) {
		reason := "user ESC interrupt"
		if metrics.LastWasToolDenial {
			reason = "tool permission denied"
		}
		return session.StateReady,
			fmt.Sprintf("%s while %s → ready", reason, currentState)
	}

	// 4. Default: transcript activity → working.
	if currentState != session.StateWorking {
		return session.StateWorking,
			fmt.Sprintf("transcript activity (%s → working)", currentState)
	}

	return currentState, ""
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

