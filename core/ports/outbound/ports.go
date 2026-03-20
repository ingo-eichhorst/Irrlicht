package outbound

import (
	"context"

	"irrlicht/core/domain/gastown"
	"irrlicht/core/domain/session"
)

// PushMessage is a typed WebSocket envelope for session and gastown state fan-out.
type PushMessage struct {
	Type    string                `json:"type"`
	Session *session.SessionState `json:"session,omitempty"`
	GasTown *gastown.State        `json:"gastown,omitempty"`
}

// Valid PushMessage type constants.
const (
	PushTypeCreated       = "session_created"
	PushTypeUpdated       = "session_updated"
	PushTypeDeleted       = "session_deleted"
	PushTypeGasTownState  = "gastown_state"
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

// ProcessWatcher monitors process PIDs via kqueue EVFILT_PROC NOTE_EXIT and
// invokes a callback when a watched process exits.
type ProcessWatcher interface {
	// Watch registers a PID for exit monitoring associated with a sessionID.
	// If the process is already dead, the exit handler fires immediately.
	Watch(pid int, sessionID string) error
	// Unwatch stops monitoring the given PID.
	Unwatch(pid int)
	// Run starts the kqueue event loop. Blocks until ctx is cancelled.
	Run(ctx context.Context) error
	// Close releases kqueue resources.
	Close() error
}

// GracePeriodTimer manages per-session idle timers. When a session has no
// transcript activity for a grace period and no open tool calls, it fires
// a callback to transition the session to waiting.
type GracePeriodTimer interface {
	// Reset restarts the grace period timer for a session. Called on
	// each transcript activity event. transcriptPath is needed to
	// compute metrics when the timer fires.
	Reset(sessionID string, transcriptPath string)
	// Stop cancels the timer for a session. Called when a session ends.
	Stop(sessionID string)
	// StopAll cancels all active timers.
	StopAll()
}

