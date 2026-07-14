// Package tailer provides transcript tailing and metrics computation.
// Format-specific parsing is delegated to TranscriptParser implementations
// that live in each agent adapter package.
package tailer

import (
	"regexp"
	"strings"
	"time"
)

// ToolUse represents a single tool invocation with its unique ID.
type ToolUse struct {
	ID   string // unique tool call ID (e.g. "toolu_01FUU...", "call_TkY0...", "call_63hf...")
	Name string // tool name (e.g. "Bash", "Read", "shell")
}

// SubagentCompletion is an authoritative "subagent done" signal that lives on
// the parent transcript (Claude Code writes it as a user-origin event with
// origin.kind="task-notification"). The subagent's own JSONL is structurally
// ambiguous when stop_reason is null; this parent-side event resolves it
// without timing-based heuristics. See issue #134.
type SubagentCompletion struct {
	AgentID   string // <task-id> — matches the agent-<id>.jsonl filename
	ToolUseID string // <tool-use-id> — the parent's Agent tool_use call
	Status    string // <status> — "completed", etc.
}

// Task status and operation constants for TaskCreate / TaskUpdate tool_use events.
const (
	TaskStatusPending    = "pending"
	TaskStatusInProgress = "in_progress"
	TaskStatusCompleted  = "completed"

	TaskOpCreate = "create"
	TaskOpUpdate = "update"
	// TaskOpAssignID carries the authoritative task ID from a TaskCreate
	// tool_result back to the provisionally-numbered task created by the
	// matching tool_use (paired via ToolUseID). Claude Code's task IDs are
	// monotonic per session, so a tailer that starts (or restarts) mid-session
	// cannot reconstruct them by counting creates — see issue #615.
	TaskOpAssignID = "assign_id"
)

// Task represents a single item in the session's Claude Code task list,
// accumulated from TaskCreate / TaskUpdate tool_use events in the transcript.
type Task struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	ActiveForm  string `json:"active_form,omitempty"`
	Status      string `json:"status"` // TaskStatusPending | TaskStatusInProgress | TaskStatusCompleted
	// CompletedAt is the unix-seconds event time of the observed transition
	// to TaskStatusCompleted (0 = unstamped, e.g. restored from a pre-#604
	// ledger). Stamped on the edge only — re-asserted completions keep the
	// original stamp. Feeds the tasks-derived ETA rate.
	CompletedAt int64 `json:"completed_at,omitempty"`
}

// TaskDelta is emitted by the Claude Code parser for each TaskCreate or
// TaskUpdate tool_use block, and for each TaskCreate tool_result carrying the
// authoritative task ID. The tailer folds deltas into its tasks slice.
type TaskDelta struct {
	Op          string // "create" | "update" | "assign_id"
	ID          string // update: taskId input; assign_id: toolUseResult.task.id; empty on create
	Subject     string // TaskCreate only
	Description string // TaskCreate only
	ActiveForm  string // TaskCreate only
	Status      string // TaskUpdate only; "pending" on create is applied by tailer
	ToolUseID   string // create: the TaskCreate tool_use id; assign_id: the result's tool_use_id
}

// TaskSnapshotEntry is one row in a Claude Code task_reminder attachment, an
// authoritative snapshot of which tasks Claude is still actively tracking and
// their current status. Used by the tailer to reconcile drift after a stale
// or bogus TaskUpdate that never gets a follow-up `completed`. See issue #282.
type TaskSnapshotEntry struct {
	ID         string
	Subject    string
	ActiveForm string
	Status     string // "pending" | "in_progress" | "completed"
}

// BackgroundSpawn signals that a `Bash` tool call with `run_in_background:
// true` reported its background id. Claude Code returns this in the Bash
// tool_result text ("Command running in background with ID: <id>. Output is
// being written to: <path>"), so the parser reads it off the result rather
// than the tool_use input (which carries only the run_in_background flag).
// The tailer folds these into its open-background-process set. See issue #445.
type BackgroundSpawn struct {
	BashID     string // the background shell id (e.g. "bc1h56v8v")
	OutputPath string // tasks/<bash_id>.output — where stdout/stderr is written
}

// BashOutputPoll records a `BashOutput` tool_use: the agent polling a
// background process by id. ToolUseID is the poll call's own id; BashID is
// the background process it targets. The tailer remembers the pairing so a
// later tool_result reporting a terminated status (TerminatedBashOutputIDs)
// can be attributed to the right background process. See issue #445.
type BashOutputPoll struct {
	ToolUseID string
	BashID    string
}

