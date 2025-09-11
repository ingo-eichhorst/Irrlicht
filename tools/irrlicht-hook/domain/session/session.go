package session

import (
	"time"
)

// Session represents the core session entity with its state and metadata
type Session struct {
	ID               string            `json:"session_id"`
	State            State             `json:"state"`
	CompactionState  CompactionState   `json:"compaction_state,omitempty"`
	Model            string            `json:"model,omitempty"`
	CWD              string            `json:"cwd,omitempty"`
	TranscriptPath   string            `json:"transcript_path,omitempty"`
	GitBranch        string            `json:"git_branch,omitempty"`
	ProjectName      string            `json:"project_name,omitempty"`
	FirstSeen        int64             `json:"first_seen"`
	UpdatedAt        int64             `json:"updated_at"`
	Confidence       string            `json:"confidence"`
	EventCount       int               `json:"event_count"`
	LastEvent        string            `json:"last_event"`
	LastMatcher      string            `json:"last_matcher,omitempty"`
	Metrics          *Metrics          `json:"metrics,omitempty"`
	
	// Transcript monitoring for waiting state recovery
	LastTranscriptSize int64  `json:"last_transcript_size,omitempty"`
	WaitingStartTime   *int64 `json:"waiting_start_time,omitempty"`
	
	// Processing state for incremental token counting
	ProcessingState *ProcessingState `json:"processing_state,omitempty"`
}

// ProcessingState tracks the transcript processing state for incremental token counting
type ProcessingState struct {
	LastProcessedOffset int64     `json:"last_processed_offset"`
	CumulativeTokens    int64     `json:"cumulative_tokens"`
	LastProcessedAt     time.Time `json:"last_processed_at"`
	TranscriptChecksum  string    `json:"transcript_checksum,omitempty"`
}

// Metrics holds computed performance metrics from transcript analysis  
type Metrics struct {
	ElapsedSeconds       int64   `json:"elapsed_seconds"`
	TotalTokens          int64   `json:"total_tokens"`
	ModelName            string  `json:"model_name"`
	ContextUtilization   float64 `json:"context_utilization_percentage"`
	PressureLevel        string  `json:"pressure_level"`
}

// NewSession creates a new session with the provided ID and initial state
func NewSession(id string, initialState State) *Session {
	now := time.Now().Unix()
	return &Session{
		ID:          id,
		State:       initialState,
		FirstSeen:   now,
		UpdatedAt:   now,
		Confidence:  "high",
		EventCount:  1,
		CompactionState: NotCompacting,
	}
}

// Update updates the session with new information from an event
func (s *Session) Update(newState State, compactionState CompactionState, eventName string, matcher string) {
	s.State = newState
	s.CompactionState = compactionState
	s.UpdatedAt = time.Now().Unix()
	s.EventCount++
	s.LastEvent = eventName
	if matcher != "" {
		s.LastMatcher = matcher
	}
}

// UpdateMetadata updates session metadata (paths, model, etc.)
func (s *Session) UpdateMetadata(transcriptPath, cwd, model, gitBranch, projectName string) {
	if transcriptPath != "" {
		s.TranscriptPath = transcriptPath
	}
	if cwd != "" {
		s.CWD = cwd
	}
	if model != "" {
		s.Model = model
	}
	if gitBranch != "" {
		s.GitBranch = gitBranch
	}
	if projectName != "" {
		s.ProjectName = projectName
	}
}

// SetMetrics updates the session with computed performance metrics
func (s *Session) SetMetrics(metrics *Metrics) {
	s.Metrics = metrics
}

// StartWaiting marks the session as entering waiting state with transcript monitoring
func (s *Session) StartWaiting(transcriptSize int64) {
	now := time.Now().Unix()
	s.WaitingStartTime = &now
	s.LastTranscriptSize = transcriptSize
}

// StopWaiting clears the waiting state monitoring
func (s *Session) StopWaiting() {
	s.WaitingStartTime = nil
	s.LastTranscriptSize = 0
}

// IsWaiting returns true if the session is in waiting state with monitoring active
func (s *Session) IsWaiting() bool {
	return s.State == Waiting && s.WaitingStartTime != nil
}

