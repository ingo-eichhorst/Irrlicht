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

	// OpenSubagents is the number of in-process child agents currently running.
	// Populated by the adapter (e.g. claudecode counts open Agent tool calls)
	// and merged with file-based children via ComputeSubagentSummary. The
	// domain model is agnostic to how each adapter represents subagents.
	OpenSubagents int `json:"open_subagents,omitempty"`

	// LastEventType is the type of the most recent transcript event
	// (e.g. "assistant", "user", "tool_use", "tool_result").
	LastEventType string `json:"last_event_type,omitempty"`

	// LastOpenToolNames holds tool names from the most recent assistant
	// message that called tools. Used to detect user-blocking tools.
	LastOpenToolNames []string `json:"last_open_tool_names,omitempty"`

	// LastWasUserInterrupt is true when the most recent user event was a
	// real ESC cancellation (the exact "[Request interrupted by user]" text
	// marker, without the "for tool use" suffix). Used by the classifier
	// to distinguish ESC from normal tool failures and tool denials.
	LastWasUserInterrupt bool `json:"last_was_user_interrupt,omitempty"`

	// LastWasToolDenial is true when the most recent user event was a tool
	// permission denial ("[Request interrupted by user for tool use]"
	// marker). Distinct from LastWasUserInterrupt because a denial does
	// NOT end the agent's turn — the cancellation rule must not fire on
	// it. Carried for observability and replay-harness flicker analysis.
	LastWasToolDenial bool `json:"last_was_tool_denial,omitempty"`

	// EstimatedCostUSD is the estimated session cost in USD, computed from
	// cumulative token totals and per-model pricing.
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`

	// Cumulative token totals across all API turns (for cost breakdown).
	CumInputTokens         int64 `json:"cum_input_tokens,omitempty"`
	CumOutputTokens        int64 `json:"cum_output_tokens,omitempty"`
	CumCacheReadTokens     int64 `json:"cum_cache_read_tokens,omitempty"`
	CumCacheCreationTokens int64 `json:"cum_cache_creation_tokens,omitempty"`

	// LastCWD is the most recent working directory extracted from the
	// transcript during metrics parsing. Used to avoid a separate file read.
	LastCWD string `json:"-"` // transient — not persisted in session JSON

	// LastAssistantText is the text content of the most recent assistant
	// message, truncated to ~200 characters. Used to surface the question
	// or request when the session is in the waiting state.
	LastAssistantText string `json:"last_assistant_text,omitempty"`

	// PermissionMode is the session's permission mode from the JSONL
	// (e.g. "default", "plan", "bypassPermissions"). Surfaced by the tailer
	// and carried on session state for UI/telemetry.
	PermissionMode string `json:"permission_mode,omitempty"`

	// PermissionPending is true when a PermissionRequest hook has fired and no
	// corresponding PostToolUse/PostToolUseFailure has cleared it. Transient —
	// set by the hook receiver in processActivity, not derived from transcript.
	PermissionPending bool `json:"-"`
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

// IsWaitingForUserInput returns true when the agent finished its turn but the
// last assistant message ends with a question mark — indicating the agent is
// waiting for user input even though no user-blocking tool is open.
func (m *SessionMetrics) IsWaitingForUserInput() bool {
	if m == nil || m.LastAssistantText == "" {
		return false
	}
	// LastAssistantText is already trimmed by the tailer.
	return m.LastAssistantText[len(m.LastAssistantText)-1] == '?'
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
	if m.LastEventType == "turn_done" {
		return true
	}
	// Fallback: Claude Code pre-stop_hook transcripts lack turn_duration.
	// Claude Code's "assistant" event is safe because HasOpenToolCall is
	// checked first — mid-turn tool calls block this, and streaming chunks
	// use "assistant_streaming" which isn't matched.
	//
	// Codex is NOT in this fallback: codex agents routinely emit a
	// preliminary `assistant_message` BEFORE calling a tool, so matching it
	// here would flip the session ready→working→ready on every turn. Codex
	// must rely on the `turn_done` primary path (emitted from task_complete).
	switch m.LastEventType {
	case "assistant", "assistant_output":
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
		ElapsedSeconds:       newM.ElapsedSeconds,
		TotalTokens:          newM.TotalTokens,
		ModelName:            newM.ModelName,
		ContextWindow:        newM.ContextWindow,
		ContextUtilization:   newM.ContextUtilization,
		PressureLevel:        newM.PressureLevel,
		HasOpenToolCall:      newM.HasOpenToolCall,
		OpenToolCallCount:    newM.OpenToolCallCount,
		OpenSubagents:        newM.OpenSubagents,
		LastEventType:        newM.LastEventType,
		LastOpenToolNames:    newM.LastOpenToolNames,
		LastWasUserInterrupt: newM.LastWasUserInterrupt,
		LastWasToolDenial:    newM.LastWasToolDenial,
		EstimatedCostUSD:     newM.EstimatedCostUSD,
		LastAssistantText:    newM.LastAssistantText,
		PermissionMode:       newM.PermissionMode,
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
	if merged.PermissionMode == "" && oldM.PermissionMode != "" {
		merged.PermissionMode = oldM.PermissionMode
	}
	return merged
}