// ParsedEvent is the normalized output from a format-specific transcript parser.
// Each parser maps its native event structure into these fields.
type ParsedEvent struct {
	EventType string    // normalized: "assistant_message", "user_message", "turn_done", etc.
	Timestamp time.Time // event timestamp
	Skip      bool      // true → ignore this line entirely

	// Tool tracking — parser reports deltas, tailer accumulates.
	ToolUses       []ToolUse // tool invocations from this event (id + name)
	ToolResultIDs  []string  // IDs of completed tool results in this event
	IsError        bool      // true if the tool result had is_error=true
	ClearToolNames bool      // true → reset open tool state (on user messages)

	// IsUserInterrupt is true only for real user ESC cancellations — the
	// exact "[Request interrupted by user]" text marker on a user event,
	// without the "for tool use" suffix. Kept distinct from IsError so the
	// classifier can tell an ESC apart from a normal tool failure (grep
	// with no matches, a failing build, etc.). See issue #102 Bug B.
	IsUserInterrupt bool

	// IsManualCompactBoundary is true only for a compact_boundary system event
	// whose compactMetadata.trigger == "manual" — a user-invoked /compact. The
	// parser also sets EventType="turn_done" for it (the context was replaced,
	// the prior turn is definitively over). The tailer surfaces it as the
	// per-pass SessionMetrics.SawManualCompactBoundary so the detector can clear
	// the PreCompact force-working hold (#657). Auto-compaction never sets this.
	IsManualCompactBoundary bool

	// PendingBackgroundAgentCount, when non-nil, is Claude Code's own
	// live count of background subagents (Agent-tool launches) still
	// running as of this turn_duration event. Nil when the transcript's
	// turn_duration event carries no such field (older Claude Code
	// versions) — absence must not be read as zero. See issue #1036.
	PendingBackgroundAgentCount *int

	// IsToolDenial is true when the user denied a permission prompt for a
	// tool call ("[Request interrupted by user for tool use]" text marker).
	// This is a *different* signal from IsUserInterrupt: a tool denial does
	// not end the agent's turn — the agent typically continues with a
	// different approach — so it must NOT feed the cancellation rule.
	// Tracked separately for observability and to suppress the spurious
	// working→ready→working flicker that happened when the parser lumped
	// both markers under IsUserInterrupt.
	IsToolDenial bool

	// SubagentCompletions are parent-side signals that a child subagent has
	// finished (parsed from origin.kind="task-notification" lines). The
	// detector drains these on the parent path to transition children to
	// ready without depending on the subagent's own ambiguous final line.
	SubagentCompletions []SubagentCompletion

	// TaskDeltas are TaskCreate / TaskUpdate events from the Claude Code
	// transcript. The tailer folds them into a running tasks slice.
	TaskDeltas []TaskDelta

	// BackgroundSpawns are Bash run_in_background launches observed in this
	// event (read from the Bash tool_result text). The tailer adds each to
	// its open-background-process set. See issue #445.
	BackgroundSpawns []BackgroundSpawn

	// BashOutputPolls are BashOutput tool_use calls in this event, pairing the
	// poll's tool_use id with the background id it targets. See issue #445.
	BashOutputPolls []BashOutputPoll

	// TerminatedBashOutputIDs are tool_result ids whose content reports a
	// terminated background-process status (anything other than "running").
	// The tailer matches each against a remembered BashOutputPoll to drop the
	// corresponding background process from its open set. See issue #445.
	TerminatedBashOutputIDs []string

	// KilledShellIDs are background ids named by a KillShell tool_use in this
	// event — an explicit, single-event termination signal. See issue #445.
	KilledShellIDs []string

	// TerminatedBackgroundTaskIDs are background ids (== backgroundTaskId)
	// reported done by a terminal <task-notification> rather than a BashOutput
	// poll. Orchestrated / SDK-harnessed claude sessions report background
	// completion this way (TaskOutput + task-notification) instead of via
	// BashOutput / KillShell. The tailer drops each from its open set; a
	// non-background id (e.g. a subagent's) is a harmless no-op. See issue #445.
	TerminatedBackgroundTaskIDs []string

	// TaskSnapshot, when non-nil, is the authoritative list of tasks Claude
	// Code is currently tracking, parsed from a task_reminder attachment.
	// Pointer-to-slice so an empty list (legitimate "nothing active" signal)
	// is distinguishable from "no snapshot in this event". The tailer applies
	// it after TaskDeltas as a defensive reconciliation pass against drift
	// from stale/bogus TaskUpdate deltas. See issue #282.
	TaskSnapshot *[]TaskSnapshotEntry

	// Metadata extracted by the parser.
	ModelName string
	// AgentVersion is the upstream agent CLI's version, if the transcript
	// exposes it (claudecode header `version`, codex `session_meta.cli_version`,
	// aider `> Aider vX.Y.Z`). Empty otherwise. See issue #374.
	AgentVersion  string
	ContextWindow int64
	// Tokens is the latest-turn snapshot used for context-utilization display.
	// It is NOT used for cost accumulation — use Contribution for that.
	Tokens *TokenSnapshot
	// Contribution, when non-nil, signals that the adapter has completed a
	// billable turn. The tailer sums these per-model instead of the old
	// scalar cum* accumulators.
	Contribution *PerTurnContribution
	// CumulativeTokens and RequestID are retained for the legacy tailer code
	// path and the testParser. They will be removed once all adapters emit
	// Contribution and the old 3-way branch in tailer.go is deleted.
	CumulativeTokens *TokenSnapshot
	RequestID        string
	AssistantText    string // ≤200 chars, for waiting-state display
	CWD              string // working directory if found
	PermissionMode   string // Claude Code only

	// RateLimit, when non-nil, is a subscription-quota snapshot extracted from
	// this event. Codex emits one per token_count event_msg; Claude Code feeds
	// them in via the statusline hook (different path — parser only fills this
	// for in-band transcript signals).
	RateLimit *RateLimitSnapshot

	// TaskEstimate, when non-nil, is the agent's self-reported task progress,
	// parsed from an in-band HTML-comment marker in this event's assistant
	// text (issue #558). Latest valid marker wins; the tailer keeps the most
	// recent one across passes.
	TaskEstimate *TaskEstimate

	// TaskSummary, when non-nil, is the agent's one-line description of what
	// the current task is about, parsed from an in-band HTML-comment marker
	// (issue #738). Latest non-empty marker wins; the tailer keeps the most
	// recent one across passes.
	TaskSummary *TaskSummary

	// TaskQuestion, when non-nil, is the agent's terse one-line version of the
	// question it is currently blocked on, parsed from an in-band HTML-comment
	// marker (issue #759). Latest non-empty marker wins; the tailer keeps the
	// most recent one across passes and clears it on a real user message.
	TaskQuestion *TaskQuestion

	// AwaySummary, when non-nil, is Claude Code's own idle "away_summary"
	// recap read from a system transcript event (issue #979) — a passive,
	// higher-quality upgrade over the deterministic fallback, arriving a few
	// minutes after the turn ends. Adapters without an equivalent recap
	// simply never set this field.
	AwaySummary *AwaySummary

	// UserText is the prose of a genuine user prompt on this event (not a
	// tool result). The tailer captures the FIRST non-empty one as the
	// heuristic-fallback task summary (issue #738). Adapters that don't set
	// it simply have no heuristic fallback (the marker still works).
	UserText string
}

