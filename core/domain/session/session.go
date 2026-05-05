package session

import (
	"strings"
	"time"
)

// State constants — three MECE states for session lifecycle.
// See STATES.md for the formal state machine specification.
const (
	StateWorking = "working" // Agent actively processing (tools, text generation, hooks, compaction)
	StateWaiting = "waiting" // Agent finished turn, waiting for user input
	StateReady   = "ready"   // Session inactive (process exited, transcript removed, cancelled)

	CompactionStateNotCompacting = "not_compacting"
	CompactionStateCompacting    = "compacting"
	CompactionStatePostCompact   = "post_compact"
)

// IsCanonicalState reports whether s is one of the three valid lifecycle
// states. Anything else (empty, "cancelled", a typo) is a domain violation.
func IsCanonicalState(s string) bool {
	return s == StateWorking || s == StateWaiting || s == StateReady
}

// SessionMetrics holds computed performance metrics from transcript analysis.
type SessionMetrics struct {
	ElapsedSeconds     int64   `json:"elapsed_seconds"`
	TotalTokens        int64   `json:"total_tokens"`
	ModelName          string  `json:"model_name"`
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

	// LastCWD is the most recent working directory extracted from the
	// transcript during metrics parsing. Used to avoid a separate file read.
	LastCWD string `json:"-"` // transient — not persisted in session JSON

	// LastAssistantText is the text content of the most recent assistant
	// message, truncated to ~200 characters. Used to surface the question
	// or request when the session is in the waiting state.
	LastAssistantText string `json:"last_assistant_text,omitempty"`

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

	// SubagentCompletions surfaces parent-side "subagent done" signals
	// from the most recent transcript scan (origin.kind="task-notification"
	// lines parsed by the Claude Code adapter). Per-pass and transient —
	// drained by the detector each activity event. See issue #134.
	SubagentCompletions []SubagentCompletion `json:"-"`

	// Tasks is the current task list for this session, populated from
	// TaskCreate / TaskUpdate tool calls in the Claude Code transcript.
	// Nil for sessions that have not used TaskCreate (including non-Claude-Code
	// adapters).
	Tasks []Task `json:"tasks,omitempty"`
}

// SubagentCompletion is the domain mirror of tailer.SubagentCompletion. The
// detector uses these to transition child sessions to ready as soon as their
// parent transcript records the authoritative task-notification event.
type SubagentCompletion struct {
	AgentID   string
	ToolUseID string
	Status    string
}

// Task is the domain mirror of tailer.Task. It represents one item in the
// Claude Code task list, accumulated from TaskCreate / TaskUpdate tool calls.
type Task struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	ActiveForm  string `json:"active_form,omitempty"`
	Status      string `json:"status"` // "pending" | "in_progress" | "completed"
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

// trailingMarkdownNoise are characters that commonly appear AFTER a
// question mark when models wrap questions in markdown or punctuation.
// e.g. `**Question?**` (bold), `*Question?*` (italic), `_Question?_`,
// `~~Question?~~`, “ `Question?` “ (inline code), `"Question?"`
// (quoted), `(yes/no?)` (parenthetical), `[link?]` (bracketed), and
// trailing whitespace.
const trailingMarkdownNoise = "*_~`\"')] \t\n\r"

// markdownWrapper is the subset of trailingMarkdownNoise excluding whitespace —
// characters that wrap a sentence terminator like `?**` or `?]` without
// breaking the sentence.
const markdownWrapper = "*_~`\"')]"

// IsWaitingForUserInput returns true when the agent finished its turn and the
// last assistant message contains a question — indicating the agent is
// waiting for user input even though no user-blocking tool is open.
//
// Detects questions anywhere in the text, not just at the trailing position,
// so phrases like "What would you like? In the meantime I'll move on." are
// recognized as waiting prompts.
func (m *SessionMetrics) IsWaitingForUserInput() bool {
	if m == nil {
		return false
	}
	return ExtractQuestionSnippet(m.LastAssistantText) != ""
}

// ExtractQuestionSnippet returns the first non-rhetorical question sentence
// found in text, or an empty string when none is present. It preserves any
// trailing markdown wrappers (e.g. `**Question?**`) so the rendered snippet
// still reads naturally. URL fragments and other non-sentence `?` occurrences
// are skipped because the question mark must be followed by whitespace,
// end-of-string, or markdown wrappers leading to either.
//
// First-question-wins is preferred over last-question because agents typically
// lead with the actual question and follow with examples or status notes; a
// bullet list of options ending in `?` would otherwise hijack the snippet.
//
// Rhetorical questions — Q&A pairs like "Why do programmers prefer dark mode?
// Because light attracts bugs." — are skipped: the agent isn't actually
// waiting on the user. Detection is heuristic (the next sentence starts with
// an answer marker like "Because"); false negatives are preferred over
// false positives in mid-paragraph waiting detection.
func ExtractQuestionSnippet(text string) string {
	if text == "" {
		return ""
	}
	sentences := splitSentences(text)
	for i, s := range sentences {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		stripped := strings.TrimRight(trimmed, trailingMarkdownNoise)
		if stripped == "" {
			continue
		}
		if stripped[len(stripped)-1] != '?' {
			continue
		}
		if isRhetorical(sentences, i) {
			continue
		}
		return trimmed
	}
	return ""
}

