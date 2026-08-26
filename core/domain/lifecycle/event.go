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
	// KindProcessDiedMidTurn is the OTHER outcome of a process exit: the pid
	// went away while a turn was still open, so #1800 converted the session to
	// `error` and KEPT the row instead of deleting it.
	//
	// It is a separate Kind rather than a flag on KindProcessExited because in
	// this codebase that Kind means the session went AWAY. Six sites read it,
	// and they do not all delete — listing them because the short version
	// ("every consumer deletes on it") is false for three:
	//
	//	internal/replay/state_machine.go  delete the row
	//	cmd/replay/ghosts.go              the ghost's removal edge
	//	cmd/replay/replay_sidecar.go      reset the lifetime to ready
	//	core/adapters/outbound/filesystem/concurrency_tracker.go  end the span
	//	internal/viewer/web/playbackTimeline.js  aliveUntil
	//	tools/linux-parity-check.sh       the "removed" token
	//
	// What they share is that none of them expects the row to survive, and
	// every process_exited event in the committed corpus was written by the
	// teardown path. Overloading the Kind would silently reinterpret all of
	// them; #1817 measured that as 16 goldens fabricating a `working→error`
	// the recording's own daemon never produced. THIS COMMENT IS THAT FIGURE'S
	// ONE HOME — cite it from elsewhere rather than restating it. The
	// population, with the command that measures it, since the scope is
	// load-bearing (it counts sidecar files, not every match in the tree):
	//
	//	find replaydata -name events.jsonl | xargs grep -h '"process_exited"' | wc -l   # 317
	//
	// Only three of those six can be guarded at compile time (the Go switches);
	// the JS and shell copies compare raw strings and must be updated by hand.
	//
	// Recorded on the CONVERSION edge only, not on every retained sweep tick —
	// see SessionDetector.retainAsProcessDeath, whose verdict registry is what
	// makes that edge one-shot. Carries the pid and the exit reason; the row it
	// describes survives, so nothing may treat this as a deletion.
	KindProcessDiedMidTurn Kind = "process_died_midturn"

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

	// Held-signal ceiling expiry (issue #1360). Emitted when an out-of-band
	// signal hold is dropped because its wall-clock ceiling elapsed rather
	// than because its own staleness rule ended it or a Release retired it —
	// which for a TierHook hold means a release that should have arrived
	// never did. Rare by construction, and the one event that explains a
	// session silently ceasing to be pinned at waiting.
	KindHoldExpired Kind = "hold_expired"

	// Hook-liveness watchdog (issue #1368). Three kinds, kept apart because
	// they are three different facts and collapsing any two of them would
	// re-create the ambiguity the watchdog exists to remove.
	//
	// KindHookChannelSilent: an adapter whose hook consent is granted and whose
	// install reported success completed N turns without its receiver being
	// handed a single request. The channel is treated as dead and the adapter
	// falls back to TierTranscript. Distinct from the two neighbouring
	// diagnoses that look identical from the outside: an install whose effect
	// FAILED never wrote entries at all and is reported as a permission
	// effect_error (#1362), and entries that were written and later went
	// missing are #1372's subject. This event says entries were written, the
	// daemon believes they are there, and nothing is coming.
	KindHookChannelSilent Kind = "hook_channel_silent"

	// KindHookChannelRecovered: a receipt arrived for an adapter previously
	// declared silent. The demotion is a health signal, not a latch — this is
	// the event that proves it, and its absence after a silent event is what
	// says the channel never came back.
	KindHookChannelRecovered Kind = "hook_channel_recovered"

	// KindHookHoldReleased: one TierHook hold dropped because the watchdog
	// declared its channel silent. Deliberately NOT KindHoldExpired: that one
	// means a single hold outlived its wall-clock ceiling (#1360), which is a
	// statement about elapsed time on one session. This one means the channel
	// that placed the hold stopped delivering, which is a statement about the
	// adapter. Same visible outcome, opposite investigation.
	KindHookHoldReleased Kind = "hook_hold_released"
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

	// Held-signal ceiling expiry (KindHoldExpired, issue #1360). HeldForMS is
	// how long the hold actually stood; CeilingMS is the bound it exceeded.
	// Both are carried so a recorded trace stays interpretable after a ceiling
	// is retuned — the elapsed time alone cannot say whether it was overdue.
	SignalKind string `json:"signal_kind,omitempty"`
	SignalTier string `json:"signal_tier,omitempty"`
	HeldForMS  int64  `json:"held_for_ms,omitempty"`
	CeilingMS  int64  `json:"ceiling_ms,omitempty"`

	// Hook-liveness watchdog (KindHookChannelSilent / KindHookChannelRecovered,
	// issue #1368). SilentTurns is how many consecutive completed turns the
	// adapter produced with no hook receipt; TurnThreshold is the bound that
	// was crossed. Both are recorded for the reason CeilingMS is: the threshold
	// is tunable, so an elapsed count alone cannot say whether a trace from an
	// older daemon was overdue by the rules that daemon was running.
	// HookReceipts is the adapter's lifetime receipt total at the moment of the
	// verdict — zero says the channel never worked, non-zero says it worked and
	// stopped, and those have different first suspects.
	SilentTurns   int    `json:"silent_turns,omitempty"`
	TurnThreshold int    `json:"turn_threshold,omitempty"`
	HookReceipts  uint64 `json:"hook_receipts,omitempty"`

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

	// SessionErrorClass and SessionErrorPhase are the #1798 failure, reduced
	// to the two facts a recorded trace needs: what kind of failure it was,
	// and whether the agent was still retrying or had given up.
	//
	// The Reason string on an error transition is the fixed "session error →
	// error", because reason prose is pinned byte-for-byte by the replay
	// goldens and cannot carry per-session detail. So this is where a
	// transition becomes debuggable: without it a recording shows a session
	// going red and gives no way to tell a rate-limit retry storm from
	// rejected credentials — which is precisely the question anyone opening
	// the recording is there to answer.
	//
	// The human message is deliberately NOT copied here. It is unbounded,
	// attacker-influenced (it is provider text), and already carried on the
	// session's own metrics; duplicating it into every transition event would
	// bloat each sidecar for no extra diagnostic power.
	//
	// An empty Phase means the agent reported a failure without saying
	// whether another attempt follows — a real, recorded case (copilot's
	// errorType "query"), not a gap in the plumbing.
	SessionErrorClass string `json:"session_error_class,omitempty"`
	SessionErrorPhase string `json:"session_error_phase,omitempty"`

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