// RateLimitSnapshot mirrors session.RateLimitSnapshot inside the tailer
// package so parsers can emit snapshots without importing the domain. The
// adapter glue (core/adapters/outbound/metrics) converts to the domain type
// at the same boundary it converts Task and SubagentCompletion.
type RateLimitSnapshot struct {
	Windows     []RateLimitWindow
	PlanType    string
	Credits     *CreditsSnapshot
	ReachedType string
	SampledAt   int64
}

// RateLimitWindow mirrors session.RateLimitWindow.
type RateLimitWindow struct {
	UsedPercent   float64
	WindowMinutes int
	ResetsAt      int64
}

// CreditsSnapshot mirrors session.CreditsSnapshot.
type CreditsSnapshot struct {
	HasCredits bool
	Unlimited  bool
	Balance    float64
}

// TaskEstimate mirrors session.TaskEstimate inside the tailer package so
// parsers can emit estimates without importing the domain (same boundary as
// RateLimitSnapshot above). It carries the agent's self-reported progress
// from an in-band marker like
//
//	<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":2} -->
//
// A "round" is the agent's own unit (≈ a task phase) — never counted
// daemon-side. See issue #558.
type TaskEstimate struct {
	TotalRounds     int
	CompletedRounds int
	Risk            string
	Confidence      *float64
	// ObservedAt is the unix-seconds timestamp of the transcript event the
	// marker appeared in (replay-deterministic, unlike daemon wall-clock).
	ObservedAt int64
}

