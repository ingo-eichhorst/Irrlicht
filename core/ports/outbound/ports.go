package outbound

import (
	"context"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
)

// PushMessage is a typed WebSocket envelope for session state fan-out.
// Session-state messages populate Session; history messages use SessionID +
// the History/Granularity/Buckets/Priority fields (see PushTypeHistory*).
type PushMessage struct {
	Type    string                `json:"type"`
	Session *session.SessionState `json:"session,omitempty"`

	// History-message fields. SessionID identifies the target row for
	// snapshot/upgrade messages; tick messages use the per-session entries
	// in Buckets instead. History maps granularity ("1"/"10"/"60") → 20-char
	// base64 of 60 bit-packed buckets.
	SessionID      string            `json:"session_id,omitempty"`
	History        map[string]string `json:"history,omitempty"`
	GranularitySec int               `json:"granularity_sec,omitempty"`
	Buckets        map[string]int8   `json:"buckets,omitempty"`
	Priority       *int8             `json:"priority,omitempty"`

	// Tick generations let the client dedupe a tick that's already
	// reflected in its snapshot. Captured under the session lock together
	// with the bucket state, so a snapshot's Generations always match the
	// History it ships, and a tick's BucketGenerations always match the
	// post-roll state. Keys: granularity for snapshots ("1"/"10"/"60"),
	// session_id for ticks (parallel to Buckets).
	Generations       map[string]uint64 `json:"generations,omitempty"`
	BucketGenerations map[string]uint64 `json:"bucket_generations,omitempty"`
}

// Valid PushMessage type constants.
const (
	PushTypeCreated        = "session_created"
	PushTypeUpdated        = "session_updated"
	PushTypeDeleted        = "session_deleted"
	PushTypeFocusRequested = "focus_requested"

	// PushTypeHistorySnapshot delivers the bit-packed 60-bucket history
	// for one session across all three granularities. Sent on WebSocket
	// connect, on session creation, and after a client reconnects.
	PushTypeHistorySnapshot = "history_snapshot"
	// PushTypeHistoryTick is a bulk per-granularity delta: one entry per
	// session with the priority of the bucket that just rolled. Emitted
	// once per granularity-second by the daemon.
	PushTypeHistoryTick = "history_tick"
	// PushTypeHistoryUpgrade fires on a state transition mid-bucket. The
	// client merges the priority into the current bucket of all three
	// rings using max-priority aggregation.
	PushTypeHistoryUpgrade = "history_upgrade"

	// PushTypePermissionsUpdated signals that consent state changed
	// (agent detected, answer applied, or new permission declared). The
	// envelope carries no payload: clients re-fetch GET /api/v1/permissions
	// and show or dismiss the wizard accordingly — which is also how the
	// surface that answered second dismisses live (issue #570).
	PushTypePermissionsUpdated = "permissions_updated"
)

// PermissionStore persists the user's per-agent consent answers for the
// permission wizard (issue #570).
type PermissionStore interface {
	// Load returns the persisted set; a missing file yields an empty set
	// (every declared permission pending).
	Load() (permission.Set, error)
	Save(permission.Set) error
}

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
	// GetGitRoot returns the absolute path of the git repo root for the given
	// directory, or "" if the directory is not inside a git repository.
	GetGitRoot(dir string) string
	GetBranchFromTranscript(transcriptPath string) string
	// GetCWDFromTranscript extracts the working directory from a transcript
	// file by scanning the first few lines for a "cwd" field.
	GetCWDFromTranscript(transcriptPath string) string
}

