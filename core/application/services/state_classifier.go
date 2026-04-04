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

	// 2. Agent finished turn → ready.
	if metrics.IsAgentDone() {
		if currentState == session.StateWorking || currentState == session.StateWaiting {
			return session.StateReady, "agent finished turn → ready"
		}
		return currentState, ""
	}

	// 3. ESC cancellation: user event with is_error=true while working/waiting
	// with no open tool calls → ready.
	if (currentState == session.StateWorking || currentState == session.StateWaiting) &&
		!metrics.HasOpenToolCall && metrics.LastEventType == "user" && metrics.LastToolResultWasError {
		return session.StateReady,
			fmt.Sprintf("rejected tool result while %s → ready (cancellation)", currentState)
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