// ToLegacySessionState converts the domain Session to the legacy SessionState format
// This allows gradual migration while maintaining compatibility
func (s *Session) ToLegacySessionState() *LegacySessionState {
	// Convert metrics if present
	var legacyMetrics *LegacySessionMetrics
	if s.Metrics != nil {
		legacyMetrics = &LegacySessionMetrics{
			ElapsedSeconds:       s.Metrics.ElapsedSeconds,
			TotalTokens:          s.Metrics.TotalTokens,
			ModelName:            s.Metrics.ModelName,
			ContextUtilization:   s.Metrics.ContextUtilization,
			PressureLevel:        s.Metrics.PressureLevel,
		}
	}

	return &LegacySessionState{
		Version:            1,
		SessionID:          s.ID,
		State:              string(s.State),
		CompactionState:    string(s.CompactionState),
		Model:              s.Model,
		CWD:                s.CWD,
		TranscriptPath:     s.TranscriptPath,
		GitBranch:          s.GitBranch,
		ProjectName:        s.ProjectName,
		FirstSeen:          s.FirstSeen,
		UpdatedAt:          s.UpdatedAt,
		Confidence:         s.Confidence,
		EventCount:         s.EventCount,
		LastEvent:          s.LastEvent,
		LastMatcher:        s.LastMatcher,
		Metrics:            legacyMetrics,
		LastTranscriptSize: s.LastTranscriptSize,
		WaitingStartTime:   s.WaitingStartTime,
		ProcessingState:    s.ProcessingState,
	}
}

// LegacySessionState represents the current SessionState struct for compatibility
type LegacySessionState struct {
	Version          int                    `json:"version"`
	SessionID        string                 `json:"session_id"`
	State            string                 `json:"state"`
	CompactionState  string                 `json:"compaction_state,omitempty"`
	Model            string                 `json:"model,omitempty"`
	CWD              string                 `json:"cwd,omitempty"`
	TranscriptPath   string                 `json:"transcript_path,omitempty"`
	GitBranch        string                 `json:"git_branch,omitempty"`
	ProjectName      string                 `json:"project_name,omitempty"`
	FirstSeen        int64                  `json:"first_seen"`
	UpdatedAt        int64                  `json:"updated_at"`
	Confidence       string                 `json:"confidence"`
	EventCount       int                    `json:"event_count"`
	LastEvent        string                 `json:"last_event"`
	LastMatcher      string                 `json:"last_matcher,omitempty"`
	Metrics          *LegacySessionMetrics  `json:"metrics,omitempty"`
	
	LastTranscriptSize int64             `json:"last_transcript_size,omitempty"`
	WaitingStartTime   *int64            `json:"waiting_start_time,omitempty"`
	ProcessingState    *ProcessingState  `json:"processing_state,omitempty"`
}

// LegacySessionMetrics represents the current SessionMetrics struct for compatibility
type LegacySessionMetrics struct {
	ElapsedSeconds       int64   `json:"elapsed_seconds"`
	TotalTokens          int64   `json:"total_tokens"`
	ModelName            string  `json:"model_name"`
	ContextUtilization   float64 `json:"context_utilization_percentage"`
	PressureLevel        string  `json:"pressure_level"`
}

// FromLegacySessionState converts a legacy SessionState to the new domain Session
func FromLegacySessionState(legacy *LegacySessionState) *Session {
	// Convert metrics if present
	var metrics *Metrics
	if legacy.Metrics != nil {
		metrics = &Metrics{
			ElapsedSeconds:       legacy.Metrics.ElapsedSeconds,
			TotalTokens:          legacy.Metrics.TotalTokens,
			ModelName:            legacy.Metrics.ModelName,
			ContextUtilization:   legacy.Metrics.ContextUtilization,
			PressureLevel:        legacy.Metrics.PressureLevel,
		}
	}

	return &Session{
		ID:                 legacy.SessionID,
		State:              State(legacy.State),
		CompactionState:    CompactionState(legacy.CompactionState),
		Model:              legacy.Model,
		CWD:                legacy.CWD,
		TranscriptPath:     legacy.TranscriptPath,
		GitBranch:          legacy.GitBranch,
		ProjectName:        legacy.ProjectName,
		FirstSeen:          legacy.FirstSeen,
		UpdatedAt:          legacy.UpdatedAt,
		Confidence:         legacy.Confidence,
		EventCount:         legacy.EventCount,
		LastEvent:          legacy.LastEvent,
		LastMatcher:        legacy.LastMatcher,
		Metrics:            metrics,
		LastTranscriptSize: legacy.LastTranscriptSize,
		WaitingStartTime:   legacy.WaitingStartTime,
		ProcessingState:    legacy.ProcessingState,
	}
}