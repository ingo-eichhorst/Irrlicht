package outbound

import "irrlicht/core/domain/session"

// SessionRepository loads, saves, and deletes session state files.
type SessionRepository interface {
	Load(sessionID string) (*session.SessionState, error)
	Save(state *session.SessionState) error
	Delete(sessionID string) error
	ListAll() ([]*session.SessionState, error)
}

// Logger provides structured, levelled logging.
type Logger interface {
	LogInfo(eventType, sessionID, message string)
	LogError(eventType, sessionID, errorMsg string)
	LogProcessingTime(eventType, sessionID string, processingTimeMs int64, payloadSize int, result string)
	Close() error
}

// GitResolver resolves git metadata from a working directory.
type GitResolver interface {
	GetBranch(dir string) string
	GetProjectName(dir string) string
	GetBranchFromTranscript(transcriptPath string) string
}

// MetricsCollector computes session metrics from a transcript file.
type MetricsCollector interface {
	ComputeMetrics(transcriptPath string) (*session.SessionMetrics, error)
}

// PathValidator validates that a file-system path is safe to use.
type PathValidator interface {
	Validate(path string) error
}
