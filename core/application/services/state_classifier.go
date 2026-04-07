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
	// working/waiting with no open tool calls → ready.
	//
	// This checks LastWasUserInterrupt (the "[Request interrupted by user"
	// text marker), NOT LastToolResultWasError. The latter fires on benign
	// tool failures (grep miss, build exit ≠ 0, find on protected dirs) —
	// using it here produced ~84 spurious flickers across the issue #102
	// fixtures. See Bug B in issue #102.
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

// InferSubagents detects in-process sub-agent activity from open Agent tool
// calls. Claude Code Explore/Plan agents run inside the parent process and
// don't create separate transcripts, so open Agent tool calls are the only
// detection path. Returns nil if no Agent tools are open.
func InferSubagents(metrics *session.SessionMetrics) *session.SubagentSummary {
	if metrics == nil || !metrics.HasOpenToolCall {
		return nil
	}
	agentCount := 0
	for _, name := range metrics.LastOpenToolNames {
		if name == "Agent" {
			agentCount++
		}
	}
	if agentCount == 0 {
		return nil
	}
	return &session.SubagentSummary{
		Total:   agentCount,
		Working: agentCount,
	}
}
