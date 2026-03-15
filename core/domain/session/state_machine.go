package session

// TransitionResult holds the outcome of a state transition calculation.
type TransitionResult struct {
	NewState           string
	NewCompactionState string
	Reason             string
}

// mapEventToState maps hook event names to session states (simple, context-free mapping).
func mapEventToState(eventName string) string {
	switch eventName {
	case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact":
		return StateWorking
	case "Notification":
		return StateWaiting
	case "Stop", "SubagentStop", "SessionEnd":
		return StateReady
	default:
		return StateWorking
	}
}

// SmartStateTransition determines the new session state based on the event and previous state.
//
// Parameters:
//   - eventName: the hook_event_name field
//   - matcher: the matcher field (e.g. "compact", "resume", "startup")
//   - source: the source field (e.g. "clear")
//   - reason: the reason field (used for SessionEnd sub-reasons)
//   - previousState: the previous session state, or nil for brand-new sessions
//   - transcriptActivityDetected: true if the transcript grew since entering waiting state
//
// This is a pure function with no I/O dependencies.
func SmartStateTransition(
	eventName, matcher, source, reason string,
	previousState *SessionState,
	transcriptActivityDetected bool,
) TransitionResult {
	result := TransitionResult{
		NewState:           mapEventToState(eventName),
		NewCompactionState: CompactionStateNotCompacting,
		Reason:             "simple_mapping",
	}
	if previousState != nil && previousState.CompactionState != "" {
		result.NewCompactionState = previousState.CompactionState
	}

	// Transcript activity overrides everything: session resumed from waiting.
	if transcriptActivityDetected {
		result.NewState = StateWorking
		result.Reason = "transcript_activity_detected"
		return result
	}

	switch eventName {
	case "UserPromptSubmit":
		if previousState != nil && previousState.State == StateWaiting {
			result.NewState = StateWorking
			result.Reason = "user_response_after_notification"
		}
		if previousState != nil && previousState.CompactionState == CompactionStatePostCompact {
			result.NewCompactionState = CompactionStateNotCompacting
			result.Reason = "user_prompt_after_compaction"
		}

	case "SessionStart":
		if source == "clear" {
			result.NewState = StateReady
			result.NewCompactionState = CompactionStateNotCompacting
			result.Reason = "session_after_clear"
		} else {
			switch matcher {
			case "compact":
				result.NewState = StateWorking
				result.NewCompactionState = CompactionStatePostCompact
				result.Reason = "session_resumed_after_compaction"
			case "resume":
				result.NewState = StateWorking
				result.NewCompactionState = CompactionStateNotCompacting
				result.Reason = "session_resumed"
			case "startup":
				result.NewState = StateReady
				result.NewCompactionState = CompactionStateNotCompacting
				result.Reason = "session_startup"
			default:
				result.NewState = StateReady
				result.NewCompactionState = CompactionStateNotCompacting
				result.Reason = "session_start_new"
			}
		}

	case "PreCompact":
		result.NewState = StateWorking
		result.NewCompactionState = CompactionStateCompacting
		switch matcher {
		case "auto":
			result.Reason = "auto_compaction_starting"
		case "manual":
			result.Reason = "manual_compaction_starting"
		default:
			result.Reason = "compaction_starting_unknown_trigger"
		}

	case "Notification":
		result.NewState = StateWaiting
		result.Reason = "notification_requires_user_attention"

	case "PreToolUse":
		if previousState != nil && previousState.State == StateWaiting {
			result.NewState = StateWorking
			result.Reason = "tool_use_after_notification"
		}

	case "PostToolUse", "Stop", "SubagentStop":
		result.Reason = "standard_event_mapping"

	case "SessionEnd":
		switch reason {
		case "prompt_input_exit":
			result.NewState = StateCancelledByUser
			result.Reason = "user_cancelled_notification_with_esc"
		case "clear":
			result.NewState = StateDeleteSession
			result.Reason = "session_cleared_delete_file"
		case "logout":
			result.NewState = StateDeleteSession
			result.Reason = "session_logout_delete_file"
		default:
			result.NewState = StateDeleteSession
			result.Reason = "session_ended_delete_file"
		}
	}

	return result
}
