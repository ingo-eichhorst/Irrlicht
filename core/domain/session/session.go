package session

// State constants
const (
	StateWorking         = "working"
	StateWaiting         = "waiting"
	StateReady           = "ready"
	StateCancelledByUser = "cancelled_by_user"
	StateDeleteSession   = "delete_session"

	CompactionStateNotCompacting = "not_compacting"
	CompactionStateCompacting    = "compacting"
	CompactionStatePostCompact   = "post_compact"
)

// SessionMetrics holds computed performance metrics from transcript analysis.
type SessionMetrics struct {
	ElapsedSeconds     int64   `json:"elapsed_seconds"`
	TotalTokens        int64   `json:"total_tokens"`
	ModelName          string  `json:"model_name"`
	ContextUtilization float64 `json:"context_utilization_percentage"`
	PressureLevel      string  `json:"pressure_level"`

	// Tool call tracking — count unmatched tool_use/tool_result pairs.
	HasOpenToolCall   bool `json:"has_open_tool_call"`
	OpenToolCallCount int  `json:"open_tool_call_count,omitempty"`
}

// SessionState represents the current state of a Claude Code or Copilot session.
type SessionState struct {
	Version         int             `json:"version"`
	SessionID       string          `json:"session_id"`
	State           string          `json:"state"`
	// Adapter identifies the source adapter (e.g. "copilot").
	// Empty means the default Claude Code adapter.
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

	// Transcript monitoring for waiting-state recovery.
	LastTranscriptSize int64  `json:"last_transcript_size,omitempty"`
	WaitingStartTime   *int64 `json:"waiting_start_time,omitempty"`
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
		ContextUtilization: newM.ContextUtilization,
		PressureLevel:      newM.PressureLevel,
		HasOpenToolCall:    newM.HasOpenToolCall,
		OpenToolCallCount:  newM.OpenToolCallCount,
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
	return merged
}
