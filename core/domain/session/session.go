package session

import (
	"time"
)

// State constants — three MECE states for session lifecycle.
// See STATES.md for the formal state machine specification.
const (
	StateWorking = "working" // Agent actively processing (tools, text generation, hooks, compaction)
	StateWaiting = "waiting" // Agent finished turn, waiting for user input
	StateReady   = "ready"   // Session inactive (process exited, transcript removed, cancelled)

	CompactionStateNotCompacting = "not_compacting"
	CompactionStateCompacting    = "compacting"
	CompactionStatePostCompact   = "post_compact"
)

// SessionMetrics holds computed performance metrics from transcript analysis.
type SessionMetrics struct {
	ElapsedSeconds     int64   `json:"elapsed_seconds"`
	TotalTokens        int64   `json:"total_tokens"`
	ModelName          string  `json:"model_name"`
	ContextWindow      int64   `json:"context_window,omitempty"`
	ContextUtilization float64 `json:"context_utilization_percentage"`
	PressureLevel      string  `json:"pressure_level"`

	// Tool call tracking — count unmatched tool_use/tool_result pairs.
	HasOpenToolCall   bool `json:"has_open_tool_call"`
	OpenToolCallCount int  `json:"open_tool_call_count,omitempty"`

	// LastEventType is the type of the most recent transcript event
	// (e.g. "assistant", "user", "tool_use", "tool_result").
	LastEventType string `json:"last_event_type,omitempty"`

	// LastOpenToolNames holds tool names from the most recent assistant
	// message that called tools. Used to detect user-blocking tools.
	LastOpenToolNames []string `json:"last_open_tool_names,omitempty"`

	// LastToolResultWasError is true when the most recent tool_result had
	// is_error=true (user rejection via ESC). Distinguishes cancellation
	// from normal tool completion.
	LastToolResultWasError bool `json:"last_tool_result_was_error"`

	// EstimatedCostUSD is the estimated session cost in USD, computed from
	// token breakdown and per-model pricing.
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
}

// NeedsUserAttention returns true when a user-blocking tool is open — one
// that always requires user input regardless of permission settings.
// Most tools auto-execute (Bash, Read, Write, Agent, MCP, etc.) and should
// NOT trigger a waiting state; only explicit user-interaction tools do.
func (m *SessionMetrics) NeedsUserAttention() bool {
	if m == nil || !m.HasOpenToolCall {
		return false
	}
	for _, name := range m.LastOpenToolNames {
		if isUserBlockingTool(name) {
			return true
		}
	}
	return false
}

// isUserBlockingTool returns true for tools that always block for user input,
// regardless of permission settings. These are the only tools that should
// trigger the "waiting" state.
func isUserBlockingTool(name string) bool {
	return name == "AskUserQuestion" || name == "ExitPlanMode"
}

// IsAgentDone returns true when the agent finished its turn. The primary
// signal is Claude Code's "turn_duration" system event which fires exactly
// once at the end of each turn. Legacy formats (Codex) fall back to the
// heuristic of "last event is assistant and no open tool calls".
//
// Open tool calls (e.g. the Agent tool waiting for a sub-agent) override
// turn_done: the turn isn't truly complete until all tool results arrive.
func (m *SessionMetrics) IsAgentDone() bool {
	if m == nil {
		return false
	}
	// Open tool calls mean the agent is still processing — a sub-agent
	// spawned via the Agent tool fires turn_done before the tool result
	// comes back, but the session is NOT idle.
	if m.HasOpenToolCall {
		return false
	}
	// Primary: Claude Code writes a system/turn_duration event at end of turn.
	// local_command fires after /clear and is also a "done" signal.
	if m.LastEventType == "turn_done" || m.LastEventType == "local_command" {
		return true
	}
	// Fallback for legacy/Codex transcripts.
	switch m.LastEventType {
	case "assistant_message", "assistant_output":
		return true
	}
	return false
}

// SubagentSummary tracks the aggregate state of all child sessions.
type SubagentSummary struct {
	Total   int `json:"total"`
	Working int `json:"working"`
	Waiting int `json:"waiting"`
	Ready   int `json:"ready"`
}

