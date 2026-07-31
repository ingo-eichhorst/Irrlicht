// Package lifecycle defines the unified event types for recording and
// replaying the full session lifecycle — not just transcript-derived state
// transitions but also process lifecycle, filesystem events, debounce
// behavior, and parent-child linking.
package lifecycle

import "time"

// Kind enumerates all recordable lifecycle signals.
type Kind string

const (
	// Transcript file events (from fswatcher → AgentWatcher).
	KindTranscriptNew      Kind = "transcript_new"
	KindTranscriptActivity Kind = "transcript_activity"
	KindTranscriptRemoved  Kind = "transcript_removed"

	// Process lifecycle events.
	KindPIDDiscovered  Kind = "pid_discovered"
	KindProcessSpawned Kind = "process_spawned"
	KindProcessExited  Kind = "process_exited"

	// File-system events on the agent's working directory. Debounced.
	// Reserved by .specs/onboard-agent/07-10-recorder-fidelity.md (WS08);
	// emission is wired by a follow-up PR.
	KindFileEvent Kind = "file_event"

	// State machine transitions (output of ClassifyState).
	KindStateTransition Kind = "state_transition"

	// Parent-child linkage.
	KindParentLinked Kind = "parent_linked"

	// Debounce: records coalescing for faithful replay.
	KindDebounceCoalesced Kind = "debounce_coalesced"
	// Terminal event bypassed the debounce window entirely.
	KindDebounceTerminal Kind = "debounce_terminal"

	// Agent hooks (future: issue #108).
	KindHookReceived Kind = "hook_received"

	// Pre-session lifecycle (process scanner detections).
	KindPreSessionCreated Kind = "presession_created"
	KindPreSessionRemoved Kind = "presession_removed"

	// Task list deltas: one per TaskDelta the tailer folds into a session's
	// task list (TaskCreate/TaskUpdate/assign_id). Makes task-list behavior an
	// assertable observable in onboarding fixtures.
	KindTaskDelta Kind = "task_delta"

	// Terminal-backend read-back (issue #732, Phase 3 of #724). KindUIDetected
	// records a transcript-invisible UI state read off the rendered terminal
	// (today: the trust/permission dialog) — the read counterpart to the
	// backchannel write path. KindTerminalFrame is reserved for raw frame
	// capture (pipe-pane + a screen-buffer parser) and is not emitted yet.
	KindUIDetected    Kind = "ui_detected"
	KindTerminalFrame Kind = "terminal_frame"

	// Cache-creation regression (issue #374). Emitted once per
	// (project, regressing_version) pair within a daemon process lifetime when
	// the detector first finds a working session's median cache-creation per
	// turn exceeding the project's p25 baseline × threshold. The named
	// consumer is the ir:agent-releases workflow.
	KindCacheBloatDetected Kind = "cache_bloat_detected"
)