// MetricsCollector computes session metrics from a transcript file.
// The adapter parameter identifies the transcript format (e.g. "claude-code",
// "codex", "pi") so the correct parser is used.
type MetricsCollector interface {
	ComputeMetrics(transcriptPath, adapter string) (*session.SessionMetrics, error)
	// ComputeMetricsTimeline returns cumulative metrics snapshots (ascending by
	// VirtualTime), one per transcript turn, so a replay viewer can animate
	// cost/tokens across the playhead. Returns nil for transcripts that can't
	// be accumulated turn-by-turn (absent/empty, or provider-backed adapters);
	// callers fall back to ComputeMetrics.
	ComputeMetricsTimeline(transcriptPath, adapter string) ([]session.MetricsTimelinePoint, error)
	// PruneEntry releases per-session state — both the in-memory tailer
	// cache and the on-disk ledger file — when a session ends. Idempotent
	// on a missing or already-removed entry.
	PruneEntry(transcriptPath string)
	// IngestRateLimit attaches a subscription-quota snapshot to the session
	// associated with transcriptPath, when a tailer already exists for it.
	// Used by the Claude Code statusline hook, which delivers rate-limit
	// data out-of-band from the transcript stream. No-op when the tailer
	// hasn't been created yet — the next ComputeMetrics will create one
	// and the next statusline tick will populate it.
	IngestRateLimit(transcriptPath string, snap *session.RateLimitSnapshot)
	// IngestTaskEstimate attaches an out-of-band task-progress estimate to
	// the session associated with transcriptPath (#604). Used by the Claude
	// Code hook receiver for markers carried in PreToolUse tool inputs —
	// they bypass the transcript writer, which drops mid-task text blocks
	// on claude ≥2.1.162. Same no-op-without-tailer semantics as
	// IngestRateLimit.
	IngestTaskEstimate(transcriptPath string, est *session.TaskEstimate)
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

// EventRecorder captures lifecycle events for offline replay.
// Implementations must be safe for concurrent use.
type EventRecorder interface {
	Record(ev lifecycle.Event)
	Close() error
}

// CostTracker persists per-session cost/token snapshots so clients can query
// project-level cost totals over a trailing time window (last day/week/…).
// Implementations must be safe for concurrent use.
type CostTracker interface {
	// RecordSnapshot appends a snapshot row for the session if either
	// estimated cost or any cumulative token count has changed since the
	// last stored row for that session, and at least a minimum debounce
	// interval has elapsed. Implementations may no-op when state lacks
	// metrics or a project name.
	RecordSnapshot(state *session.SessionState) error

	// CostsInWindows returns per-timeframe cost maps in a single pass over
	// each cost file. The returned map keys mirror the caller-supplied
	// windowSeconds keys. byProject keys each inner map by projectName;
	// byProvider keys by billing provider ("anthropic"/"openai"), excluding
	// rows with no known provider. Both axes come from one scan.
	CostsInWindows(windowSeconds map[string]int64) (byProject, byProvider map[string]map[string]float64, err error)

	// Prune drops snapshot rows older than the given number of days.
	// Safe to call periodically (e.g. daemon startup).
	Prune(olderThanDays int) error
}

// HistoryTracker maintains per-session rolling state buffers for three
// granularities (1s, 10s, 60s), using priority aggregation waiting>working>ready.
// Implementations must be safe for concurrent use.
type HistoryTracker interface {
	// OnTransition records a state transition for a session, upgrading the
	// current bucket's priority if the new state outranks the stored one.
	OnTransition(sessionID, newState string, ts time.Time)
	// Remove drops all buffers for a session.
	Remove(sessionID string)
	// EmitSnapshot ships the current bit-packed history for one session
	// through the configured emit callback. Used to hydrate newly-created
	// sessions on the WebSocket without waiting for the next tick.
	EmitSnapshot(sessionID string)
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

// ProcessObserver concentrates the OS coupling of process observation behind
// one seam. Agent adapters declare *what* to observe (a process name, a
// command-line pattern, a transcript path); the observer fulfils *how* per
// platform — pgrep/lsof/sysctl on darwin, /proc on linux. A new OS is "add
// one file that implements this interface," with no change to adapters or to
// the discovery/scanner orchestration that routes every OS primitive through
// it. The concrete implementation is selected at compile time by build tag.
type ProcessObserver interface {
	// FindByName returns the PIDs of processes whose executable base name
	// exactly matches name. Returns nil, nil when none match.
	FindByName(name string) ([]int, error)
	// FindByCmdline returns the PIDs whose full command line matches the
	// given pattern. Returns nil, nil when none match.
	FindByCmdline(pattern string) ([]int, error)
	// CWDOf returns the working directory of pid.
	CWDOf(pid int) (string, error)
	// WriterOf returns the PID that currently has path open for writing, or
	// 0 when no process is writing it. A missing/unopened file is not an
	// error — it returns 0, nil.
	WriterOf(path string) (int, error)
	// EnvOf returns the whitelisted launcher env vars of pid. Returns an
	// empty/nil map (not an error) when the env is unreadable.
	EnvOf(pid int) (map[string]string, error)
}