// SessionState represents the current state of a Claude Code or Copilot session.
type SessionState struct {
	Version         int             `json:"version"`
	SessionID       string          `json:"session_id"`
	State           string          `json:"state"`
	// Adapter identifies the source agent (e.g. "claude-code", "codex").
	// Empty means Claude Code (for backwards compatibility).
	Adapter string `json:"adapter,omitempty"`
	CompactionState string          `json:"compaction_state,omitempty"`
	Model           string          `json:"model,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	TranscriptPath  string          `json:"transcript_path,omitempty"`
	GitBranch       string          `json:"git_branch,omitempty"`
	ProjectName     string          `json:"project_name,omitempty"`
	FirstSeen       int64           `json:"first_seen"`
	UpdatedAt       int64           `json:"updated_at"`
	Confidence      string          `json:"confidence"`
	EventCount      int             `json:"event_count"`
	LastEvent       string          `json:"last_event"`
	LastMatcher     string          `json:"last_matcher,omitempty"`
	Metrics         *SessionMetrics `json:"metrics,omitempty"`

	// PID of the Claude Code process that owns this session (set on SessionStart).
	PID int `json:"pid,omitempty"`

	// ParentSessionID links a subagent session to its spawning parent session.
	// Derived from file path or heuristic matching in SessionDetector.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	// Subagents holds the aggregate state of all child sessions.
	// Nil when this session has no children.
	Subagents *SubagentSummary `json:"subagents,omitempty"`

	// DaemonVersion records which irrlichd version created this session,
	// enabling future data migrations when the schema evolves.
	DaemonVersion string `json:"daemon_version,omitempty"`

	// Transcript monitoring for waiting-state recovery.
	LastTranscriptSize int64  `json:"last_transcript_size,omitempty"`
	WaitingStartTime   *int64 `json:"waiting_start_time,omitempty"`
}

// IsStale reports whether the session's last update is older than maxAge.
// A zero or negative maxAge disables the check (always returns false).
func (s *SessionState) IsStale(maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	return time.Since(time.Unix(s.UpdatedAt, 0)) > maxAge
}

// StringState returns a display-friendly state string including compaction state.
func (s *SessionState) StringState() string {
	if s.CompactionState != "" && s.CompactionState != CompactionStateNotCompacting {
		return s.State + "(" + s.CompactionState + ")"
	}
	return s.State
}

// MergeMetrics merges new metrics with old, preserving old values when new are zero/empty.
func MergeMetrics(newM, oldM *SessionMetrics) *SessionMetrics {
	if newM == nil {
		return oldM
	}
	if oldM == nil {
		return newM
	}
	merged := &SessionMetrics{
		ElapsedSeconds:     newM.ElapsedSeconds,
		TotalTokens:        newM.TotalTokens,
		ModelName:          newM.ModelName,
		ContextWindow:      newM.ContextWindow,
		ContextUtilization: newM.ContextUtilization,
		PressureLevel:      newM.PressureLevel,
		HasOpenToolCall:    newM.HasOpenToolCall,
		OpenToolCallCount:  newM.OpenToolCallCount,
		LastEventType:          newM.LastEventType,
		LastOpenToolNames:      newM.LastOpenToolNames,
		LastToolResultWasError: newM.LastToolResultWasError,
		EstimatedCostUSD:       newM.EstimatedCostUSD,
	}
	if merged.ContextWindow == 0 && oldM.ContextWindow > 0 {
		merged.ContextWindow = oldM.ContextWindow
	}
	if merged.ElapsedSeconds == 0 && oldM.ElapsedSeconds > 0 {
		merged.ElapsedSeconds = oldM.ElapsedSeconds
	}
	if merged.TotalTokens == 0 && oldM.TotalTokens > 0 {
		merged.TotalTokens = oldM.TotalTokens
	}
	if (merged.ModelName == "" || merged.ModelName == "unknown") && oldM.ModelName != "" && oldM.ModelName != "unknown" {
		merged.ModelName = oldM.ModelName
	}
	if merged.ContextUtilization == 0 && oldM.ContextUtilization > 0 {
		merged.ContextUtilization = oldM.ContextUtilization
	}
	if (merged.PressureLevel == "" || merged.PressureLevel == "unknown") && oldM.PressureLevel != "" && oldM.PressureLevel != "unknown" {
		merged.PressureLevel = oldM.PressureLevel
	}
	if merged.EstimatedCostUSD == 0 && oldM.EstimatedCostUSD > 0 {
		merged.EstimatedCostUSD = oldM.EstimatedCostUSD
	}
	return merged
}