// TaskSummary mirrors the agent's one-line task description parsed from an
// in-band marker like
//
//	<!-- {"marker":"irrlicht-summary","summary":"Add a logout button"} -->
//
// It is the stable companion to TaskEstimate (issue #738): a human-readable
// "what is this session about" that the UI surfaces in both the waiting and
// ready states. Wall-clock independent, so it survives replay.
type TaskSummary struct {
	Text string
	// ObservedAt is the unix-seconds timestamp of the transcript event the
	// marker appeared in (replay-deterministic), used for latest-wins.
	ObservedAt int64
}

// TaskQuestion mirrors the agent's terse one-line question parsed from an
// in-band marker like
//
//	<!-- {"marker":"irrlicht-question","question":"Run the migration now?"} -->
//
// It is the question-state companion to TaskSummary (issue #759): the preferred
// source for the surfaced waiting-state headline when the agent supplies one.
// Wall-clock independent, so it survives replay.
type TaskQuestion struct {
	Text string
	// ObservedAt is the unix-seconds timestamp of the transcript event the
	// marker appeared in (replay-deterministic), used for latest-wins.
	ObservedAt int64
}

// AwaySummary mirrors Claude Code's own idle recap — a system transcript
// event written a few minutes after a turn ends, e.g.
//
//	{"type":"system","subtype":"away_summary","content":"Goal was X. Done: Y. Next: Z."}
//
// It is a passive, lower-priority alternative to TaskQuestion: a real
// self-report always wins when present, but a session that never emits one
// still gets upgraded from the deterministic fallback once the recap lands.
// See issue #979. Wall-clock independent (ObservedAt is the transcript
// event's own timestamp), so it survives replay.
type AwaySummary struct {
	Text string
	// ObservedAt is the unix-seconds timestamp of the transcript event the
	// recap appeared in (replay-deterministic), used for latest-wins.
	ObservedAt int64
}

// TokenSnapshot holds a token breakdown from a single event.
// Used for context-utilization display (latest-turn snapshot).
// For cost accumulation, adapters emit PerTurnContribution instead.
type TokenSnapshot struct {
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	Total         int64
}

// UsageBreakdown is the format-neutral per-turn token count produced by each
// adapter after deduplication and provider-specific field mapping.
// Unused fields stay zero; the tailer sums them across turns per model.
type UsageBreakdown struct {
	Input           int64
	Output          int64
	CacheRead       int64 // Anthropic cache hit OR OpenAI cached_tokens
	CacheCreation5m int64 // Anthropic ephemeral 5-minute write
	CacheCreation1h int64 // Anthropic ephemeral 1-hour write
}

// PerTurnContribution is what an adapter emits for one completed billable turn.
// The tailer accumulates these into cumByModel for cost calculation.
type PerTurnContribution struct {
	Model           string
	Usage           UsageBreakdown
	ProviderCostUSD *float64 // set when the provider reports an authoritative cost (Pi)
}

// TranscriptParser parses a single JSONL line from a specific transcript format
// and returns a normalized ParsedEvent. Implementations live in each agent
// adapter package (claudecode, codex, pi).
type TranscriptParser interface {
	// ParseLine parses a raw JSON map and returns a normalized event.
	// Returns nil for lines that should be silently ignored (no event emitted).
	ParseLine(raw map[string]interface{}) *ParsedEvent
}

// RawLineParser is implemented by parsers whose source format is not JSONL.
// When the tailer detects this capability it skips its JSON pre-parse and
// hands the trimmed line directly to ParseLineRaw. ParseLine is still part
// of the TranscriptParser contract; raw-line parsers should make it a no-op
// returning nil.
//
// The state classifier returns the session to ready only on
// EventType="turn_done"; an assistant_message alone leaves the session
// working. Parsers whose transcript carries an explicit end-of-turn marker
// emit turn_done from ParseLineRaw directly. Parsers whose transcript has
// no such marker (e.g. aider's `.aider.chat.history.md` — aider's idle TUI
// prompt is not written to the file) should additionally implement
// idleFlusher to synthesize turn_done when the file has been quiet long
// enough.
type RawLineParser interface {
	ParseLineRaw(line string) *ParsedEvent
}

// pendingContributor is an optional interface that stateful parsers implement
// (currently Claude Code) to expose the in-progress turn's cost contribution.
// The tailer queries this at metrics-computation time to include the latest
// streaming turn in the live cost display even before the turn is complete.
type pendingContributor interface {
	PendingContribution() *PerTurnContribution
}

