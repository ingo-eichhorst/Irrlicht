package outbound

import (
	"context"

	"irrlicht/core/domain/gastown"
	"irrlicht/core/domain/session"
	"irrlicht/core/domain/transcript"
)

// PushMessage is a typed WebSocket envelope for session state fan-out.
type PushMessage struct {
	Type    string                `json:"type"`
	Session *session.SessionState `json:"session"`
}

// Valid PushMessage type constants.
const (
	PushTypeCreated = "session_created"
	PushTypeUpdated = "session_updated"
	PushTypeDeleted = "session_deleted"
)

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

// PushBroadcaster fans out session state changes to subscribers (e.g. WebSocket clients).
type PushBroadcaster interface {
	Broadcast(msg PushMessage)
	Subscribe() chan PushMessage
	Unsubscribe(ch chan PushMessage)
}

// GTBinResolver resolves the path to the gt binary.
type GTBinResolver interface {
	// Path returns the resolved absolute path to the gt binary,
	// or "" if the binary could not be found.
	Path() string
}

// TranscriptWatcher watches ~/.claude/projects/** for transcript file changes,
// emitting events for new sessions, activity, and removals.
type TranscriptWatcher interface {
	// Watch begins watching the Claude projects directory for transcript
	// changes. It blocks until ctx is cancelled or an unrecoverable error
	// occurs. Subdirectories are watched dynamically as they appear.
	Watch(ctx context.Context) error
	// Subscribe returns a channel that receives transcript events whenever
	// a .jsonl file is created, modified, or removed.
	Subscribe() <-chan transcript.TranscriptEvent
	// Unsubscribe removes a previously subscribed channel and closes it.
	Unsubscribe(ch <-chan transcript.TranscriptEvent)
}

// GasTownCollector detects Gas Town presence, resolves GT_ROOT, and watches
// the daemon state file for changes.
type GasTownCollector interface {
	// Detected returns true if a valid Gas Town installation was found.
	Detected() bool
	// Root returns the resolved GT_ROOT path, or "" if not detected.
	Root() string
	// DaemonState returns the latest parsed daemon state, or nil if unavailable.
	DaemonState() *gastown.DaemonState
	// Watch begins watching daemon/state.json for changes. It blocks until
	// ctx is cancelled or an unrecoverable error occurs.
	Watch(ctx context.Context) error
	// Subscribe returns a channel that receives daemon state updates whenever
	// the watched file changes on disk.
	Subscribe() <-chan gastown.DaemonState
	// Unsubscribe removes a previously subscribed channel.
	Unsubscribe(ch <-chan gastown.DaemonState)
}