// Event is a single recorded lifecycle signal. The Kind field discriminates
// which optional fields are populated. All events carry a monotonic sequence
// number and wall-clock timestamp for ordering.
type Event struct {
	// Ordering and timing.
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"ts"`
	Kind      Kind      `json:"kind"`

	// Session identity.
	SessionID string `json:"session_id"`
	Adapter   string `json:"adapter,omitempty"`

	// Transcript events.
	TranscriptPath string `json:"transcript_path,omitempty"`
	FileSize       int64  `json:"file_size,omitempty"`
	ProjectDir     string `json:"project_dir,omitempty"`
	CWD            string `json:"cwd,omitempty"`

	// Process lifecycle.
	PID int `json:"pid,omitempty"`

	// State transitions (recorded as output for validation during replay).
	PrevState string `json:"prev_state,omitempty"`
	NewState  string `json:"new_state,omitempty"`
	Reason    string `json:"reason,omitempty"`

	// Inputs is the classifier-input snapshot that drove this transition
	// (KindStateTransition only). Pointer + omitempty: absent on every event
	// that isn't a transition, so existing sidecars stay compact. Lets an
	// agent reconstruct *why* the classifier chose NewState (issue #757).
	Inputs *ClassifierInputs `json:"inputs,omitempty"`

	// Parent-child.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	// Debounce.
	CoalescedCount int `json:"coalesced_count,omitempty"`

	// Hooks (future: issue #108).
	HookName string `json:"hook_name,omitempty"`
	HookData string `json:"hook_data,omitempty"`

	// File-system events (KindFileEvent). Reserved by WS08 — emission pending.
	Path   string `json:"path,omitempty"`
	FileOp string `json:"file_op,omitempty"` // create | write | remove | rename

	// Task list deltas (KindTaskDelta).
	TaskOp      string `json:"task_op,omitempty"`      // create | update | assign_id
	TaskID      string `json:"task_id,omitempty"`      // post-fold task id (authoritative once assigned)
	TaskSubject string `json:"task_subject,omitempty"` // create only
	TaskStatus  string `json:"task_status,omitempty"`  // pending | in_progress | completed

	// Terminal-backend read-back (KindUIDetected). The UI state read off the
	// rendered terminal screen, e.g. "trust_dialog". Empty on the clearing edge.
	UIKind string `json:"ui_kind,omitempty"`

	// Cache-creation regression (KindCacheBloatDetected, issue #374).
	Project           string  `json:"project,omitempty"`
	RegressingVersion string  `json:"regressing_version,omitempty"`
	PriorVersion      string  `json:"prior_version,omitempty"`
	DeltaTokens       int64   `json:"delta_tokens,omitempty"`
	BaselineMedian    float64 `json:"baseline_median,omitempty"`
	CurrentMedian     float64 `json:"current_median,omitempty"`
}

// ClassifierInputs is a snapshot of the transient SessionMetrics signals that
// feed ClassifyState, attached to KindStateTransition events so a recorded
// trace explains its own classification decisions at replay time (issue #757).
// Every field is omitempty and mirrors the same-named SessionMetrics field;
// the values are copied, never re-derived here. Captured on the transition edge
// only (not on every activity event) since these are the inputs that decided
// the new state — the headline use is reconstructing why an antigravity PID=0
// ghost was classified ready before it was reaped.
type ClassifierInputs struct {
	HasLiveBackgroundProcess          bool     `json:"has_live_background_process,omitempty"`
	PermissionPending                 bool     `json:"permission_pending,omitempty"`
	CompactInProgress                 bool     `json:"compact_in_progress,omitempty"`
	OpenToolStalled                   bool     `json:"open_tool_stalled,omitempty"`
	SawUserBlockingToolClosedThisPass bool     `json:"saw_user_blocking_tool_closed_this_pass,omitempty"`
	SawManualCompactBoundary          bool     `json:"saw_manual_compact_boundary,omitempty"`
	NoSubstantiveActivity             bool     `json:"no_substantive_activity,omitempty"`
	HasOpenToolCall                   bool     `json:"has_open_tool_call,omitempty"`
	LastOpenToolNames                 []string `json:"last_open_tool_names,omitempty"`
	LastEventType                     string   `json:"last_event_type,omitempty"`
	LastWasUserInterrupt              bool     `json:"last_was_user_interrupt,omitempty"`
	LastWasToolDenial                 bool     `json:"last_was_tool_denial,omitempty"`

	// HookTurnDone and IdlePromptPending are the two hook-delivered signals
	// (#1161, #1173) that were missing from this snapshot: a trace could show
	// that a session went ready but not whether a Stop hook said so or a
	// transcript-tail heuristic guessed it — the exact question a tiered state
	// layer has to be able to answer about its own history (#1288).
	HookTurnDone      bool `json:"hook_turn_done,omitempty"`
	IdlePromptPending bool `json:"idle_prompt_pending,omitempty"`

	// DecidedByTier is the authority tier of the evidence that decided this
	// transition ("hook", "transcript", …) and DecidedByRule the id of the
	// rule that claimed it. Together they are the provenance half of #1288:
	// two transitions with identical Reason prose ("agent finished turn →
	// ready") are no longer indistinguishable when one came from a Stop hook
	// and the other from a guess at the transcript tail.
	//
	// Empty on synthetic transitions, which are emitted by the collapse/
	// catch-up synthesizers rather than decided by the ladder — an absent
	// tier means "not a classifier verdict", never "unknown tier".
	DecidedByTier string `json:"decided_by_tier,omitempty"`
	DecidedByRule string `json:"decided_by_rule,omitempty"`
}
