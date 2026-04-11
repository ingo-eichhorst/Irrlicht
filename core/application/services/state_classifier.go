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

	// 3. ESC cancellation: user explicitly interrupted the turn while
	// working/waiting with no open tool calls → ready. The signal is the
	// "[Request interrupted by user" text marker, tracked via
	// LastWasUserInterrupt — generic tool_result errors are benign (grep
	// miss, failed build, etc.) and must not trigger this path.
	if (currentState == session.StateWorking || currentState == session.StateWaiting) &&
		!metrics.HasOpenToolCall && metrics.LastEventType == "user" && metrics.LastWasUserInterrupt {
		return session.StateReady,
			fmt.Sprintf("user ESC interrupt while %s → ready (cancellation)", currentState)
	}

	// 4. Default: transcript activity → working.
	if currentState != session.StateWorking {
		return session.StateWorking,
			fmt.Sprintf("transcript activity (%s → working)", currentState)
	}

	return currentState, ""
}