// idleFlusher is an optional interface for raw-line parsers whose source
// format has no in-band end-of-turn marker. The tailer calls IdleFlush after
// each TailAndProcess pass with the elapsed wall-clock time since the parser
// last saw a transcript line. Implementations return a synthesized turn_done
// event when both (a) a turn is open and (b) idleFor exceeds the parser's
// own threshold; otherwise return nil. See aider/parser.go.
type idleFlusher interface {
	IdleFlush(idleFor time.Duration) *ParsedEvent
}

// queuedTurnSplitter is an optional interface a parser implements when its
// adapter's queued-follow-up model genuinely starts a NEW, distinct turn
// once drained — not a mid-turn continuation/steering input folded into the
// turn already in progress. Mistral Vibe's in-memory message queue drains a
// follow-up prompt synchronously the instant the prior turn clears, with no
// observable ready gap (issue #988); without this signal the tailer would
// fold both turns into one working→ready span.
//
// Deliberately opt-in and adapter-specific: other adapters queue follow-ups
// differently and must NOT implement this. Pi's steering input, for
// example, is intentionally the SAME turn — its
// 2-10_mid-turn-message-queued fixture asserts a single contiguous working
// span with no intervening ready, and implementing this interface there
// would incorrectly split it.
type queuedTurnSplitter interface {
	SplitsQueuedFollowUpTurns() bool
}

// ParserLedger holds the durable state a stateful parser checkpoints across
// daemon restarts. Fields are parser-specific; unused ones stay zero.
type ParserLedger struct {
	// LastRequestID is the last requestId seen by the Claude Code parser.
	// Restored so the dedup cursor resumes at the right turn boundary.
	LastRequestID string `json:"last_request_id,omitempty"`
	// CumCursor is the last committed total_token_usage seen by the Codex parser.
	// Restored so per-turn deltas after a restart are computed correctly.
	CumCursor *UsageBreakdown `json:"cum_cursor,omitempty"`
}

// ParserStateProvider is an optional interface for stateful parsers that can
// checkpoint and restore their per-turn accumulation state across tailer restarts.
type ParserStateProvider interface {
	GetParserLedger() ParserLedger
	SetParserLedger(ParserLedger)
}

// TranscriptPathAware is an optional interface for parsers that need to know
// which transcript file they are parsing — e.g. to read a metadata sidecar
// living next to it (Kiro CLI's <uuid>.json, issue #599). The tailer injects
// the path once at construction; ParseLine itself stays path-free.
//
// Deliberately not renamed for godre:S8196 ("-er" suffix convention): this is
// an optional capability-marker interface (type-asserted with `x, ok :=
// p.(TranscriptPathAware)`), a well-established alternate Go idiom for that
// pattern, and it is implemented across three separate adapter packages
// (kirocli, antigravity) plus pkg/tailer itself — renaming would touch a wide,
// cross-package surface for a naming style that already reads clearly.
type TranscriptPathAware interface {
	SetTranscriptPath(path string)
}

// ReplayStoreStager is an optional interface for parsers whose live
// path-resolution reads a store that is NOT a transcript sibling — e.g.
// Antigravity's conversations/<conv>.db, which the parser finds by climbing the
// brain/<conv>/.system_generated/logs/ tree (issue #766). During replay the
// recorded transcript is materialized in a flat scratch dir, so that climb
// would miss the captured store. The replay engine gives such a parser a chance
// to rebuild its expected directory layout under tmpDir from a store captured
// next to the recorded transcript (recordingDir/store/…), and to return the
// transcript path the tailer should open instead of the flat default.
// Returning "" (or any error) means "no relocation — use the flat path", so a
// recording without a captured store replays exactly as before.
type ReplayStoreStager interface {
	StageReplayStore(tmpDir, recordingDir string) (transcriptPath string, err error)
}

// LedgerSchemaVersion is the current ledger schema. A persisted ledger with a
// different version is discarded on load, forcing a full transcript re-scan
// under the current parser. Bump it whenever a LedgerState change (or a parser
// fix) makes previously persisted state misleading.
//
// History: 3 — #615 (authoritative task IDs + TaskSeq);
// 4 — #649 (LastEventType persisted; the bump also heals sessions stranded
// in `working` by pre-#642 parsers, since the re-scan reclassifies them);
// 5 — #705 (LastAssistantText persisted; the bump also heals sessions
// mis-demoted from `waiting` to `ready` on restart, since the re-scan
// recovers the question text IsWaitingForUserInput needs).
const LedgerSchemaVersion = 5

