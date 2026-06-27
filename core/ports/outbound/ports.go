package outbound

import (
	"context"
	"errors"
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

	// Input carries the payload for a PushTypeInputRequested message — the
	// daemon asking the macOS app to inject text/interrupt into a session
	// hosted by an AppleScript-only backend (iTerm2/Terminal.app), which the
	// daemon cannot script directly (no Automation TCC grant). nil for every
	// other message type.
	Input *InputRequest `json:"input,omitempty"`

	// Seq is a daemon-global monotonic sequence number stamped by
	// pushService.Broadcast before fan-out — every subscriber sees the same
	// Seq for a given message, so a gap in received Seq tells a client it
	// missed a drop (slow-subscriber skip) and should re-hydrate (#593).
	// 0 = unstamped: connect-time hub snapshots, or an older daemon.
	Seq uint64 `json:"seq,omitempty"`

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

// InputRequest is the payload of a PushTypeInputRequested message: the action
// to perform on the session's terminal and (for input) the bytes to inject.
type InputRequest struct {
	Action string `json:"action"`         // "input" | "interrupt"
	Data   string `json:"data,omitempty"` // bytes to inject for the input action
}

// InputAction values for InputRequest.Action.
const (
	InputActionInput     = "input"
	InputActionInterrupt = "interrupt"
)

// Valid PushMessage type constants.
const (
	PushTypeCreated        = "session_created"
	PushTypeUpdated        = "session_updated"
	PushTypeDeleted        = "session_deleted"
	PushTypeFocusRequested = "focus_requested"
	// PushTypeInputRequested asks the macOS app to inject input/interrupt
	// into a session whose terminal backend the daemon can't script directly
	// (iTerm2/Terminal.app via AppleScript). Carries Session + Input.
	PushTypeInputRequested = "input_requested"

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
	// GetHeadCommit returns the full SHA of the current HEAD commit for the
	// given directory, or "" if it is not inside a git repository (#373).
	GetHeadCommit(dir string) string
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
	// IngestTaskSummary attaches an out-of-band task-summary marker to the
	// session associated with transcriptPath (#738). Used by the Claude Code
	// hook receiver for the one-line summary carried in a PreToolUse tool
	// input (e.g. a Bash description), which bypasses the dropped-text-block
	// path. observedAt is the marker's unix-seconds timestamp. Same
	// no-op-without-tailer semantics as IngestRateLimit.
	IngestTaskSummary(transcriptPath, text string, observedAt int64)
	// PurgeDeadBackgroundProcs drops the session's tracked background
	// processes whose output path is in outputs, after the detector's
	// liveness probe verdicts them dead — they died without a
	// transcript-observable termination, and the ledger would otherwise
	// resurrect them on every restart (#649). Scoped to the probed outputs
	// so a process spawned after the probe's snapshot survives. Same
	// no-op-without-tailer semantics as IngestRateLimit.
	PurgeDeadBackgroundProcs(transcriptPath string, outputs []string)
	// PurgeDeadBackgroundPIDs is the PID-keyed counterpart for adapters that
	// track a backgrounded command by its PID rather than an output file
	// (Gemini CLI). The detector calls it when the PID-liveness probe verdicts
	// them dead — Gemini writes no transcript termination, so the ledger would
	// otherwise resurrect them as phantom open processes on every restart.
	// Scoped to the probed PIDs; same no-op-without-tailer semantics. See #661.
	PurgeDeadBackgroundPIDs(transcriptPath string, pids []string)
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

// SeriesQuery parameterizes CostTracker.CostSeries (issue #750). It carries the
// window + bucket width, which group axis to key the matrix by, which metric to
// measure, and an optional scope filter (only rows whose ScopeField equals
// ScopeValue are counted — the drilldown primitive). Group "project" preserves
// Phase 1 behavior (the filename is the fallback key); the other axes read the
// matching snapshot column, with an empty value bucketed under "" (the unknown
// key, surfaced or dropped by the handler's ≥10% rule).
type SeriesQuery struct {
	Start         int64
	End           int64
	BucketSeconds int64
	Group         string // project|branch|provider|model|session
	Metric        string // cost|tokens ("" defaults to cost)
	ScopeField    string // "" = unscoped; else project|branch|provider|model|session
	ScopeValue    string
}

// TokenSplit holds the window's aggregate token throughput broken down by kind,
// populated only when SeriesQuery.Metric is "tokens" (nil otherwise). It drives
// the History tab's tokens side panel (in/out/cache).
type TokenSplit struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cache  float64 `json:"cache"` // cache-read + cache-creation tokens
}

// CostSeriesResult is the downsampled incremental time series returned by
// CostTracker.CostSeries. BucketStarts holds each bucket's start timestamp
// (unix seconds); ByKey maps a group key (project/branch/provider/model/session
// per the query) to its per-bucket increment — USD for the cost metric, token
// counts for the tokens metric (each slice has len == len(BucketStarts));
// Totals is each key's sum over [Start, End). TokenSplit is set only for the
// tokens metric.
type CostSeriesResult struct {
	Start         int64                `json:"start"`
	End           int64                `json:"end"`
	BucketSeconds int64                `json:"bucket_seconds"`
	BucketStarts  []int64              `json:"bucket_starts"`
	ByKey         map[string][]float64 `json:"by_key"`
	Totals        map[string]float64   `json:"totals"`
	TokenSplit    *TokenSplit          `json:"token_split,omitempty"`
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

	// CostSeries returns a downsampled incremental time series over the query's
	// [Start, End) window, bucketed into BucketSeconds-wide buckets and keyed by
	// the requested group axis, plus per-key totals. Each interval's increment
	// is attributed to the group value active at the interval's end row, so a
	// session that changes branch/model/provider mid-flight splits cleanly. One
	// pass over every cost file, reusing the same baseline/delta model as
	// CostsInWindows so a cost series' sum matches the corresponding
	// trailing-window total.
	CostSeries(q SeriesQuery) (*CostSeriesResult, error)

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
	// ArgvOf returns the argument vector of pid (argv[0] is the executable
	// as invoked). Returns nil, nil when the argv is unreadable (e.g. a
	// hardened-runtime process that strips it) — an empty argv is not an
	// error, the same way EnvOf treats an unreadable env.
	ArgvOf(pid int) ([]string, error)
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

// AgentController writes back to a discovered, externally-launched agent
// session through whatever terminal backend owns its pty (tmux, kitty,
// iTerm2/Terminal.app, …). It is the write counterpart to the read-only
// observation seams: the daemon never owns the agent process, it scripts the
// backend that does, keyed off the session's already-captured session.Launcher
// (issue #724, the "backchannel"). The concrete implementation lives in
// adapters/outbound/control. Consent and the backchannel master-toggle are
// enforced upstream by InputService — implementations just inject.
type AgentController interface {
	// SendInput injects data into the session's terminal as if typed.
	// Returns a non-nil error when the backend command fails; callers that
	// want a "not controllable" verdict consult Controllable first.
	SendInput(sessionID string, data []byte) error
	// SendCommand injects a command and submits it. Unlike SendInput, the
	// submit sequence is owned by the backend: tmux/kitty get a trailing CR
	// appended; AppleScript hosts auto-submit the bare command. Used for
	// preset actions whose command text is agent-declared (issue #754).
	SendCommand(sessionID string, command string) error
	// Interrupt delivers an interrupt (e.g. Ctrl-C) to the session.
	Interrupt(sessionID string) error
	// Controllable reports whether the session has a usable backend target
	// for a supported terminal backend. It does not consider consent or the
	// master-toggle — those are InputService's concern.
	Controllable(sessionID string) bool
}

// TerminalReader reads the rendered terminal screen of a discovered agent
// session back from whatever multiplexer/kitty backend owns its pty — the read
// counterpart to AgentController (issue #732, Phase 3 of #724). It surfaces
// signals that are structurally absent from the transcript (today: the
// interactive trust/permission dialog) without replacing the transcript/process
// observers.
//
// Snapshot-only and multiplexer/kitty-only: tmux capture-pane and kitty get-text
// render the screen for us, so the daemon needs no terminal emulator; plain
// iTerm2/Terminal.app have no daemon-reachable read path and report
// not-readable. Consent and the backchannel master-toggle are enforced upstream
// by TerminalObserver — implementations just capture.
type TerminalReader interface {
	// CaptureScreen returns the session's rendered terminal screen. Returns a
	// non-nil error wrapping ErrNotReadable when no readable backend hosts the
	// session, so callers can skip such sessions with errors.Is — it does not
	// consider consent or the master-toggle.
	CaptureScreen(sessionID string) ([]byte, error)
}

// ErrNotReadable is returned (wrapped) by TerminalReader.CaptureScreen when the
// session has no backend that supports read-back — read-back is
// multiplexer/kitty-only, so plain iTerm2/Terminal.app sessions are not
// readable (issue #732).
var ErrNotReadable = errors.New("session terminal is not readable")
