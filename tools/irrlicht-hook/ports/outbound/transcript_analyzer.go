package outbound

import (
	"irrlicht/hook/domain/metrics"
	"time"
)

// TranscriptAnalyzer defines the outbound port for transcript analysis
type TranscriptAnalyzer interface {
	// AnalyzeTranscript analyzes a transcript file and returns metrics
	AnalyzeTranscript(transcriptPath string) (*metrics.AnalysisResult, error)
	
	// ComputeSessionMetrics computes session metrics from transcript
	ComputeSessionMetrics(transcriptPath string, existingMetrics *metrics.SessionMetrics) (*metrics.SessionMetrics, error)
	
	// GetTranscriptSize returns the size of a transcript file
	GetTranscriptSize(transcriptPath string) (int64, error)
	
	// IsTranscriptValid checks if a transcript file is valid and readable
	IsTranscriptValid(transcriptPath string) bool
	
	// GetLastModified returns the last modification time of a transcript
	GetLastModified(transcriptPath string) (time.Time, error)
}

// TranscriptProcessor defines low-level transcript processing operations
type TranscriptProcessor interface {
	// ProcessTranscriptFile processes a transcript file line by line
	ProcessTranscriptFile(transcriptPath string, processor LineProcessor) error
	
	// ExtractModelInfo extracts model information from transcript
	ExtractModelInfo(transcriptPath string) (*ModelInfo, error)
	
	// CalculateTokenCounts calculates token counts from transcript content
	CalculateTokenCounts(transcriptPath string) (*TokenCounts, error)
	
	// GetSessionTimings extracts timing information from transcript
	GetSessionTimings(transcriptPath string) (*SessionTimings, error)
}

// LineProcessor defines the interface for processing transcript lines
type LineProcessor interface {
	// ProcessLine processes a single line from the transcript
	ProcessLine(lineNumber int, line string) error
	
	// GetResult returns the processing result
	GetResult() interface{}
}

// ModelInfo holds information about the model used in a session
type ModelInfo struct {
	Name            string `json:"name"`
	NormalizedName  string `json:"normalized_name"`
	Version         string `json:"version,omitempty"`
	ContextWindow   int64  `json:"context_window,omitempty"`
	TokenLimit      int64  `json:"token_limit,omitempty"`
}

// TokenCounts holds token counting information
type TokenCounts struct {
	TotalTokens     int64 `json:"total_tokens"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	MessageCount    int64 `json:"message_count"`
}

// SessionTimings holds timing information for a session
type SessionTimings struct {
	StartTime       *time.Time `json:"start_time,omitempty"`
	EndTime         *time.Time `json:"end_time,omitempty"`
	LastActivity    *time.Time `json:"last_activity,omitempty"`
	ElapsedSeconds  int64      `json:"elapsed_seconds"`
	ActiveDuration  int64      `json:"active_duration"`
}

// TranscriptWatcher defines the interface for watching transcript changes
type TranscriptWatcher interface {
	// WatchTranscript watches a transcript file for changes
	WatchTranscript(transcriptPath string, changes chan<- TranscriptChange) error
	
	// StopWatching stops watching transcript changes
	StopWatching(transcriptPath string) error
	
	// StopAllWatching stops watching all transcripts
	StopAllWatching() error
}

// TranscriptChange represents a change to a transcript file
type TranscriptChange struct {
	Path         string    `json:"path"`
	ChangeType   ChangeType `json:"change_type"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// ChangeType represents the type of change to a transcript
type ChangeType string

const (
	TranscriptCreated  ChangeType = "created"
	TranscriptModified ChangeType = "modified"
	TranscriptDeleted  ChangeType = "deleted"
	TranscriptMoved    ChangeType = "moved"
)

// TranscriptCache defines the interface for caching transcript analysis results
type TranscriptCache interface {
	// GetCachedMetrics retrieves cached metrics for a transcript
	GetCachedMetrics(transcriptPath string, lastModified time.Time) (*metrics.SessionMetrics, bool)
	
	// SetCachedMetrics stores metrics in cache
	SetCachedMetrics(transcriptPath string, lastModified time.Time, metrics *metrics.SessionMetrics) error
	
	// InvalidateCache invalidates cached data for a transcript
	InvalidateCache(transcriptPath string) error
	
	// ClearCache clears all cached data
	ClearCache() error
}

// TranscriptValidator defines validation for transcript operations
type TranscriptValidator interface {
	// ValidateTranscriptPath validates a transcript file path
	ValidateTranscriptPath(path string) error
	
	// ValidateTranscriptContent validates transcript file content
	ValidateTranscriptContent(path string) error
	
	// SanitizeTranscriptPath sanitizes a transcript path
	SanitizeTranscriptPath(path string) string
}

// TranscriptConfig holds configuration for transcript analysis
type TranscriptConfig struct {
	MaxFileSize      int64         `json:"max_file_size"`
	CacheEnabled     bool          `json:"cache_enabled"`
	CacheTTL         time.Duration `json:"cache_ttl"`
	EnableWatching   bool          `json:"enable_watching"`
	ProcessingTimeout time.Duration `json:"processing_timeout"`
}

// DefaultTranscriptConfig returns default transcript configuration
func DefaultTranscriptConfig() *TranscriptConfig {
	return &TranscriptConfig{
		MaxFileSize:       100 * 1024 * 1024, // 100MB
		CacheEnabled:      true,
		CacheTTL:          time.Hour,
		EnableWatching:    true,
		ProcessingTimeout: 30 * time.Second,
	}
}