// LedgerState is the durable portion of a tailer's accumulation state, written
// to disk after every TailAndProcess pass so that daemon restarts don't reset
// cumulative cost to zero for in-flight sessions.
type LedgerState struct {
	SchemaVersion      int                        `json:"schema_version"`
	LastOffset         int64                      `json:"last_offset"`
	CumByModel         map[string]*UsageBreakdown `json:"cum_by_model,omitempty"`
	CumProviderCostUSD float64                    `json:"cum_provider_cost_usd,omitempty"`
	ParserState        *ParserLedger              `json:"parser_state,omitempty"`
	Tasks              []Task                     `json:"tasks,omitempty"`
	// TaskSeq persists the provisional-ID counter. Claude Code's task IDs are
	// monotonic per session while completed batches are pruned from Tasks, so
	// len(Tasks) understates the counter after a restart and every subsequent
	// TaskUpdate would miss its target. See issue #615.
	TaskSeq int `json:"task_seq,omitempty"`
	// PendingTaskCreates persists in-flight TaskCreate calls (tool_use id →
	// provisional task ID) so a restart between a create's tool_use and its
	// result can still apply the authoritative ID. See issue #615.
	PendingTaskCreates map[string]string `json:"pending_task_creates,omitempty"`
	// BackgroundProcs persists the open-background-process set (background id
	// → output path) so a daemon restart keeps holding the session `working`
	// for processes still alive. See issue #445.
	BackgroundProcs map[string]string `json:"background_procs,omitempty"`
	// PendingBashPolls persists in-flight BashOutput polls (poll tool_use id →
	// background id) so a restart between a poll's tool_use and its terminated
	// tool_result can still attribute the termination and clear the process.
	// See issue #445.
	PendingBashPolls map[string]string `json:"pending_bash_polls,omitempty"`
	// LastTaskEstimate / FirstTaskEstimate persist the agent's task-progress
	// marker and the current task's rate baseline (issue #558). MergeMetrics
	// no longer carries the estimate across markerless passes (so a
	// user-message reset propagates), which means a daemon restart would
	// otherwise blank the ETA chip and lose the baseline until the next
	// marker — persisting them here keeps both across restarts. A reset
	// stores nil, so the reset survives a restart too.
	LastTaskEstimate  *TaskEstimate `json:"last_task_estimate,omitempty"`
	FirstTaskEstimate *TaskEstimate `json:"first_task_estimate,omitempty"`
	// LastTaskSummary persists the agent's task-summary marker and
	// FirstUserText persists the heuristic-fallback first user message
	// (issue #738), both so a daemon restart keeps surfacing the summary
	// before the next marker/message arrives. A reset stores a nil summary.
	LastTaskSummary *TaskSummary `json:"last_task_summary,omitempty"`
	FirstUserText   string       `json:"first_user_text,omitempty"`
	// LastTaskQuestion persists the agent's task-question marker (issue #759)
	// so a daemon restart keeps surfacing the waiting headline before the next
	// marker/message arrives. A reset stores nil.
	LastTaskQuestion *TaskQuestion `json:"last_task_question,omitempty"`
	// ModelName is the last observed model for the session. Persisted so that
	// applyContribution's fallback (used when a contribution event carries no
	// model — codex token_count) still routes to the right pricing bucket
	// after a daemon restart, before the next model-bearing event arrives.
	ModelName string `json:"model_name,omitempty"`
	// AgentVersion persists the upstream agent CLI version parsed from the
	// transcript header so a daemon restart that resumes at LastOffset (zero
	// new lines, header never re-read) keeps it for the cache-bloat detector's
	// version attribution. See issue #374.
	AgentVersion string `json:"agent_version,omitempty"`
	// LastEventType persists the most recent substantive event type so a
	// daemon restart that resumes at LastOffset (zero new lines) can still
	// answer IsAgentDone. Without it, a session persisted as `working` whose
	// transcript never grows again is stranded: the recomputed metrics carry
	// an empty LastEventType, IsAgentDone can never fire, and no future
	// activity arrives to re-classify. See issue #649.
	LastEventType string `json:"last_event_type,omitempty"`
	// LastAssistantText persists the tail of the most recent assistant message
	// so a daemon restart that resumes at LastOffset (zero new lines) can still
	// answer IsWaitingForUserInput. Without it, a session persisted as `waiting`
	// (turn ended on a question) loses the question text on restart and the seed
	// re-classification demotes it to `ready`. See issue #705.
	LastAssistantText string `json:"last_assistant_text,omitempty"`
}