// answerPrefixes flag a sentence as starting with an explanatory answer to a
// preceding question. Conservative on purpose — connectives that strongly
// imply "this sentence answers the previous question" rather than continuing
// the agent's status report. False negatives (rhetorical Qs we miss) are
// preferable to false positives that would re-break #236's mid-paragraph
// detection.
var answerPrefixes = []string{
	"because ", "because,", "because:",
	"since ", "since,",
}

// isRhetorical reports whether the question at sentences[qIdx] is answered
// by a subsequent sentence in the same paragraph — i.e. a Q&A pair like
// "Why do programmers prefer dark mode? Because light attracts bugs."
func isRhetorical(sentences []string, qIdx int) bool {
	for k := qIdx + 1; k < len(sentences); k++ {
		next := strings.TrimSpace(sentences[k])
		if next == "" {
			continue
		}
		return looksLikeAnswer(next)
	}
	return false
}

func looksLikeAnswer(s string) bool {
	s = strings.TrimLeft(s, markdownWrapper)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, p := range answerPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// splitSentences splits text on sentence terminators (`.`, `!`, `?`) and
// newlines. A terminator only ends a sentence when followed by whitespace,
// end-of-string, or markdown wrappers leading to either — so URL `?` and
// abbreviations like `e.g.` don't split. Each returned sentence retains its
// terminator and any wrapper characters.
func splitSentences(text string) []string {
	var sentences []string
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch c {
		case '.', '!', '?':
			j := i + 1
			for j < len(text) && strings.IndexByte(markdownWrapper, text[j]) >= 0 {
				j++
			}
			if j == len(text) || isSentenceBreak(text[j]) {
				sentences = append(sentences, text[start:j])
				start = j
				i = j - 1
			}
		case '\n':
			sentences = append(sentences, text[start:i])
			start = i + 1
		}
	}
	if start < len(text) {
		sentences = append(sentences, text[start:])
	}
	return sentences
}

