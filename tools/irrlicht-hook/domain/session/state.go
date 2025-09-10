package session

import (
	"fmt"
)

// State represents the current state of a session
type State string

const (
	Working State = "working"
	Waiting State = "waiting"
	Ready   State = "ready"
)

// String returns the string representation of the state
func (s State) String() string {
	return string(s)
}

// IsValid checks if the state is a valid session state
func (s State) IsValid() bool {
	switch s {
	case Working, Waiting, Ready:
		return true
	default:
		return false
	}
}

// CompactionState represents the current compaction state of a session
type CompactionState string

const (
	NotCompacting CompactionState = "not_compacting"
	Compacting    CompactionState = "compacting"
	PostCompact   CompactionState = "post_compact"
)

// String returns the string representation of the compaction state
func (cs CompactionState) String() string {
	return string(cs)
}

// IsValid checks if the compaction state is valid
func (cs CompactionState) IsValid() bool {
	switch cs {
	case NotCompacting, Compacting, PostCompact:
		return true
	default:
		return false
	}
}

// StateTransitionResult holds the result of a state transition decision
type StateTransitionResult struct {
	NewState           State
	NewCompactionState CompactionState
	Reason             string
}

// StateTransitioner handles state transition logic
type StateTransitioner struct{}

// NewStateTransitioner creates a new state transitioner
func NewStateTransitioner() *StateTransitioner {
	return &StateTransitioner{}
}

// DetermineStateTransition determines the new state based on event and current state
// This encapsulates the complex state transition logic from smartStateTransition
func (st *StateTransitioner) DetermineStateTransition(
	eventName, matcher, source string,
	previousSession *Session,
	transcriptPath string,
) StateTransitionResult {

	// Start with simple mapping
	newState := st.mapEventToState(eventName)
	newCompactionState := NotCompacting
	reason := "simple_mapping"

	// Preserve existing compaction state if available
	if previousSession != nil && previousSession.CompactionState != "" {
		newCompactionState = previousSession.CompactionState
	}

	// Note: Transcript activity detection is now handled by the application layer
	// This keeps the domain layer pure without file system dependencies

	// Handle specific event types with complex logic
	switch eventName {
	case "SessionStart":
		return st.handleSessionStart(source, matcher, reason, newCompactionState)

	case "PreCompact":
		newState = Working
		newCompactionState = Compacting
		reason = "compaction_started"

	case "PostCompact":
		newState = Working
		newCompactionState = PostCompact
		reason = "compaction_completed"

	case "UserPromptSubmit":
		// Reset compaction state on new user input
		if newCompactionState == PostCompact {
			newCompactionState = NotCompacting
			reason = "compaction_state_reset_on_user_input"
		}
	}

	return StateTransitionResult{
		NewState:           newState,
		NewCompactionState: newCompactionState,
		Reason:             reason,
	}
}

// mapEventToState provides simple event-to-state mapping
func (st *StateTransitioner) mapEventToState(eventName string) State {
	switch eventName {
	case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact":
		return Working
	case "Notification":
		return Waiting
	case "Stop", "SubagentStop", "SessionEnd":
		return Ready
	default:
		return Working // Default fallback
	}
}

// handleSessionStart handles the complex SessionStart state logic
func (st *StateTransitioner) handleSessionStart(
	source, matcher, fallbackReason string,
	newCompactionState CompactionState,
) StateTransitionResult {

	// Check event.Source field for clear events
	if source == "clear" {
		return StateTransitionResult{
			NewState:           Ready,
			NewCompactionState: NotCompacting,
			Reason:             "session_after_clear",
		}
	}

	// Handle matcher-based SessionStart events
	switch matcher {
	case "resume":
		return StateTransitionResult{
			NewState:           Working,
			NewCompactionState: NotCompacting,
			Reason:             "session_resumed",
		}
	case "startup":
		return StateTransitionResult{
			NewState:           Ready,
			NewCompactionState: NotCompacting,
			Reason:             "session_startup",
		}
	default:
		// Regular SessionStart without specific matcher - treat as new session
		return StateTransitionResult{
			NewState:           Ready,
			NewCompactionState: NotCompacting,
			Reason:             "session_start_new",
		}
	}
}

// hasTranscriptGrown is now implemented in the application layer via FileSystemService
// This method is deprecated and will be removed

// ShouldDeleteSession determines if a session should be deleted based on the event
func ShouldDeleteSession(eventName string) bool {
	return eventName == "SessionEnd"
}

// GetTranscriptSize is now implemented via FileSystemService in the application layer
// This function is deprecated and will be removed

// StateTransitionLogger can be used to log state transitions for debugging
type StateTransitionLogger interface {
	LogStateTransition(sessionID, eventName, oldState, newState, reason string)
}

// LogTransition logs a state transition if a logger is provided
func LogTransition(logger StateTransitionLogger, sessionID, eventName string,
	oldState, newState State, reason string) {
	if logger != nil {
		logger.LogStateTransition(sessionID, eventName, string(oldState), string(newState), reason)
	}
}

// ValidateStateTransition ensures state transitions are valid
func ValidateStateTransition(from, to State) error {
	if !from.IsValid() {
		return fmt.Errorf("invalid source state: %s", from)
	}
	if !to.IsValid() {
		return fmt.Errorf("invalid target state: %s", to)
	}
	// Additional validation rules can be added here
	return nil
}