// --- Shared helpers used by multiple parsers ---

// isUserEventType reports whether a ParsedEvent.EventType represents a user
// turn across any of the supported transcript formats.
func isUserEventType(eventType string) bool {
	switch eventType {
	case "user", "user_message", "user_input":
		return true
	}
	return false
}

// Normalized Claude model names referenced more than once by
// NormalizeModelName below (as both an alias target and a switch case).
const (
	modelClaudeOpus41   = "claude-opus-4-1"
	modelClaudeSonnet46 = "claude-sonnet-4-6"
	modelClaudeHaiku45  = "claude-haiku-4-5"
)

// NormalizeModelName normalizes model names by removing date suffixes, extended
// context markers, and handling aliases. Exported for use by adapter parsers.
func NormalizeModelName(rawModel string) string {
	if rawModel == "" {
		return ""
	}

	// Strip extended context suffix (e.g. "claude-opus-4-6[1m]")
	rawModel = strings.TrimSuffix(rawModel, "[1m]")

	// Handle common aliases first
	aliases := map[string]string{
		"opusplan": modelClaudeOpus41,
		"sonnet":   modelClaudeSonnet46,
		"haiku":    modelClaudeHaiku45,
	}
	if normalized, exists := aliases[rawModel]; exists {
		return normalized
	}

	// Remove date suffixes (e.g., "claude-opus-4-6-20250715" -> "claude-opus-4-6")
	datePattern := regexp.MustCompile(`-\d{8}$`)
	normalized := datePattern.ReplaceAllString(rawModel, "")

	// Match most-specific patterns first (longer model IDs before shorter)
	switch {
	case strings.Contains(normalized, "claude-opus-4-6"):
		return "claude-opus-4-6"
	case strings.Contains(normalized, modelClaudeSonnet46):
		return modelClaudeSonnet46
	case strings.Contains(normalized, "claude-sonnet-4-5"):
		return "claude-sonnet-4-5"
	case strings.Contains(normalized, modelClaudeHaiku45):
		return modelClaudeHaiku45
	case strings.Contains(normalized, modelClaudeOpus41):
		return modelClaudeOpus41
	case strings.Contains(normalized, "claude-sonnet-4"):
		return "claude-4-sonnet"
	case strings.Contains(normalized, "claude-3.5-sonnet"):
		return "claude-3.5-sonnet"
	case strings.Contains(normalized, "claude-3.5-haiku"):
		return "claude-3.5-haiku"
	}
	return normalized
}

// ExtractAssistantText extracts and concatenates text blocks from an assistant
// message, returning at most 200 characters. Checks both Claude Code
// (message.content[].text) and Codex (content[].text / content[].output_text) formats.
func ExtractAssistantText(raw map[string]interface{}) string {
	var parts []string

	// Claude Code: message.content[]
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if arr, ok := msg["content"].([]interface{}); ok {
			collectAssistantTextBlocks(arr, &parts)
		}
	}
	// Codex: top-level content[]
	if arr, ok := raw["content"].([]interface{}); ok {
		collectAssistantTextBlocks(arr, &parts)
	}

	return TruncateAssistantText(strings.Join(parts, " "))
}

// collectAssistantTextBlocks appends the text of each "text"/"output_text"
// content block in arr to *parts, in order. Shared by both content sources
// ExtractAssistantText scans (Claude Code's message.content[] and Codex's
// top-level content[]).
func collectAssistantTextBlocks(arr []interface{}, parts *[]string) {
	for _, item := range arr {
		if block, ok := item.(map[string]interface{}); ok {
			bt := block["type"]
			if bt == "text" || bt == "output_text" {
				if text, ok := block["text"].(string); ok && text != "" {
					*parts = append(*parts, text)
				}
			}
		}
	}
}

// ExtractUserText extracts the prose of a genuine user prompt from a parsed
// line, handling the common transcript shapes: a plain string content
// (message.content or top-level content) or an array of text blocks. It
// deliberately returns "" for a user line that carries only tool_result
// blocks (a tool response, not a prompt) so the heuristic-fallback summary
// (issue #738) is anchored on the real opening prompt, not on tool output.
// Untruncated — the caller (tailer) cleans and caps.
func ExtractUserText(raw map[string]interface{}) string {
	var parts []string
	// Claude Code: message.content (string or []block).
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		appendContentText(msg["content"], &parts)
	}
	// Codex / others: top-level content (string or []block).
	appendContentText(raw["content"], &parts)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// appendContentText appends content's text to *parts: a plain non-empty