func isSentenceBreak(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
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

// subagentSummary tracks the aggregate state of all child sessions.
type subagentSummary struct {
	Total   int `json:"total"`
	Working int `json:"working"`
	Waiting int `json:"waiting"`
	Ready   int `json:"ready"`
}

// Launcher identifies the terminal emulator or IDE that spawned the session's
// agent process. Captured once from the process env when the PID is first
// known (see processlifecycle.ReadLauncherEnv). Fields are best-effort —
// clients must treat every field as optional and fall back to the session
// CWD when nothing identifies the host.
//
// TermProgram is the primary identifier; clients map it to a platform-native
// activator (e.g. the macOS menu-bar app derives an app bundle ID from it).
// Keeping that derivation client-side avoids persisting redundant state.
type Launcher struct {
	TermProgram    string `json:"term_program,omitempty"`     // $TERM_PROGRAM (e.g. iTerm.app, Apple_Terminal, vscode, cursor, ghostty, WezTerm, Hyper)
	ITermSessionID string `json:"iterm_session_id,omitempty"` // $ITERM_SESSION_ID
	TermSessionID  string `json:"term_session_id,omitempty"`  // $TERM_SESSION_ID (Terminal.app)
	TmuxPane       string `json:"tmux_pane,omitempty"`        // $TMUX_PANE
	TmuxSocket     string `json:"tmux_socket,omitempty"`      // first `,`-field of $TMUX
	VSCodePID      int    `json:"vscode_pid,omitempty"`       // $VSCODE_PID (vscode/cursor/windsurf)
	TTY            string `json:"tty,omitempty"`              // controlling TTY of the agent process, e.g. "/dev/ttys021" — Terminal.app AppleScript matches tabs by this
	KittyListenOn  string `json:"kitty_listen_on,omitempty"`  // $KITTY_LISTEN_ON — kitty remote-control socket path
	KittyWindowID  string `json:"kitty_window_id,omitempty"`  // $KITTY_WINDOW_ID — kitty window identifier
}

// IsEmpty reports whether the launcher carries no identifying information
// — i.e. every field is zero. Capture helpers use this to decide whether to
// return nil rather than attach a meaningless struct to the session.
func (l *Launcher) IsEmpty() bool {
	return l == nil || (l.TermProgram == "" && l.ITermSessionID == "" &&
		l.TermSessionID == "" && l.TmuxPane == "" &&
		l.TmuxSocket == "" && l.VSCodePID == 0 && l.TTY == "" &&
		l.KittyListenOn == "" && l.KittyWindowID == "")
}

// SessionState represents the current state of a Claude Code or Copilot session.
type SessionState struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	// Adapter identifies the source agent (e.g. "claude-code", "codex").
	// Empty means Claude Code (for backwards compatibility).
	Adapter         string          `json:"adapter,omitempty"`
	CompactionState string          `json:"compaction_state,omitempty"`
	Model           string          `json:"model,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	TranscriptPath  string          `json:"transcript_path,omitempty"`
	GitBranch       string          `json:"git_branch,omitempty"`
	ProjectName     string          `json:"project_name,omitempty"`
	FirstSeen       int64           `json:"first_seen"`
	UpdatedAt       int64           `json:"updated_at"`
	Confidence      string          `json:"confidence"`
	EventCount      int             `json:"event_count"`
	LastEvent       string          `json:"last_event"`
	LastMatcher     string          `json:"last_matcher,omitempty"`
	Metrics         *SessionMetrics `json:"metrics,omitempty"`

	// PID of the Claude Code process that owns this session (set on SessionStart).
	PID int `json:"pid,omitempty"`

	// Launcher identifies the terminal/IDE that spawned the agent process.
	// Captured once when PID is first assigned; nil if env capture failed
	// or no recognized env vars were present.
	Launcher *Launcher `json:"launcher,omitempty"`

	// ParentSessionID links a subagent session to its spawning parent session.
	// Derived from file path or heuristic matching in SessionDetector.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	// Subagents holds the aggregate state of all child sessions.
	// Nil when this session has no children.
	Subagents *subagentSummary `json:"subagents,omitempty"`

	// DaemonVersion records which irrlichd version created this session,
	// enabling future data migrations when the schema evolves.
	DaemonVersion string `json:"daemon_version,omitempty"`

	// Transcript monitoring for waiting-state recovery.
	LastTranscriptSize int64  `json:"last_transcript_size,omitempty"`
	WaitingStartTime   *int64 `json:"waiting_start_time,omitempty"`
}

// IsStale reports whether the session's last update is older than maxAge.
// A zero or negative maxAge disables the check (always returns false).
func (s *SessionState) IsStale(maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	return time.Since(time.Unix(s.UpdatedAt, 0)) > maxAge
}

// StringState returns a display-friendly state string including compaction state.
func (s *SessionState) StringState() string {
	if s.CompactionState != "" && s.CompactionState != CompactionStateNotCompacting {
		return s.State + "(" + s.CompactionState + ")"
	}
	return s.State
}

// MergeMetrics merges new metrics with old, preserving old values when new are zero/empty.
func MergeMetrics(newM, oldM *SessionMetrics) *SessionMetrics {
	if newM == nil {
		return oldM
	}
	if oldM == nil {
		return newM
	}
	merged := &SessionMetrics{
		ElapsedSeconds:         newM.ElapsedSeconds,
		TotalTokens:            newM.TotalTokens,
		ModelName:              newM.ModelName,
		ContextWindow:          newM.ContextWindow,
		ContextUtilization:     newM.ContextUtilization,
		PressureLevel:          newM.PressureLevel,
		ContextWindowUnknown:   newM.ContextWindowUnknown,
		HasOpenToolCall:        newM.HasOpenToolCall,
		OpenToolCallCount:      newM.OpenToolCallCount,
		OpenSubagents:          newM.OpenSubagents,
		LastEventType:          newM.LastEventType,
		LastOpenToolNames:      newM.LastOpenToolNames,
		LastWasUserInterrupt:   newM.LastWasUserInterrupt,
		LastWasToolDenial:      newM.LastWasToolDenial,
		EstimatedCostUSD:       newM.EstimatedCostUSD,
		LastAssistantText:      newM.LastAssistantText,
		PermissionMode:         newM.PermissionMode,
		SubagentCompletions:    newM.SubagentCompletions,
		CumInputTokens:         newM.CumInputTokens,
		CumOutputTokens:        newM.CumOutputTokens,
		CumCacheReadTokens:     newM.CumCacheReadTokens,
		CumCacheCreationTokens: newM.CumCacheCreationTokens,
		Tasks:                  newM.Tasks,
	}
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
	// nil Tasks = "no data yet"; non-nil empty slice = "no tasks" — overwrite only for the latter.
	if merged.Tasks == nil && oldM.Tasks != nil {
		merged.Tasks = oldM.Tasks
	}
	return merged
}
