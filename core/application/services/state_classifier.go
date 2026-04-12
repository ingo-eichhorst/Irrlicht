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