// string is appended directly; a []interface{} is scanned block-by-block via
// collectUserTextBlocks. Any other shape (nil, object, number) is ignored.
// Shared by the two content sources ExtractUserText scans.
func appendContentText(content interface{}, parts *[]string) {
	switch c := content.(type) {
	case string:
		if c != "" {
			*parts = append(*parts, c)
		}
	case []interface{}:
		collectUserTextBlocks(c, parts)
	}
}

// collectUserTextBlocks appends the text of each "text"/"input_text" content
// block in arr to *parts, in order.
func collectUserTextBlocks(arr []interface{}, parts *[]string) {
	for _, item := range arr {
		if block, ok := item.(map[string]interface{}); ok {
			switch block["type"] {
			case "text", "input_text":
				if text, ok := block["text"].(string); ok && text != "" {
					*parts = append(*parts, text)
				}
			}
		}
	}
}

// MaxAssistantTextRunes caps the assistant text kept for waiting-state display.
const MaxAssistantTextRunes = 200

// TruncateAssistantText reduces s to the assistant text kept for waiting-state
// display: trimmed, then at most the trailing MaxAssistantTextRunes runes with a
// leading ellipsis when it drops text. Keeping the tail (not the head) preserves
// the agent's most recent words — the part that signals whether it is waiting on
// the user, and where a trailing question mark lands.
//
// This is the single display-truncation rule shared by every agent adapter.
// Adapters MUST scan the full text for markers (ScanTaskEstimate) BEFORE calling
// this — the dropped head can carry a task-estimate marker.
func TruncateAssistantText(s string) string {
	text := strings.TrimSpace(s)
	runes := []rune(text)
	if len(runes) > MaxAssistantTextRunes {
		return "…" + string(runes[len(runes)-MaxAssistantTextRunes:])
	}
	return text
}

// ExtractUsage pulls token breakdown fields from a usage map.
// Handles both standard (Claude/Codex) and Pi field naming conventions.
func ExtractUsage(usage map[string]interface{}) *TokenSnapshot {
	snap := &TokenSnapshot{}
	hasBreakdown := false

	if v, ok := usage["input_tokens"].(float64); ok {
		snap.Input = int64(v)
		hasBreakdown = true
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		snap.Output = int64(v)
		hasBreakdown = true
	}
	// Pi uses shorter field names as fallback.
	if !hasBreakdown {
		if v, ok := usage["input"].(float64); ok {
			snap.Input = int64(v)
			hasBreakdown = true
		}
		if v, ok := usage["output"].(float64); ok {
			snap.Output = int64(v)
			hasBreakdown = true
		}
	}
	// Standard cache field names.
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		snap.CacheRead = int64(v)
	}
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		snap.CacheCreation = int64(v)
	}
	// Pi cache field names.
	if v, ok := usage["cacheRead"].(float64); ok {
		snap.CacheRead = int64(v)
	}
	if v, ok := usage["cacheWrite"].(float64); ok {
		snap.CacheCreation = int64(v)
	}
	snap.Total = snap.Input + snap.Output + snap.CacheRead + snap.CacheCreation
	// total_tokens override
	if total, ok := usage["total_tokens"].(float64); ok {
		snap.Total = int64(total)
	}
	// Pi totalTokens field.
	if total, ok := usage["totalTokens"].(float64); ok {
		snap.Total = int64(total)
	}

	if !hasBreakdown && snap.Total == 0 {
		return nil
	}
	return snap
}

// ParseTimestamp extracts a timestamp from a raw JSON map, trying RFC3339
// and millisecond-precision formats, then numeric Unix timestamps.
func ParseTimestamp(raw map[string]interface{}) time.Time {
	if ts, ok := raw["timestamp"]; ok {
		if tsStr, ok := ts.(string); ok {
			if parsed, err := time.Parse(time.RFC3339, tsStr); err == nil {
				return parsed
			}
			if parsed, err := time.Parse("2006-01-02T15:04:05.000Z", tsStr); err == nil {
				return parsed
			}
		} else if tsNum, ok := ts.(float64); ok && tsNum > 0 {
			return time.Unix(int64(tsNum), 0)
		}
	}
	return time.Now()
}
