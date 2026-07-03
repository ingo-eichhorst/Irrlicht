// metrics.go holds SessionMetrics — the per-pass computed view of a
// session's transcript (tokens, cost, context pressure, open tool calls,
// task list, rate limits) — the turn-completion and tool-blocking logic
// derived from it, and MergeMetrics, which reconciles a fresh tailer pass
// against the previously merged state so a field's zero/empty reading
// ("not observed this pass") doesn't clobber a real value carried from
// before.
package session

import "strings"

// SessionMetrics holds computed performance metrics from transcript analysis.
type SessionMetrics struct {
	ElapsedSeconds int64  `json:"elapsed_seconds"`
	TotalTokens    int64  `json:"total_tokens"`
	ModelName      string `json:"model_name"`

	// AgentVersion is the upstream agent CLI's own version (e.g. Claude Code
	// "2.1.143"), distinct from DaemonVersion (irrlichd's version). Captured
	// from the transcript by adapters that expose it (claudecode, codex,
	// aider); empty for adapters whose transcript omits it. The cache-bloat
	// detector (issue #374) groups a project's completed sessions by this
	// value to attribute a regression to a specific version.
	AgentVersion string `json:"agent_version,omitempty"`

	ContextWindow      int64   `json:"context_window,omitempty"`
	ContextUtilization float64 `json:"context_utilization_percentage"`
	PressureLevel      string  `json:"pressure_level"`

	// ContextWindowUnknown signals that the model has no LiteLLM pricing
	// entry, so ContextWindow is a sentinel fallback (32k) rather than a
	// known value. The UI uses this to render a tentative bar (dashed
	// outline / "~" prefix) instead of suppressing context display
	// entirely. Without this fallback, sessions on local models like
	// `gemma-4-26b-a4b` (aider via LM Studio) had pressure_level="unknown"
	// which the macOS app treated as "no data" and hid the row's context
	// column.
	ContextWindowUnknown bool `json:"context_window_unknown,omitempty"`

	// Tool call tracking — count unmatched tool_use/tool_result pairs.
	HasOpenToolCall   bool `json:"has_open_tool_call"`
	OpenToolCallCount int  `json:"open_tool_call_count,omitempty"`

	// OpenSubagents is the number of in-process child agents currently running.
	// Populated by the adapter (e.g. claudecode counts open Agent tool calls)
	// and merged with file-based children via ComputeSubagentSummary. The
	// domain model is agnostic to how each adapter represents subagents.
	OpenSubagents int `json:"open_subagents,omitempty"`

	// BackgroundProcessCount is the number of agent-spawned background
	// processes the transcript shows as still open — for Claude Code, a
	// `Bash` tool call with `run_in_background: true` that has not yet been
	// observed terminating (via a `BashOutput` status or a `KillShell`).
	// Deterministic from the transcript, so it is stable across replay. It
	// is the agent's *claimed* open count; HasLiveBackgroundProcess is the
	// daemon's authoritative liveness verdict. See issue #445.
	BackgroundProcessCount int `json:"background_process_count,omitempty"`

	// HasLiveBackgroundProcess is the daemon's authoritative answer to "does
	// this session still have a running background process?", set by the
	// liveness probe in processActivity (lsof on each background process's
	// output file). It gates IsAgentDone so a session stays `working` past
	// end_turn while a background process is alive. Transient — set by the
	// detector, never derived from the transcript, so it is absent under
	// replay (where there is no live process to probe). See issue #445.
	HasLiveBackgroundProcess bool `json:"-"`

	// BackgroundProcessOutputs holds the output-file paths of the currently
	// open background processes (Claude Code writes each backgrounded
	// command's stdout/stderr to `tasks/<bash_id>.output`). The liveness
	// probe lsof's these to find a live writer. Transient — recomputed from
	// the transcript each pass, not persisted in session JSON.
	BackgroundProcessOutputs []string `json:"-"`

	// BackgroundProcessPIDs holds the OS PIDs of currently open background
	// processes whose adapter reports a PID rather than an output file (Gemini
	// CLI). The liveness probe signals these directly to decide whether the
	// session is still working. Transient — recomputed from the transcript each
	// pass, not persisted in session JSON. See issue #661.
	BackgroundProcessPIDs []string `json:"-"`

	// LastEventType is the type of the most recent transcript event
	// (e.g. "assistant", "user", "tool_use", "tool_result").
	LastEventType string `json:"last_event_type,omitempty"`

	// LastOpenToolNames holds tool names from the most recent assistant
	// message that called tools. Used to detect user-blocking tools.
	LastOpenToolNames []string `json:"last_open_tool_names,omitempty"`

	// LastWasUserInterrupt is true when the most recent user event was a
	// real ESC cancellation (the exact "[Request interrupted by user]" text
	// marker, without the "for tool use" suffix). Used by the classifier
	// to distinguish ESC from normal tool failures and tool denials.
	LastWasUserInterrupt bool `json:"last_was_user_interrupt,omitempty"`

	// LastWasToolDenial is true when the most recent user event was a tool
	// permission denial ("[Request interrupted by user for tool use]"
	// marker). Distinct from LastWasUserInterrupt because a denial does
	// NOT end the agent's turn — the cancellation rule must not fire on
	// it. Carried for observability and replay-harness flicker analysis.
	LastWasToolDenial bool `json:"last_was_tool_denial,omitempty"`

	// EstimatedCostUSD is the estimated session cost in USD, computed from
	// cumulative token totals and per-model pricing.
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`

	// Cumulative token totals across all API turns (for cost breakdown).
	CumInputTokens         int64 `json:"cum_input_tokens,omitempty"`
	CumOutputTokens        int64 `json:"cum_output_tokens,omitempty"`
	CumCacheReadTokens     int64 `json:"cum_cache_read_tokens,omitempty"`
	CumCacheCreationTokens int64 `json:"cum_cache_creation_tokens,omitempty"`

	// CompletedTurns counts the finished agent turns for this session — each
	// rising edge of IsAgentDone (a turn ending in ready, or in waiting on a
	// question), not just working→waiting. The cache-bloat detector (issue
	// #374) maintains it and uses it both as the per-session denominator for
	// "cache creation per turn" and as a variance guard (the rule does not
	// fire until ≥3 turns). Incremented by the detector on each turn boundary
	// and persisted so the per-project baseline can be computed over completed
	// sessions.
	CompletedTurns int `json:"completed_turns,omitempty"`

	// CacheBloat is true when the cache-creation regression detector (issue
	// #374) has found this working session's median cache-creation per turn
	// exceeding the project's p25 baseline × threshold. Both UIs render it as
	// a "↑" glyph on the row. Set by the detector; never derived from the
	// transcript.
	CacheBloat bool `json:"cache_bloat,omitempty"`

	// CacheBloatTooltip is the human-readable hover text for the CacheBloat
	// glyph, composed daemon-side so both UIs stay dumb. When the project's
	// lookback window contains ≥2 distinct agent versions with a large enough
	// per-version delta, it names the regressing version, e.g.
	// "claude-code 2.1.143 +14K cache tokens vs 2.1.98". Empty when no version
	// attribution is possible (no false attribution).
	CacheBloatTooltip string `json:"cache_bloat_tooltip,omitempty"`

	// LastCWD is the most recent working directory extracted from the
	// transcript during metrics parsing. Used to avoid a separate file read.
	LastCWD string `json:"-"` // transient — not persisted in session JSON

	// LastAssistantText is the text content of the most recent assistant
	// message, truncated to ~200 characters. Used to surface the question
	// or request when the session is in the waiting state.
	LastAssistantText string `json:"last_assistant_text,omitempty"`

	// TaskSummary is a human-readable one-line description of what the
	// session's current task is about (issue #738). Sourced from the agent's
	// in-band irrlicht-summary marker when present, else a daemon-side
	// heuristic (the first user message). Surfaced in both the waiting and
	// ready states so a human can tell what a session was about at a glance.
	// Kept as the full text; the sidebar shows IntentHeadline and uses this as
	// the hover tooltip.
	TaskSummary string `json:"task_summary,omitempty"`

	// IntentHeadline is the terse ~70-char one-line version of TaskSummary,
	// produced by the compaction seam (issue #759). The sidebar renders this in
	// the purple "intent" block; the full TaskSummary is the tooltip. Empty
	// when there is no summary source.
	IntentHeadline string `json:"intent_headline,omitempty"`

	// QuestionHeadline is the terse ~70-char one-line version of the pending
	// question, produced by the compaction seam from the agent's in-band
	// irrlicht-question marker when present, else from LastAssistantText (issue
	// #759). The sidebar renders this in the orange "waiting" block; the full
	// LastAssistantText is the tooltip. Empty when there is no question source.
	QuestionHeadline string `json:"question_headline,omitempty"`

	// PermissionMode is the session's permission mode from the JSONL
	// (e.g. "default", "plan", "bypassPermissions"). Surfaced by the tailer
	// and carried on session state for UI/telemetry.
	PermissionMode string `json:"permission_mode,omitempty"`

	// PermissionPending is true when a PermissionRequest hook has fired and no
	// corresponding PostToolUse/PostToolUseFailure has cleared it. Transient —
	// set by the hook receiver in processActivity, not derived from transcript.
	PermissionPending bool `json:"-"`

	// SawUserBlockingToolClosedThisPass reflects the last tailer pass: true
	// when an AskUserQuestion / ExitPlanMode tool_use and its tool_result
	// were processed together in one pass. Triggers the daemon's synthetic
	// working→waiting emission so observers see the collapsed waiting
	// episode (issue #150). Transient — per-pass, not persisted.
	SawUserBlockingToolClosedThisPass bool `json:"-"`

	// OpenToolStalled is a transient, live-only signal set by the detector
	// when a permission-gated file-edit tool (Edit/Write/MultiEdit/
	// NotebookEdit) has stayed open with no transcript progress long enough
	// that the agent is almost certainly blocked on a permission prompt
	// rather than mid-execution (those tools complete near-instantly). It is
	// the transcript-based fallback for permission detection when the
	// curl-delivered PermissionRequest hook can't reach the daemon (#488).
	// Like HasLiveBackgroundProcess it is wall-clock derived and never set
	// under replay. Read by ClassifyState.
	OpenToolStalled bool `json:"-"`

	// NoSubstantiveActivity reflects the last tailer pass: true when the
	// pass consumed new transcript content but produced no state-relevant
	// change (every line was Skip=true with no SubagentCompletions and no
	// TaskSnapshot). The detector uses this to short-circuit the
	// classification pipeline for post-turn writes like Claude Code's
	// `system/away_summary` recap — without it, the force-bounce in
	// processActivity sees the stale LastEventType and flips a ready
	// session back to working (issue #329). Per-pass: MergeMetrics copies
	// it from newM with no fallback, and json:"-" keeps it out of
	// serialized state.
	NoSubstantiveActivity bool `json:"-"`

	// SawManualCompactBoundary reflects the last tailer pass: true when it
	// parsed a manual compact_boundary (user /compact), the marker Claude Code
	// burst-writes when compaction finishes. The detector uses it to clear the
	// PreCompact force-working hold so the session releases working → ready
	// (#657, paired with #656). Per-pass: MergeMetrics copies it from newM with
	// no fallback, and json:"-" keeps it out of serialized state.
	SawManualCompactBoundary bool `json:"-"`

	// CompactInProgress is an overlay flag set by the detector (NOT the parser)
	// while a manual /compact is running: the PreCompact hook recorded a pending
	// compaction and no compact_boundary has landed yet. ClassifyState forces
	// working while it is set, holding the session busy through the silent
	// compaction window where the transcript receives no writes (#657). Cleared
	// the pass SawManualCompactBoundary fires. Transient, never persisted.
	CompactInProgress bool `json:"-"`

	// SubagentCompletions surfaces parent-side "subagent done" signals
	// from the most recent transcript scan (origin.kind="task-notification"
	// lines parsed by the Claude Code adapter). Per-pass and transient —
	// drained by the detector each activity event. See issue #134.
	SubagentCompletions []SubagentCompletion `json:"-"`

	// AppliedTaskDeltas surfaces the task-list deltas folded into this session's
	// task list during the most recent scan — one per applied create/update.
	// Per-pass and transient (drained by the detector each activity event to
	// record task_delta lifecycle events). See issue #662.
	AppliedTaskDeltas []AppliedTaskDelta `json:"-"`

	// Tasks is the current task list for this session, populated from
	// TaskCreate / TaskUpdate tool calls in the Claude Code transcript.
	// Nil for sessions that have not used TaskCreate (including non-Claude-Code
	// adapters).
	Tasks []Task `json:"tasks,omitempty"`

	// RateLimit is the most recent subscription quota snapshot observed for
	// this session. Populated by the Codex parser (from token_count events)
	// and by the Claude Code statusline hook. Nil when the underlying
	// provider doesn't surface quota (API-key Claude Code, Bedrock, Vertex)
	// or when no snapshot has arrived yet.
	RateLimit *RateLimitSnapshot `json:"rate_limit,omitempty"`

	// RateLimitForecastEta is the projected wall-clock time (Unix seconds)
	// at which the most-imminent rate-limit window will hit 100%, computed
	// from a rolling history of changed snapshots. Nil when forecasting is
	// not possible — insufficient history, flat or decreasing burn rate,
	// or the projected ETA exceeds the window's ResetsAt.
	RateLimitForecastEta *int64 `json:"rate_limit_forecast_eta,omitempty"`

	// TaskEstimate is the agent's self-reported task progress, parsed from
	// the most recent in-band marker in its transcript (issue #558). Nil
	// when the session never emitted a marker.
	TaskEstimate *TaskEstimate `json:"task_estimate,omitempty"`

	// TaskCompletionEta is the projected wall-clock time (Unix seconds) at
	// which the agent's current task completes, derived from TaskEstimate
	// via ForecastTaskCompletion. Nil when no projection is possible (no
	// marker, or no reported progress yet). UIs additionally suppress the
	// chip when the session is not `working`.
	TaskCompletionEta *int64 `json:"task_completion_eta,omitempty"`
}

// SubagentCompletion is the domain mirror of tailer.SubagentCompletion. The
// detector uses these to transition child sessions to ready as soon as their
// parent transcript records the authoritative task-notification event.
type SubagentCompletion struct {
	AgentID   string
	ToolUseID string
	Status    string
}

// AppliedTaskDelta is the domain mirror of tailer.AppliedTaskDelta — one
// task-list change the tailer folded in during a pass. The detector records a
// task_delta lifecycle event per entry, making task tracking an assertable
// observable in onboarding fixtures.
type AppliedTaskDelta struct {
	Op      string // create | update
	ID      string
	Subject string
	Status  string
}

// Task is the domain mirror of tailer.Task. It represents one item in the
// Claude Code task list, accumulated from TaskCreate / TaskUpdate tool calls.
type Task struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	ActiveForm  string `json:"active_form,omitempty"`
	Status      string `json:"status"` // "pending" | "in_progress" | "completed"
	// CompletedAt is the unix-seconds transcript-event time the task was
	// observed transitioning to "completed" (0 = unstamped, e.g. completed
	// before stamping existed). Feeds the tasks-derived ETA rate (#604).
	CompletedAt int64 `json:"completed_at,omitempty"`
}

// NeedsUserAttention returns true when a user-blocking tool is open — one
// that always requires user input regardless of permission settings.
// Most tools auto-execute (Bash, Read, Write, Agent, MCP, etc.) and should
// NOT trigger a waiting state; only explicit user-interaction tools do.
func (m *SessionMetrics) NeedsUserAttention() bool {
	if m == nil || !m.HasOpenToolCall {
		return false
	}
	for _, name := range m.LastOpenToolNames {
		if isUserBlockingTool(name) {
			return true
		}
	}
	return false
}

// isUserBlockingTool returns true for tools that always block for user input,
// regardless of permission settings. These are the only tools that should
// trigger the "waiting" state.
func isUserBlockingTool(name string) bool {
	return name == "AskUserQuestion" || name == "ExitPlanMode" || name == "question"
}

// HasOpenEditPermissionTool reports whether an open tool call is a
// permission-gated file-edit tool (Edit/Write/MultiEdit/NotebookEdit). These
// run in-process and complete near-instantly, so an open one that lingers is
// a strong signal the agent is blocked on a permission prompt — the basis for
// the OpenToolStalled fallback (#488). Bash/WebFetch/MCP are deliberately
// excluded: they can legitimately run for seconds, so duration alone can't
// distinguish "blocked" from "executing" for them (they rely on the hook).
func (m *SessionMetrics) HasOpenEditPermissionTool() bool {
	if m == nil || !m.HasOpenToolCall {
		return false
	}
	for _, name := range m.LastOpenToolNames {
		if isPermissionGatedEditTool(name) {
			return true
		}
	}
	return false
}

// isPermissionGatedEditTool reports whether name is a fast, in-process
// file-edit tool that prompts for permission by default.
//
// The match is case-insensitive because adapters name the same tool
// differently: claudecode emits PascalCase (Edit/Write/MultiEdit/
// NotebookEdit) while kiro-cli and pi emit lowercase (write/edit). All
// are fast in-process file edits, so an open one that lingers is the same
// "blocked on a permission prompt" signal regardless of casing (#588). No
// adapter names a long-running tool (bash/read/web_search/MCP) with one of
// these spellings, so case-folding introduces no false positives.
func isPermissionGatedEditTool(name string) bool {
	switch strings.ToLower(name) {
	case "edit", "write", "multiedit", "notebookedit":
		return true
	default:
		return false
	}
}

// IsAgentDone returns true when the agent finished its turn. The primary
// signal is Claude Code's "turn_duration" system event which fires exactly
// once at the end of each turn. Legacy formats (Codex) fall back to the
// heuristic of "last event is assistant and no open tool calls".
//
// Open tool calls (e.g. the Agent tool waiting for a sub-agent) override
// turn_done: the turn isn't truly complete until all tool results arrive.
func (m *SessionMetrics) IsAgentDone() bool {
	if m == nil {
		return false
	}
	// Open tool calls mean the agent is still processing — a sub-agent
	// spawned via the Agent tool fires turn_done before the tool result
	// comes back, but the session is NOT idle.
	if m.HasOpenToolCall {
		return false
	}
	// A live background process (Bash run_in_background) outlives the turn
	// that spawned it: Claude Code writes end_turn the instant the Bash tool
	// returns, but the process keeps running. The daemon's liveness probe
	// confirms it is still alive, so the session is NOT idle. See issue #445.
	if m.HasLiveBackgroundProcess {
		return false
	}
	// Primary: Claude Code writes a system/turn_duration event at end of turn.
	if m.LastEventType == "turn_done" {
		return true
	}
	// Fallback: Claude Code pre-stop_hook transcripts lack turn_duration.
	// Claude Code's "assistant" event is safe because HasOpenToolCall is
	// checked first — mid-turn tool calls block this, and streaming chunks
	// use "assistant_streaming" which isn't matched.
	//
	// Codex is NOT in this fallback: codex agents routinely emit a
	// preliminary `assistant_message` BEFORE calling a tool, so matching it
	// here would flip the session ready→working→ready on every turn. Codex
	// must rely on the `turn_done` primary path (emitted from task_complete).
	switch m.LastEventType {
	case "assistant", "assistant_output":
		return true
	}
	return false
}

// MergeMetrics merges new metrics with old, preserving old values when new are zero/empty.
func MergeMetrics(newM, oldM *SessionMetrics) *SessionMetrics {
	if newM == nil {
		return oldM
	}
	if oldM == nil {
		return newM
	}
	merged := newMergedMetrics(newM)
	carryForwardScalarFields(merged, oldM)
	carryForwardCumulativeCounters(merged, oldM)
	carryForwardOverlayState(merged, oldM)
	return merged
}

// newMergedMetrics copies the fields a fresh tailer pass always recomputes
// verbatim from newM. Fields not listed here are deliberately left at their
// zero value: LastCWD, PermissionPending, SawUserBlockingToolClosedThisPass,
// OpenToolStalled, and CompactInProgress are per-pass overlay/transient
// signals that the detector (re-)sets fresh after each merge, so carrying
// stale values here would be a bug, not a convenience.
func newMergedMetrics(newM *SessionMetrics) *SessionMetrics {
	return &SessionMetrics{
		ElapsedSeconds:       newM.ElapsedSeconds,
		TotalTokens:          newM.TotalTokens,
		ModelName:            newM.ModelName,
		AgentVersion:         newM.AgentVersion,
		ContextWindow:        newM.ContextWindow,
		ContextUtilization:   newM.ContextUtilization,
		PressureLevel:        newM.PressureLevel,
		ContextWindowUnknown: newM.ContextWindowUnknown,
		HasOpenToolCall:      newM.HasOpenToolCall,
		OpenToolCallCount:    newM.OpenToolCallCount,
		OpenSubagents:        newM.OpenSubagents,
		// Background-process fields are recomputed from the transcript every
		// pass (count + output paths + PIDs) — copy the new values verbatim.
		// HasLiveBackgroundProcess is set by the detector's probe *after* this
		// merge, so newM always carries its zero value here.
		BackgroundProcessCount:   newM.BackgroundProcessCount,
		BackgroundProcessOutputs: newM.BackgroundProcessOutputs,
		BackgroundProcessPIDs:    newM.BackgroundProcessPIDs,
		HasLiveBackgroundProcess: newM.HasLiveBackgroundProcess,
		LastEventType:            newM.LastEventType,
		LastOpenToolNames:        newM.LastOpenToolNames,
		LastWasUserInterrupt:     newM.LastWasUserInterrupt,
		LastWasToolDenial:        newM.LastWasToolDenial,
		EstimatedCostUSD:         newM.EstimatedCostUSD,
		LastAssistantText:        newM.LastAssistantText,
		TaskSummary:              newM.TaskSummary,
		IntentHeadline:           newM.IntentHeadline,
		QuestionHeadline:         newM.QuestionHeadline,
		PermissionMode:           newM.PermissionMode,
		SubagentCompletions:      newM.SubagentCompletions,
		AppliedTaskDeltas:        newM.AppliedTaskDeltas,
		CumInputTokens:           newM.CumInputTokens,
		CumOutputTokens:          newM.CumOutputTokens,
		CumCacheReadTokens:       newM.CumCacheReadTokens,
		CumCacheCreationTokens:   newM.CumCacheCreationTokens,
		CompletedTurns:           newM.CompletedTurns,
		CacheBloat:               newM.CacheBloat,
		CacheBloatTooltip:        newM.CacheBloatTooltip,
		Tasks:                    newM.Tasks,
		NoSubstantiveActivity:    newM.NoSubstantiveActivity,
		SawManualCompactBoundary: newM.SawManualCompactBoundary,
		RateLimit:                newM.RateLimit,
		RateLimitForecastEta:     newM.RateLimitForecastEta,
		TaskEstimate:             newM.TaskEstimate,
		TaskCompletionEta:        newM.TaskCompletionEta,
	}
}

// carryForwardScalarFields preserves single-value measurements from oldM
// when merged's copy of newM reads as "not observed this pass" (zero,
// empty, or "unknown") rather than a real freshly computed value.
func carryForwardScalarFields(merged, oldM *SessionMetrics) {
	if merged.ContextWindow == 0 && oldM.ContextWindow > 0 {
		merged.ContextWindow = oldM.ContextWindow
	}
	// Preserve a previously-known "unknown context" verdict over a fresh
	// false — pre-token TailAndProcess passes leave the flag at its zero
	// value, and we don't want the UI to flip the tentative bar off and
	// on between passes. The flag goes back to false only when the next
	// real computation produces a known window.
	if !merged.ContextWindowUnknown && oldM.ContextWindowUnknown && merged.ContextWindow == 0 {
		merged.ContextWindowUnknown = oldM.ContextWindowUnknown
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
	if merged.EstimatedCostUSD == 0 && oldM.EstimatedCostUSD > 0 {
		merged.EstimatedCostUSD = oldM.EstimatedCostUSD
	}
	if merged.PermissionMode == "" && oldM.PermissionMode != "" {
		merged.PermissionMode = oldM.PermissionMode
	}
	// AgentVersion appears once in the transcript header — carry it across the
	// many markerless passes that follow, exactly like ModelName.
	if merged.AgentVersion == "" && oldM.AgentVersion != "" {
		merged.AgentVersion = oldM.AgentVersion
	}
}

// carryForwardCumulativeCounters preserves the cumulative token/turn
// counters across passes that didn't recompute them — their zero value
// means "not observed this pass", not "reset to zero".
func carryForwardCumulativeCounters(merged, oldM *SessionMetrics) {
	if merged.CumInputTokens == 0 && oldM.CumInputTokens > 0 {
		merged.CumInputTokens = oldM.CumInputTokens
	}
	if merged.CumOutputTokens == 0 && oldM.CumOutputTokens > 0 {
		merged.CumOutputTokens = oldM.CumOutputTokens
	}
	if merged.CumCacheReadTokens == 0 && oldM.CumCacheReadTokens > 0 {
		merged.CumCacheReadTokens = oldM.CumCacheReadTokens
	}
	if merged.CumCacheCreationTokens == 0 && oldM.CumCacheCreationTokens > 0 {
		merged.CumCacheCreationTokens = oldM.CumCacheCreationTokens
	}
	// CompletedTurns is incremented by the detector on the SessionState *after*
	// this merge; the tailer never sets it, so newM always carries 0. Carry the
	// accumulated count forward or every pass would reset it to zero.
	if merged.CompletedTurns == 0 && oldM.CompletedTurns > 0 {
		merged.CompletedTurns = oldM.CompletedTurns
	}
}

// carryForwardOverlayState preserves detector-set overlay signals (the
// cache-bloat verdict, the task list, and rate-limit snapshots) across
// passes that didn't recompute them. TaskEstimate/TaskCompletionEta are
// deliberately excluded: see the comment at their use site below.
func carryForwardOverlayState(merged, oldM *SessionMetrics) {
	// CacheBloat / CacheBloatTooltip are overlay flags set by the detector on
	// turn boundaries (newM never carries them). Keep the last verdict sticky
	// across mid-turn passes so the glyph doesn't flicker; the detector
	// overwrites it on the next turn boundary.
	if !merged.CacheBloat && oldM.CacheBloat {
		merged.CacheBloat = oldM.CacheBloat
		merged.CacheBloatTooltip = oldM.CacheBloatTooltip
	}
	// nil Tasks = "no data yet"; non-nil empty slice = "no tasks" — overwrite only for the latter.
	if merged.Tasks == nil && oldM.Tasks != nil {
		merged.Tasks = oldM.Tasks
	}
	// Rate-limit snapshots arrive sporadically — preserve the previous one
	// across passes that didn't observe a fresh sample.
	if merged.RateLimit == nil && oldM.RateLimit != nil {
		merged.RateLimit = oldM.RateLimit
	}
	if merged.RateLimitForecastEta == nil && oldM.RateLimitForecastEta != nil {
		merged.RateLimitForecastEta = oldM.RateLimitForecastEta
	}
	// TaskEstimate/TaskCompletionEta deliberately get NO nil carry-over:
	// the tailer itself persists the last-seen marker across markerless
	// passes (lastTaskEstimate), so a nil here is a real signal — the
	// estimate was reset by a new user message and must not be
	// resurrected from the previous task (issue #558).
}
