package tailer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/pkg/capacity"
)

// MessageEvent represents a single message event from transcript
type MessageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Content   string    `json:"content,omitempty"`
}

// AppliedTaskDelta records a task-list delta the tailer actually folded into
// its task list during a pass (a create or a status update). Surfaced per-pass
// on SessionMetrics so the daemon can record a task_delta lifecycle event;
// never persisted.
type AppliedTaskDelta struct {
	Op      string // create | update
	ID      string // task id (provisional at create, authoritative once assigned)
	Subject string
	Status  string
}

// SessionMetrics holds computed performance metrics
type SessionMetrics struct {
	MessagesPerMinute   float64        `json:"messages_per_minute"`
	ElapsedSeconds      int64          `json:"elapsed_seconds"`
	LastMessageAt       time.Time      `json:"last_message_at"`
	MessageHistory      []MessageEvent `json:"-"` // Sliding window, not serialized
	SessionStartAt      time.Time      `json:"session_start_at"`
	TotalTokens         int64          `json:"total_tokens,omitempty"`
	InputTokens         int64          `json:"input_tokens,omitempty"`
	OutputTokens        int64          `json:"output_tokens,omitempty"`
	CacheReadTokens     int64          `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64          `json:"cache_creation_tokens,omitempty"`
	// Cumulative token totals across all API turns (for cost calculation).
	CumInputTokens         int64   `json:"cum_input_tokens,omitempty"`
	CumOutputTokens        int64   `json:"cum_output_tokens,omitempty"`
	CumCacheReadTokens     int64   `json:"cum_cache_read_tokens,omitempty"`
	CumCacheCreationTokens int64   `json:"cum_cache_creation_tokens,omitempty"`
	EstimatedCostUSD       float64 `json:"estimated_cost_usd,omitempty"`
	// EstimatedCO2Grams and CO2Tier mirror EstimatedCostUSD's cumulative-token
	// derivation (issue #829) — see capacity.EstimateCO2Grams. CO2Tier stores
	// capacity.CO2Tier's string value; kept plain so this package doesn't take
	// on more of capacity's API surface than the cost fields already do.
	EstimatedCO2Grams  float64 `json:"estimated_co2_grams,omitempty"`
	CO2Tier            string  `json:"co2_tier,omitempty"`
	ModelName          string  `json:"model_name,omitempty"`
	AgentVersion       string  `json:"agent_version,omitempty"`
	ContextWindow      int64   `json:"context_window,omitempty"`
	ContextUtilization float64 `json:"context_utilization_percentage,omitempty"`
	PressureLevel      string  `json:"pressure_level,omitempty"` // "safe", "caution", "warning", "critical"

	// ContextWindowUnknown is true when ContextWindow is the 32k sentinel
	// fallback (no LiteLLM pricing for this model) rather than a known
	// value. The macOS app uses this to render a tentative bar (dashed
	// outline / "~" prefix). See computeContextUtilization in
	// tailer_metrics.go.
	ContextWindowUnknown bool `json:"context_window_unknown,omitempty"`

	// Raw event data for real-time client-side calculations
	TotalEventCount        int64     `json:"total_event_count,omitempty"`
	RecentEventCount       int64     `json:"recent_event_count,omitempty"`
	RecentEventWindowStart time.Time `json:"recent_event_window_start,omitempty"`

	// Tool call tracking — count unmatched tool_use/tool_result pairs
	HasOpenToolCall   bool `json:"has_open_tool_call"`
	OpenToolCallCount int  `json:"open_tool_call_count,omitempty"`

	// OpenSubagents is the number of in-process child agents currently
	// running. The tailer leaves this at zero; adapters populate it from
	// LastOpenToolNames or whatever adapter-specific signal they use.
	OpenSubagents int `json:"open_subagents,omitempty"`

	// BackgroundProcessCount is the number of agent-spawned background
	// processes the transcript shows as still open (Bash run_in_background
	// launches not yet observed terminating). Derived from the
	// openBackgroundProcs set. See issue #445.
	BackgroundProcessCount int `json:"background_process_count,omitempty"`

	// BackgroundProcessOutputs holds the output-file paths of those open
	// background processes, sorted for determinism. The daemon's liveness
	// probe lsof's them. Not serialized — recomputed each pass. See issue #445.
	BackgroundProcessOutputs []string `json:"-"`

	// BackgroundProcessPIDs holds the OS PIDs of those open background
	// processes whose adapter reports a PID rather than an output file (Gemini
	// CLI hides backgrounded output and surfaces only "(PID: N)"). The daemon's
	// liveness probe signals these directly. Sorted for determinism, not
	// serialized — recomputed each pass. See issue #661.
	BackgroundProcessPIDs []string `json:"-"`

	// SubagentCompletions surfaces parent-side "subagent done" signals
	// discovered during the most recent TailAndProcess() pass. Cleared at
	// the start of every pass so the detector drains fresh events only.
	// See issue #134.
	SubagentCompletions []SubagentCompletion `json:"-"`

	// AppliedTaskDeltas surfaces the task-list deltas the tailer folded into the
	// session's task list during the most recent pass — one per applied
	// create/update. Cleared at the start of every pass (same per-pass contract
	// as SubagentCompletions) so the detector records each task_delta lifecycle
	// event exactly once. Not serialized.
	AppliedTaskDeltas []AppliedTaskDelta `json:"-"`

	// LastEventType is the event type of the most recent message event in
	// the transcript (e.g. "assistant", "user", "tool_use", "tool_result").
	// Used for content-based working/waiting detection.
	LastEventType string `json:"last_event_type,omitempty"`

	// LastOpenToolNames holds the tool names from the most recent assistant
	// message that called tools. Cleared when a user message appears.
	// Used to detect user-blocking tools (AskUserQuestion, ExitPlanMode).
	LastOpenToolNames []string `json:"last_open_tool_names,omitempty"`

	// LastWasUserInterrupt is true when the most recent user event was a
	// real ESC cancellation (the exact "[Request interrupted by user]" text
	// marker, without the "for tool use" suffix). Reset when any subsequent
	// non-interrupt user event arrives. The classifier uses this to
	// transition working/waiting → ready on genuine interrupts without
	// being fooled by normal tool failures or tool denials.
	LastWasUserInterrupt bool `json:"last_was_user_interrupt"`

	// LastWasToolDenial is true when the most recent user event was a tool
	// denial — the user clicked "no" on a permission prompt, producing the
	// "[Request interrupted by user for tool use]" text marker. Distinct
	// from LastWasUserInterrupt because a denial does NOT end the agent's
	// turn (the agent typically continues with a different approach), so
	// it must not feed the cancellation rule. Surfaced for observability
	// and replay-harness flicker categorization.
	LastWasToolDenial bool `json:"last_was_tool_denial,omitempty"`

	// LastCWD is the most recent working directory seen in the transcript.
	// Extracted during parsing so callers don't need a separate file read.
	LastCWD string `json:"last_cwd,omitempty"`

	// LastAssistantText is the text content of the most recent assistant
	// message, truncated to ~200 characters.
	LastAssistantText string `json:"last_assistant_text,omitempty"`

	// PermissionMode is the session's permission mode (e.g. "default",
	// "plan", "bypassPermissions"). Extracted from "permission-mode" events.
	PermissionMode string `json:"permission_mode,omitempty"`

	// SawUserBlockingToolClosedThisPass is true when an AskUserQuestion or
	// ExitPlanMode tool opened and closed within a single TailAndProcess
	// call — the fswatcher-coalesce case where HasOpenToolCall is already
	// false by the time the classifier runs, collapsing the waiting
	// episode. Per-pass transient; daemon uses it to synthesise the
	// missing working→waiting step (issue #150).
	SawUserBlockingToolClosedThisPass bool `json:"-"`

	// SawMidPassTurnBoundary is true when a "turn_done" event was followed
	// by further substantive transcript activity within the SAME
	// TailAndProcess call — a genuinely distinct subsequent turn began (and
	// possibly also completed) before the classifier ever saw the
	// intervening ready state. This is the batch-scan analog of
	// SawUserBlockingToolClosedThisPass (#150): an agent whose message queue
	// drains a follow-up prompt synchronously, with no observable ready gap
	// between the two turns (e.g. mistral-vibe's in-memory queue, issue
	// #988), would otherwise have both turns collapsed into one
	// working→ready span. Per-pass transient; daemon uses it to synthesise
	// the missing ready→working step.
	SawMidPassTurnBoundary bool `json:"-"`

	// NoSubstantiveActivity is true when a TailAndProcess pass consumed new
	// transcript content but every parsed line was Skip=true and produced no
	// state-relevant change (no SubagentCompletions, no TaskSnapshot, no
	// processParsedEvent call). Lets the detector treat post-turn writes
	// like Claude Code's `system/away_summary` recap as activity for
	// timestamp purposes only — the state machine must not be re-run, since
	// LastEventType still carries the prior turn_done and rule 4 would
	// bounce a ready session back to working. Per-pass transient
	// (issue #329).
	NoSubstantiveActivity bool `json:"-"`

	// SawManualCompactBoundary is true when this pass parsed a manual
	// compact_boundary (user-invoked /compact) — the burst-written marker
	// Claude Code flushes when compaction finishes. Per-pass transient; the
	// detector uses it to clear the PreCompact force-working hold so the
	// session releases working → ready (#657, paired with #656). Auto-compaction
	// never sets it.
	SawManualCompactBoundary bool `json:"-"`

	// Tasks is the current task list for this session, accumulated from
	// TaskCreate / TaskUpdate tool_use events in the Claude Code transcript.
	// Nil for sessions that have not called TaskCreate.
	Tasks []Task `json:"tasks,omitempty"`

	// RateLimit is the most recent rate-limit snapshot observed for this
	// session. Populated by parsers that surface subscription quota (codex
	// from token_count events) and by the Claude Code statusline hook
	// receiver (which calls IngestRateLimit directly). Nil when no
	// snapshot has been seen.
	RateLimit *RateLimitSnapshot `json:"rate_limit,omitempty"`

	// RateLimitHistory is a small rolling buffer of changed snapshots used
	// to compute a burn-rate forecast. Sample-on-change: duplicates of the
	// most recent used_percent values are dropped before append, so the
	// slope calculation isn't diluted by zero-delta statusline ticks
	// (issue #309). Capped at rateLimitHistoryCap entries.
	RateLimitHistory []RateLimitSnapshot `json:"-"`

	// TaskEstimate is the most recent agent-emitted task-progress marker
	// (issue #558). Sporadic like RateLimit — the last one seen persists
	// across passes. Nil when the session never emitted a marker.
	TaskEstimate *TaskEstimate `json:"task_estimate,omitempty"`

	// TaskEstimateBase is the first marker of the current task — the rate
	// baseline for the completion forecast. Computation-only (the API
	// surfaces the derived eta, not the baseline).
	TaskEstimateBase *TaskEstimate `json:"-"`

	// TaskSummary is the most recent agent-emitted task-summary marker
	// (issue #738) — the one-line "what is this session about". Sporadic like
	// TaskEstimate; the last one seen persists across passes. Nil when the
	// session never emitted a summary marker.
	TaskSummary *TaskSummary `json:"task_summary,omitempty"`

	// FirstUserText is the first user message of the session, truncated. Used
	// as a daemon-side heuristic fallback for the surfaced task summary when
	// no marker is present (issue #738) — e.g. agents that don't emit one.
	FirstUserText string `json:"first_user_text,omitempty"`

	// TaskQuestion is the most recent agent-emitted task-question marker
	// (issue #759) — the terse one-line version of the question the agent is
	// blocked on. Sporadic like TaskSummary; the last one seen persists across
	// passes and is cleared on a real user message. Nil when the session never
	// emitted a question marker; the surfaced waiting headline then falls back
	// to compacting the raw last-assistant text.
	TaskQuestion *TaskQuestion `json:"task_question,omitempty"`

	// AwaySummary is Claude Code's own idle recap, once it has arrived (issue
	// #979) — a passive, higher-quality alternative source for the surfaced
	// waiting headline when no agent marker is present. Cleared on a real
	// user message, like TaskQuestion.
	AwaySummary *AwaySummary `json:"away_summary,omitempty"`
}

// TranscriptTailer monitors transcript files and computes metrics.
// Format-specific parsing is delegated to a TranscriptParser.
type TranscriptTailer struct {
	path        string
	lastOffset  int64
	metrics     *SessionMetrics
	windowSize  time.Duration // Default 60 seconds
	capacityMgr *capacity.CapacityManager

	// parser handles format-specific line parsing (Claude Code, Codex, Pi).
	parser TranscriptParser

	// adapter name for model config fallback.
	adapter string

	// disableModelConfigFallback turns off the getDefaultModelFromConfig
	// lookup. The daemon leaves it false so a live session with no in-band
	// model still surfaces the operator's configured default. The replay
	// path sets it true (DisableModelConfigFallback) so a committed fixture's
	// output reflects only the transcript — never the operator's local
	// ~/.claude/settings.json — keeping byte-identity goldens reproducible
	// across machines and CI (issue #440).
	disableModelConfigFallback bool

	// Context window override from transcript or extended context model suffix.
	contextWindowOverride int64

	// openToolCalls is the single source of truth for currently-open tool
	// calls. Keyed by tool call ID; value is the tool name. Tool uses
	// insert by ID (idempotent — duplicate IDs overwrite), tool results
	// delete by ID (orphan IDs are harmless no-ops). HasOpenToolCall and
	// OpenToolCallCount are derived from len(openToolCalls).
	//
	// Historical note: this was originally paired integer counters
	// (toolUseCount/toolResultCount, see #102), then a []string FIFO
	// (lastOpenToolNames, see #114). Both had the same structural weakness:
	// no correlation between a tool_result and the tool_use it pertains to.
	// The id-keyed map eliminates phantom entries from orphan results,
	// duplicate tool_use events (multi-line splits), and out-of-order
	// parallel tool closures. See issue #117.
	openToolCalls map[string]string

	// Token breakdown accumulators (latest snapshot, not cumulative).
	// Used for context utilization — always reflects the most recent API turn.
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64

	// Cumulative token accumulators for cost calculation.
	// These sum the FINAL usage from each unique API turn (requestId).
	// Preserved for the legacy fallback path (testParser, non-Contribution events).
	cumInputTokens         int64
	cumOutputTokens        int64
	cumCacheReadTokens     int64
	cumCacheCreationTokens int64

	// Deduplication: track requestId to avoid double-counting streaming events
	// within a single API turn. Used by the legacy fallback path.
	lastRequestID   string
	pendingSnapshot *TokenSnapshot // latest snapshot for current requestId; flushed on ID change

	// New accumulation path: per-model usage breakdown from PerTurnContribution.
	// Populated when adapters emit Contribution on ParsedEvent (Phase 2+).
	cumByModel         map[string]*UsageBreakdown
	cumProviderCostUSD float64 // sum of provider-reported per-turn costs (Pi)

	// lastWasUserInterrupt tracks whether the most recent user event was
	// an ESC cancellation (the exact "[Request interrupted by user]" text
	// marker — NOT the "for tool use" suffix variant).
	lastWasUserInterrupt bool

	// lastWasToolDenial tracks whether the most recent user event was a
	// tool denial ("[Request interrupted by user for tool use]" marker).
	// Kept distinct from lastWasUserInterrupt so the cancellation rule
	// only fires on real ESC, not on denials (which don't end the turn).
	lastWasToolDenial bool

	// lastCWD tracks the most recent working directory seen in transcript lines.
	lastCWD string

	// lastAssistantText holds the text content of the most recent assistant
	// message, truncated to ~200 characters.
	lastAssistantText string

	// lastTaskEstimate holds the most recent agent-emitted task-progress
	// marker. Markers are sporadic (model-discretion, every few turns), so
	// the last one seen persists across passes that carry no fresh marker.
	// See issue #558.
	lastTaskEstimate *TaskEstimate

	// firstTaskEstimate is the first marker of the CURRENT task — the rate
	// baseline. Measuring perRound from marker deltas (latest − first)
	// instead of session elapsed keeps multi-task sessions honest: after a
	// reset, session elapsed includes all previous tasks and idle gaps and
	// inflated projections ~2×. Re-anchored when completed_rounds goes
	// backwards (agent started a new count without a user prompt).
	firstTaskEstimate *TaskEstimate

	// lastTaskSummary holds the most recent agent-emitted task-summary marker
	// (issue #738). Sporadic like lastTaskEstimate — the last one seen
	// persists across markerless passes; cleared on a real user message.
	lastTaskSummary *TaskSummary

	// firstUserText holds the first user message of the session, captured
	// once and never overwritten — the heuristic fallback for the surfaced
	// task summary when no marker is present (issue #738).
	firstUserText string

	// lastTaskQuestion holds the most recent agent-emitted task-question marker
	// (issue #759). Sporadic like lastTaskSummary — the last one seen persists
	// across markerless passes; cleared on a real user message.
	lastTaskQuestion *TaskQuestion

	// lastAwaySummary holds Claude Code's own idle recap, once it has arrived
	// (issue #979). Sporadic and lower-priority than lastTaskQuestion — the
	// last one seen persists across passes; cleared on a real user message.
	lastAwaySummary *AwaySummary

	// tasks accumulates the session's task list from TaskCreate / TaskUpdate
	// tool_use events parsed by the Claude Code adapter.
	tasks []Task
	// taskSeq is the provisional ID counter for TaskCreate. The authoritative
	// ID arrives one line later in the create's tool_result (assign_id delta)
	// and replaces the provisional one; the counter is only the fallback for
	// transcripts that never carry toolUseResult.task.id. NOT len(tasks):
	// completed batches are pruned by reconcileTaskSnapshot while Claude's
	// numbering keeps climbing. See issue #615.
	taskSeq int
	// pendingTaskCreates maps a TaskCreate's tool_use id to the provisional
	// task ID it was assigned, so the tool_result's authoritative ID
	// (assign_id delta) can be applied to the right task. Entries resolve on
	// the result line either way. See issue #615.
	pendingTaskCreates map[string]string

	// openBackgroundProcs is the set of agent-spawned background processes
	// still believed running, keyed by background id with the output-file
	// path as value. A BackgroundSpawn inserts; a matched terminated
	// BashOutput poll or a KillShell removes. BackgroundProcessCount and
	// BackgroundProcessOutputs are derived from it. See issue #445.
	openBackgroundProcs map[string]string
	// pendingBashPolls maps a BashOutput poll's tool_use id to the background
	// id it targets, so a later tool_result reporting a terminated status can
	// be attributed to the right background process. See issue #445.
	pendingBashPolls map[string]string

	// lastLineSeenAt is the wall-clock time at which the parser last consumed
	// a transcript line. Used by idleFlusher-implementing parsers (aider) to
	// synthesize turn_done when the file has been quiet long enough. Zero
	// means no line has been seen yet.
	lastLineSeenAt time.Time

	// rateLimit is the most recent snapshot observed; rateLimitHistory holds
	// the rolling sample-on-change buffer (capped at rateLimitHistoryCap).
	// In-memory only — after a daemon restart, the next few token_count or
	// statusline events repopulate the history before forecasting resumes.
	rateLimit        *RateLimitSnapshot
	rateLimitHistory []RateLimitSnapshot
}

// rateLimitHistoryCap caps the rolling history at a small number of changed
// samples. Larger windows blur the slope across burn-rate regime changes; a
// 5-sample window is responsive to recent activity without being thrown off
// by a single jumpy reading.
const rateLimitHistoryCap = 5

// NewTranscriptTailer creates a new tailer for the given transcript path.
// The parser handles format-specific line parsing; adapter is used for model
// config fallback.
func NewTranscriptTailer(path string, parser TranscriptParser, adapter string) *TranscriptTailer {
	if pa, ok := parser.(TranscriptPathAware); ok {
		pa.SetTranscriptPath(path)
	}
	return &TranscriptTailer{
		path:                path,
		lastOffset:          0,
		capacityMgr:         capacity.DefaultCapacityManager(),
		parser:              parser,
		adapter:             adapter,
		openToolCalls:       make(map[string]string),
		openBackgroundProcs: make(map[string]string),
		pendingBashPolls:    make(map[string]string),
		pendingTaskCreates:  make(map[string]string),
		cumByModel:          make(map[string]*UsageBreakdown),
		metrics: &SessionMetrics{
			MessageHistory: make([]MessageEvent, 0),
			SessionStartAt: time.Time{},
		},
		windowSize: 60 * time.Second,
	}
}

// DisableModelConfigFallback stops the tailer from reading the operator's
// per-agent config (~/.claude/settings.json and friends) to fill in a missing
// model name. The replay tool calls this so committed fixtures replay
// identically regardless of the machine's local config (issue #440).
func (t *TranscriptTailer) DisableModelConfigFallback() {
	t.disableModelConfigFallback = true
}

// TailAndProcess reads new transcript content from the last offset (or from the
// beginning on first open) and processes each JSONL line via the parser.
func (t *TranscriptTailer) TailAndProcess() (*SessionMetrics, error) {
	file, err := os.Open(t.path)
	if err != nil {
		return nil, fmt.Errorf("failed to open transcript: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat transcript: %w", err)
	}
	fileSize := stat.Size()

	// Per-pass signals must be cleared so the detector only drains events
	// discovered in this scan (see issue #134).
	t.metrics.SubagentCompletions = nil
	t.metrics.AppliedTaskDeltas = nil

	startPos := int64(0)
	switch {
	case fileSize < t.lastOffset:
		// File rotated/truncated — reset cumulative accumulators to avoid
		// double-counting tokens from the previous file.
		startPos = 0
		t.resetAccumulatorsForRotation()
	case t.lastOffset > 0:
		// Normal incremental path: never skip ahead of the last processed byte.
		startPos = t.lastOffset
	}

	if _, err := file.Seek(startPos, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to seek transcript: %w", err)
	}

	// bufio.Reader (not Scanner) so a single oversized JSONL line can't wedge
	// the tailer. Lines above maxTranscriptLineSize are skipped: the offset is
	// advanced past them and processing continues. See issue #270.
	reader := bufio.NewReaderSize(file, 64*1024)
	scan := t.scanNewLines(reader, startPos)
	t.lastOffset = scan.endOffset

	// Idle-flush hook: parsers whose transcript has no in-band end-of-turn
	// marker (currently aider) synthesize turn_done when the file has been
	// quiet for long enough. The parser owns the threshold and decides
	// whether to flush; the tailer just routes the resulting event through
	// the normal processing path so tool sweeps and LastEventType update
	// the same way they do for an in-band turn_done.
	if flusher, ok := t.parser.(idleFlusher); ok && !t.lastLineSeenAt.IsZero() {
		if ev := flusher.IdleFlush(time.Since(t.lastLineSeenAt)); ev != nil {
			t.processParsedEvent(ev, &scan.sawUserBlockingClosed)
			scan.substantive = true
		}
	}

	// Compute current metrics.
	t.computeMetrics()
	t.metrics.SawUserBlockingToolClosedThisPass = scan.sawUserBlockingClosed
	t.metrics.SawMidPassTurnBoundary = scan.sawMidPassTurnBoundary
	// NoSubstantiveActivity is set only when at least one parsed line was seen
	// AND none of them produced substantive output (no processParsedEvent
	// call, no subagent completion, no task snapshot). An empty pass (zero
	// parsed lines, e.g. fswatcher fired on an unchanged file) leaves the flag
	// false so the detector's classifier still runs — needed for hook-driven
	// synthetic activity events that re-classify against stale metrics. See
	// issue #329.
	t.metrics.NoSubstantiveActivity = scan.linesParsed > 0 && !scan.substantive
	t.metrics.SawManualCompactBoundary = scan.sawManualCompact

	// Model config fallback. Skipped on the replay path (see
	// disableModelConfigFallback) so fixture output stays hermetic.
	if t.metrics.ModelName == "" && !t.disableModelConfigFallback {
		if defaultModel := getDefaultModelFromConfig(t.adapter); defaultModel != "" {
			t.metrics.ModelName = defaultModel
		}
	}

	t.computeContextUtilization()

	return t.metrics, scan.err
}

// resetAccumulatorsForRotation clears every accumulator that belongs to the
// previous transcript file's contents. TailAndProcess calls this when it
// detects fileSize < lastOffset — the transcript was rotated or truncated —
// so replaying from byte 0 doesn't double-count tokens, resurrect stale
// background processes, or misattribute the previous file's tasks.
func (t *TranscriptTailer) resetAccumulatorsForRotation() {
	t.cumInputTokens = 0
	t.cumOutputTokens = 0
	t.cumCacheReadTokens = 0
	t.cumCacheCreationTokens = 0
	t.lastRequestID = ""
	t.pendingSnapshot = nil
	t.cumByModel = make(map[string]*UsageBreakdown)
	t.cumProviderCostUSD = 0
	t.tasks = nil
	t.taskSeq = 0
	t.pendingTaskCreates = make(map[string]string)
	// Background-process set belongs to the prior file; drop it so a
	// rotated/truncated transcript doesn't keep a stale session `working`.
	// See issue #445.
	t.openBackgroundProcs = make(map[string]string)
	t.pendingBashPolls = make(map[string]string)
	// Drop the pre-rotation idle anchor so the post-scan idleFlusher
	// hook doesn't synthesize a phantom turn_done against stale time.
	t.lastLineSeenAt = time.Time{}
}

// transcriptScanResult carries the outcomes of one scanNewLines pass: how far
// the offset advanced and the per-pass signals TailAndProcess folds into the
// returned SessionMetrics.
type transcriptScanResult struct {
	endOffset             int64
	linesParsed           int
	substantive           bool
	sawManualCompact      bool
	sawUserBlockingClosed bool
	// sawMidPassTurnBoundary and turnDoneSeen implement
	// SessionMetrics.SawMidPassTurnBoundary's detection: turnDoneSeen is
	// scan-local bookkeeping (was the previous substantive event a
	// turn_done?), set by scanParsedLine after each event; sawMidPassTurnBoundary
	// latches true the first time a later substantive event follows one.
	sawMidPassTurnBoundary bool
	turnDoneSeen           bool
	err                    error
}

// scanNewLines reads and processes every complete line available from reader
// (starting at startPos), stopping at clean EOF, a partial trailing line, or
// a read error. Each line is parsed via parseTranscriptLine and then routed
// to applySkippedEvent (Skip=true or unparseable) or processParsedEvent
// (a real event) — the same split TailAndProcess used to perform inline.
func (t *TranscriptTailer) scanNewLines(reader *bufio.Reader, startPos int64) transcriptScanResult {
	res := transcriptScanResult{endOffset: startPos}
	rawLineParser, isRawLine := t.parser.(RawLineParser)

	for {
		raw, consumed, lineErr := readLineCapped(reader, maxTranscriptLineSize)
		switch t.classifyLineReadError(lineErr, consumed, &res) {
		case lineReadStop:
			return res
		case lineReadSkip:
			continue
		}

		res.endOffset += consumed
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}

		t.lastLineSeenAt = time.Now()
		t.scanParsedLine(line, isRawLine, rawLineParser, &res)
	}
}

// lineReadAction tells scanNewLines' loop what to do after readLineCapped
// returns, as classified by classifyLineReadError.
type lineReadAction int

const (
	// lineReadContinueProcessing means a full line was read (lineErr == nil);
	// the caller proceeds to trim and process it.
	lineReadContinueProcessing lineReadAction = iota
	// lineReadSkip means the line was handled in full (an oversized line was
	// discarded) and the loop should move on to the next one.
	lineReadSkip
	// lineReadStop means the scan is done: clean EOF, a partial trailing
	// line, or a real read error.
	lineReadStop
)

// classifyLineReadError interprets the error from readLineCapped and applies
// its side effects (advancing endOffset past a skipped oversized line,
// recording a real read error onto res), returning what the caller should do
// next.
func (t *TranscriptTailer) classifyLineReadError(lineErr error, consumed int64, res *transcriptScanResult) lineReadAction {
	switch {
	case errors.Is(lineErr, io.EOF) || errors.Is(lineErr, errPartialAtEOF):
		// EOF (clean) or partial trailing line — stop without advancing past
		// the partial bytes; they'll be re-read next tick once more data is
		// appended.
		return lineReadStop
	case errors.Is(lineErr, errLineTooLong):
		log.Printf("irrlicht/tailer: skipping oversized line at offset %d (%d bytes) in %s", res.endOffset, consumed, t.path)
		res.endOffset += consumed
		return lineReadSkip
	case lineErr != nil:
		res.err = lineErr
		return lineReadStop
	default:
		return lineReadContinueProcessing
	}
}

// scanParsedLine parses a single non-empty, already-offset-accounted
// transcript line and routes it to either applySkippedEvent (Skip=true or
// unparseable) or processParsedEvent (a real event), folding the outcome
// into res.
func (t *TranscriptTailer) scanParsedLine(line string, isRawLine bool, rawLineParser RawLineParser, res *transcriptScanResult) {
	parsed := t.parseTranscriptLine(line, isRawLine, rawLineParser)
	if parsed == nil || parsed.Skip {
		if parsed != nil {
			res.linesParsed++
			if t.applySkippedEvent(parsed) {
				res.substantive = true
			}
		}
		return
	}
	res.linesParsed++
	res.substantive = true
	if parsed.IsManualCompactBoundary {
		res.sawManualCompact = true
	}
	t.updateMidPassTurnBoundary(parsed, res)
	t.processParsedEvent(parsed, &res.sawUserBlockingClosed)
}

// updateMidPassTurnBoundary implements SessionMetrics.SawMidPassTurnBoundary's
// detection (see transcriptScanResult), gated on the parser's queuedTurnSplitter
// opt-in. Split out of scanParsedLine to keep that function's branching flat.
func (t *TranscriptTailer) updateMidPassTurnBoundary(parsed *ParsedEvent, res *transcriptScanResult) {
	qs, ok := t.parser.(queuedTurnSplitter)
	if !ok || !qs.SplitsQueuedFollowUpTurns() {
		return
	}
	if res.turnDoneSeen {
		res.sawMidPassTurnBoundary = true
	}
	res.turnDoneSeen = parsed.EventType == "turn_done"
}

// parseTranscriptLine converts a single trimmed transcript line into a
// ParsedEvent, or nil for a line that carries no event at all (JSONL noise
// that isn't even a JSON object, or a line the format-specific parser
// declined to interpret). A nil result is distinct from a non-nil Skip=true
// event: the latter still carries metadata applySkippedEvent must apply.
func (t *TranscriptTailer) parseTranscriptLine(line string, isRawLine bool, rawLineParser RawLineParser) *ParsedEvent {
	if isRawLine {
		// Markdown / non-JSONL formats: parser sees the trimmed line directly.
		return rawLineParser.ParseLineRaw(line)
	}
	// Quick JSON check.
	if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
		return nil
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil
	}
	// Delegate to format-specific parser.
	return t.parser.ParseLine(raw)
}

// applySkippedEvent folds a Skip=true event's side effects into tailer state.
// Even skipped events carry metadata the tailer must apply: model changes,
// CWD, subagent completions, and authoritative task snapshots — task-
// notification lines are deliberately marked Skip=true so they don't
// pollute message-event tracking, but the signals they carry must still
// surface to the detector (issue #134). Likewise, task_reminder attachments
// are Skip=true but carry an authoritative TaskSnapshot the tailer must
// apply (issue #282). Returns true when the event counts as substantive
// activity for this pass.
func (t *TranscriptTailer) applySkippedEvent(parsed *ParsedEvent) bool {
	t.applyMetadata(parsed)
	substantive := false
	if len(parsed.SubagentCompletions) > 0 {
		t.metrics.SubagentCompletions = append(t.metrics.SubagentCompletions, parsed.SubagentCompletions...)
		substantive = true
	}
	// Background-process completion arrives on a Skip=true task-notification
	// (origin.kind or queued_command attachment) — drain it here so the count
	// drops and the pass is substantive enough for the detector to
	// re-classify and release the hold. See issue #445.
	if len(parsed.TerminatedBackgroundTaskIDs) > 0 {
		for _, id := range parsed.TerminatedBackgroundTaskIDs {
			delete(t.openBackgroundProcs, id)
		}
		substantive = true
	}
	if parsed.TaskSnapshot != nil {
		substantive = true
	}
	// away_summary arrives on a Skip=true system event by design (it must
	// never be promoted to turn_done, issue #329) — but its content is still
	// worth reading, so it's applied here without flipping substantive: a
	// passive data upgrade, not activity that should reset any idle clock.
	if parsed.AwaySummary != nil {
		t.applyAwaySummary(parsed.AwaySummary)
	}
	t.reconcileTaskSnapshot(parsed)
	return substantive
}

// forceIdleFlushDuration is passed to IdleFlush from FlushIdle. Any
// reasonable parser threshold is comfortably below this, so the flusher
// returns its synthesized event whenever a turn is open.
const forceIdleFlushDuration = 24 * time.Hour

// FlushIdle forces the idleFlusher hook regardless of the parser's own
// wall-clock threshold, processing any synthesized event through the same
// pipeline TailAndProcess uses. Returns the updated metrics and a bool
// indicating whether the flusher actually emitted an event (true) or
// returned nil / wasn't implemented (false). Callers use the bool to
// decide whether to re-run downstream classification.
//
// Production daemons rely on the threshold check inside TailAndProcess;
// this method exists for callers (the replay tool, end-of-stream
// shutdown paths) that need to drain the parser's pending turn without
// waiting real wall-clock time.
func (t *TranscriptTailer) FlushIdle() (*SessionMetrics, bool) {
	flusher, ok := t.parser.(idleFlusher)
	if !ok {
		return t.metrics, false
	}
	ev := flusher.IdleFlush(forceIdleFlushDuration)
	if ev == nil {
		return t.metrics, false
	}
	sawUserBlockingClosed := false
	t.processParsedEvent(ev, &sawUserBlockingClosed)
	t.computeMetrics()
	if sawUserBlockingClosed {
		t.metrics.SawUserBlockingToolClosedThisPass = true
	}
	// Deliberately no getDefaultModelFromConfig fallback here (unlike
	// TailAndProcess): the replay tool is the primary caller and must stay
	// hermetic (issue #440). If a fallback is ever added, gate it on
	// disableModelConfigFallback so replay doesn't read operator config.
	t.computeContextUtilization()
	return t.metrics, true
}

// reconcileTaskSnapshot applies a Claude Code task_reminder snapshot from
// parsed (when present) to the running tasks slice. Reminders are
// authoritative over local state:
//
//  1. Prune: a local task whose ID is missing from the snapshot is removed.
//     Claude Code's UI only shows the current batch — when it stops tracking
//     a task, we drop it too. This both fixes the phantom in_progress bug
//     from #282 (no entry means no stuck status) and prevents unbounded dot
//     accumulation in long sessions (#389).
//  2. Present divergence: for any ID present in the snapshot whose status
//     differs from local state, the snapshot's status wins. Claude's view
//     is authoritative whenever it speaks. Safe under transcript ordering
//     because reminders appear on user turns, after the assistant tool_use
//     that emitted any deltas for that turn.
//
// Snapshots that mention IDs we never saw a TaskCreate for are ignored;
// the reconcile only acts on pre-existing tasks. Task IDs come from the
// TaskCreate tool_result (assign_id delta), so they match Claude's
// numbering even when the tailer started mid-session (issue #615).
// See issues #282 and #389.
// eventUnix is the unix-seconds time of a parsed event, 0 when the event
// carries no timestamp (a 0 CompletedAt means "unstamped", never a stamp at
// the zero time).
func eventUnix(parsed *ParsedEvent) int64 {
	if parsed == nil || parsed.Timestamp.IsZero() {
		return 0
	}
	return parsed.Timestamp.Unix()
}

func (t *TranscriptTailer) reconcileTaskSnapshot(parsed *ParsedEvent) {
	if parsed == nil || parsed.TaskSnapshot == nil || len(t.tasks) == 0 {
		return
	}
	snapByID := make(map[string]TaskSnapshotEntry, len(*parsed.TaskSnapshot))
	for _, entry := range *parsed.TaskSnapshot {
		snapByID[entry.ID] = entry
	}
	kept := make([]Task, 0, len(t.tasks))
	for i := range t.tasks {
		entry, present := snapByID[t.tasks[i].ID]
		if !present {
			// Quiet on the common case (completed tasks pruned at batch
			// turnover). Pending/in_progress prunes still log — they're
			// the #282-style "Claude dropped a task it claimed to track"
			// signal worth surfacing.
			if t.tasks[i].Status != TaskStatusCompleted {
				log.Printf("irrlicht/tailer: pruning %s task id=%s subject=%q (absent from task_reminder snapshot) in %s", t.tasks[i].Status, t.tasks[i].ID, t.tasks[i].Subject, t.path)
			}
			continue
		}
		if entry.Status != "" && entry.Status != t.tasks[i].Status {
			log.Printf("irrlicht/tailer: reconciling task id=%s status %s → %s from task_reminder in %s", t.tasks[i].ID, t.tasks[i].Status, entry.Status, t.path)
			if entry.Status == TaskStatusCompleted {
				t.tasks[i].CompletedAt = eventUnix(parsed)
			}
			t.tasks[i].Status = entry.Status
		}
		kept = append(kept, t.tasks[i])
	}
	t.tasks = kept
}

// processParsedEvent applies a single non-skipped ParsedEvent to the tailer's
// running state: tool tracking, task deltas, background-process tracking,
// turn_done sweep, interrupt/denial flags, metadata, assistant-text and
// marker bookkeeping, and message-event recording. Called once per non-Skip
// event from TailAndProcess — both for events parsed from a transcript line
// and for events synthesized post-scan via the idleFlusher hook. Caller must
// have already drained SubagentCompletions if applicable.
//
// The steps below run in a fixed order for a reason: applyTaskDeltas' trailing
// cleanup relies on ToolResultIDs from the same event as any assign_id delta;
// sweepOpenToolCallsOnTurnDone must see openToolCalls after
// applyToolCallDeltas has fully applied this event's tool_use/tool_result
// pairs.
func (t *TranscriptTailer) processParsedEvent(parsed *ParsedEvent, sawUserBlockingClosed *bool) {
	if len(parsed.SubagentCompletions) > 0 {
		t.metrics.SubagentCompletions = append(t.metrics.SubagentCompletions, parsed.SubagentCompletions...)
	}

	t.applyToolCallDeltas(parsed, sawUserBlockingClosed)
	t.applyTaskDeltas(parsed)
	t.applyBackgroundProcessDeltas(parsed)
	t.reconcileTaskSnapshot(parsed)
	t.sweepOpenToolCallsOnTurnDone(parsed)
	t.updateInterruptAndDenialFlags(parsed)
	t.applyMetadata(parsed)
	t.applyAssistantTextAndMarkers(parsed)

	t.addMessageEvent(MessageEvent{
		Timestamp: parsed.Timestamp,
		EventType: parsed.EventType,
	})
}

// applyToolCallDeltas applies the parser's tool tracking deltas to
// openToolCalls, the id-keyed single source of truth for currently-open tool
// calls. tool_use events insert by ID (idempotent: duplicate IDs from
// multi-line splits overwrite), tool_result events delete by ID (orphan IDs
// with no matching entry are harmless no-ops). This eliminates the old
// FIFO's structural weakness where out-of-order or orphan results could pop
// unrelated entries. See issue #117.
func (t *TranscriptTailer) applyToolCallDeltas(parsed *ParsedEvent, sawUserBlockingClosed *bool) {
	for _, tu := range parsed.ToolUses {
		if tu.ID != "" {
			t.openToolCalls[tu.ID] = tu.Name
		}
	}
	for _, id := range parsed.ToolResultIDs {
		if name, ok := t.openToolCalls[id]; ok && isUserBlockingToolName(name) {
			*sawUserBlockingClosed = true
		}
		delete(t.openToolCalls, id)
	}
	if parsed.ClearToolNames && len(parsed.ToolResultIDs) == 0 {
		t.openToolCalls = make(map[string]string)
	}
}

// applyTaskDeltas folds the parser's TaskCreate/AssignID/Update deltas into
// the running tasks slice and records each applied delta on
// AppliedTaskDeltas for the detector's task_delta lifecycle events.
func (t *TranscriptTailer) applyTaskDeltas(parsed *ParsedEvent) {
	for _, d := range parsed.TaskDeltas {
		switch d.Op {
		case TaskOpCreate:
			t.applyTaskCreateDelta(d)
		case TaskOpAssignID:
			t.applyTaskAssignIDDelta(d)
		case TaskOpUpdate:
			t.applyTaskUpdateDelta(d, parsed)
		}
	}
	// A create's pending entry resolves when its tool_result arrives — via
	// the assign_id delta above when the result carried the ID, or here for
	// results that didn't (error results), so the map only ever holds
	// in-flight creates. Runs after the delta loop: the assign_id delta and
	// its ToolResultID arrive on the same parsed event.
	for _, id := range parsed.ToolResultIDs {
		delete(t.pendingTaskCreates, id)
	}
}

// applyTaskCreateDelta handles a TaskOpCreate delta: assigns a provisional
// ID (the authoritative one arrives in the create's tool_result as an
// assign_id delta and replaces it — the counter only carries transcripts
// whose results don't include toolUseResult.task.id, see issue #615),
// appends the new task, and records the pending create + applied delta.
func (t *TranscriptTailer) applyTaskCreateDelta(d TaskDelta) {
	t.taskSeq++
	provisional := strconv.Itoa(t.taskSeq)
	t.tasks = append(t.tasks, Task{
		ID:          provisional,
		Subject:     d.Subject,
		Description: d.Description,
		ActiveForm:  d.ActiveForm,
		Status:      TaskStatusPending,
	})
	if d.ToolUseID != "" {
		t.pendingTaskCreates[d.ToolUseID] = provisional
	}
	t.metrics.AppliedTaskDeltas = append(t.metrics.AppliedTaskDeltas, AppliedTaskDelta{
		Op: "create", ID: provisional, Subject: d.Subject, Status: TaskStatusPending,
	})
}

// applyTaskAssignIDDelta handles a TaskOpAssignID delta: replaces the
// still-provisional task's ID with Claude's authoritative one and keeps the
// provisional counter aligned with it.
func (t *TranscriptTailer) applyTaskAssignIDDelta(d TaskDelta) {
	provisional, ok := t.pendingTaskCreates[d.ToolUseID]
	if !ok || d.ID == "" {
		return
	}
	delete(t.pendingTaskCreates, d.ToolUseID)
	// Scan in reverse: when the provisional counter lags Claude's
	// numbering by less than a parallel-create batch, an already
	// assigned authoritative ID can collide with a later task's
	// provisional one. The authoritative holder was necessarily
	// created earlier (smaller provisional, earlier result), so the
	// last match is always the still-provisional task.
	for i := len(t.tasks) - 1; i >= 0; i-- {
		if t.tasks[i].ID == provisional {
			t.tasks[i].ID = d.ID
			break
		}
	}
	// Keep the provisional counter at least at Claude's numbering so
	// a create whose result never lands (interrupt) still gets a
	// non-colliding, aligned fallback ID.
	if n, err := strconv.Atoi(d.ID); err == nil && n > t.taskSeq {
		t.taskSeq = n
	}
}

// applyTaskUpdateDelta handles a TaskOpUpdate delta: updates the matching
// task's status (stamping CompletedAt on the first transition to completed)
// and records the applied delta.
func (t *TranscriptTailer) applyTaskUpdateDelta(d TaskDelta, parsed *ParsedEvent) {
	for i := range t.tasks {
		if t.tasks[i].ID != d.ID {
			continue
		}
		if d.Status != "" {
			if d.Status == TaskStatusCompleted && t.tasks[i].Status != TaskStatusCompleted {
				t.tasks[i].CompletedAt = eventUnix(parsed)
			}
			t.tasks[i].Status = d.Status
			t.metrics.AppliedTaskDeltas = append(t.metrics.AppliedTaskDeltas, AppliedTaskDelta{
				Op: "update", ID: t.tasks[i].ID, Subject: t.tasks[i].Subject, Status: d.Status,
			})
		}
		break
	}
}

// applyBackgroundProcessDeltas tracks agent-spawned background processes
// (Bash run_in_background) in openBackgroundProcs, the source of truth for
// BackgroundProcessCount. A spawn adds the background id; a matched
// terminated BashOutput poll, a KillShell, or a terminal task-notification
// removes it. See issues #445 and #661.
func (t *TranscriptTailer) applyBackgroundProcessDeltas(parsed *ParsedEvent) {
	for _, sp := range parsed.BackgroundSpawns {
		if sp.BashID != "" {
			t.openBackgroundProcs[sp.BashID] = sp.OutputPath
		}
	}
	for _, poll := range parsed.BashOutputPolls {
		if poll.ToolUseID != "" && poll.BashID != "" {
			t.pendingBashPolls[poll.ToolUseID] = poll.BashID
		}
	}
	for _, id := range parsed.TerminatedBashOutputIDs {
		if bashID, ok := t.pendingBashPolls[id]; ok {
			delete(t.openBackgroundProcs, bashID)
		}
	}
	// A poll is resolved once its tool_result arrives (terminated OR still
	// running) — drop the pairing either way so pendingBashPolls only ever
	// holds in-flight polls (bounded by concurrent polls, not total polls).
	for _, id := range parsed.ToolResultIDs {
		delete(t.pendingBashPolls, id)
	}
	for _, bashID := range parsed.KilledShellIDs {
		delete(t.openBackgroundProcs, bashID)
	}
	// Terminal task-notification completion (orchestrated/SDK path): the
	// <task-id> is the backgroundTaskId. A non-matching id is a harmless no-op.
	// See issue #445.
	for _, id := range parsed.TerminatedBackgroundTaskIDs {
		delete(t.openBackgroundProcs, id)
	}
}

// sweepOpenToolCallsOnTurnDone drops stale entries from openToolCalls when
// parsed is the authoritative end-of-turn signal. By definition most
// tool_use events opened during the turn have already received their
// tool_result, so anything still open is a stale leak. Sweeping here lets the
// classifier see HasOpenToolCall=false and transition working → ready.
//
// Some tools survive the sweep (see surviveTurnDone): Agent (sub-agent still
// running), AskUserQuestion, and ExitPlanMode (user-blocking tools whose
// result arrives only after the user responds). Preserving them ensures
// NeedsUserAttention() returns true so the classifier transitions to
// "waiting" instead of "ready".
func (t *TranscriptTailer) sweepOpenToolCallsOnTurnDone(parsed *ParsedEvent) {
	if parsed.EventType != "turn_done" || len(t.openToolCalls) == 0 {
		return
	}
	for id, name := range t.openToolCalls {
		if !surviveTurnDone(name) {
			delete(t.openToolCalls, id)
		}
	}
}

// updateInterruptAndDenialFlags maintains the sticky lastWasUserInterrupt and
// lastWasToolDenial flags. Each is cleared by any subsequent user event that
// isn't itself the same kind. The two flags are tracked independently
// because only ESC feeds the classifier's cancellation rule — denials are
// recorded for observability but don't end the agent's turn. parsed.IsError
// is for tool_result errors — not used by the classifier, so it isn't
// tracked here.
func (t *TranscriptTailer) updateInterruptAndDenialFlags(parsed *ParsedEvent) {
	if parsed.IsUserInterrupt {
		t.lastWasUserInterrupt = true
	} else if isUserEventType(parsed.EventType) {
		t.lastWasUserInterrupt = false
	}
	if parsed.IsToolDenial {
		t.lastWasToolDenial = true
	} else if isUserEventType(parsed.EventType) {
		t.lastWasToolDenial = false
	}
}

// applyAssistantTextAndMarkers updates the assistant-text and
// agent-emitted-marker state (task estimate, summary, question) that a
// waiting/ready session's surfaced headline is derived from, and captures
// the session's first user prompt as the heuristic-fallback summary.
func (t *TranscriptTailer) applyAssistantTextAndMarkers(parsed *ParsedEvent) {
	if parsed.AssistantText != "" {
		t.lastAssistantText = parsed.AssistantText
	}
	if parsed.ClearToolNames {
		t.lastAssistantText = ""
	}
	if parsed.TaskEstimate != nil {
		t.applyTaskEstimate(parsed.TaskEstimate)
	}
	if parsed.TaskSummary != nil {
		t.applyTaskSummary(parsed.TaskSummary)
	}
	if parsed.TaskQuestion != nil {
		t.applyTaskQuestion(parsed.TaskQuestion)
	}
	if parsed.AwaySummary != nil {
		t.applyAwaySummary(parsed.AwaySummary)
	}
	// Capture the first user prompt once as the heuristic-fallback summary
	// (issue #738) — never overwritten, so it describes what the session was
	// originally about even after many turns.
	if t.firstUserText == "" && parsed.UserText != "" {
		t.firstUserText = cleanSummaryText(parsed.UserText)
	}
	if parsed.ClearToolNames && len(parsed.ToolResultIDs) == 0 {
		// A REAL user message (new prompt, ESC, answer) starts a new task or
		// redirects the current one — the previous estimate no longer
		// describes what the agent is doing, so reset it like
		// lastAssistantText above. The ToolResultIDs guard matters: Claude
		// Code delivers tool results as user-role lines that also raise
		// ClearToolNames, and resetting on those wiped the estimate on every
		// tool call — the chip vanished mid-task until the next marker (#558).
		t.lastTaskEstimate = nil
		t.firstTaskEstimate = nil
		// The summary describes the now-superseded task; clear it so the next
		// task re-anchors (the agent re-emits, or the heuristic takes over).
		t.lastTaskSummary = nil
		// The question was answered by this very user message — clear it so the
		// next waiting turn re-derives from the new last-assistant text.
		t.lastTaskQuestion = nil
		// The recap described the now-superseded turn; clear it so a later
		// away_summary arriving for a stale turn can't leak into the new one.
		t.lastAwaySummary = nil
	}
}

// applyTaskEstimate runs the marker anchor bookkeeping shared by the
// transcript scan and the hook ingest path (#604): track the CURRENT task's
// first marker (re-anchored when completed_rounds goes backwards — an
// agent-initiated new count) and let the latest marker win. An estimate
// observed BEFORE the current latest is dropped — a late hook delivery must
// not regress a fresher transcript marker (and a stale re-delivery must not
// falsely re-anchor the rate base). The user-message reset stays in
// processParsedEvent: it is transcript-line-driven.
func (t *TranscriptTailer) applyTaskEstimate(est *TaskEstimate) {
	if est == nil {
		return
	}
	if t.lastTaskEstimate != nil && est.ObservedAt < t.lastTaskEstimate.ObservedAt {
		return
	}
	if t.firstTaskEstimate == nil ||
		(t.lastTaskEstimate != nil && est.CompletedRounds < t.lastTaskEstimate.CompletedRounds) {
		t.firstTaskEstimate = est
	}
	t.lastTaskEstimate = est
}

// IngestTaskEstimate feeds an out-of-band task-progress estimate into the
// same anchor/reset/ledger state as a transcript-scanned marker. Used by the
// Claude Code hook receiver (#604): markers carried in tool inputs reach the
// daemon via PreToolUse payloads, bypassing the transcript writer that drops
// mid-task text blocks on claude ≥2.1.162. Caller (the metrics adapter)
// holds the per-tailer lock, mirroring IngestRateLimit.
func (t *TranscriptTailer) IngestTaskEstimate(est *TaskEstimate) {
	t.applyTaskEstimate(est)
}

// applyTaskSummary keeps the latest task-summary marker, shared by the
// transcript scan and the hook ingest path (#738). A summary observed BEFORE
// the current latest is dropped so a late hook delivery can't regress a
// fresher transcript marker. The user-message reset stays in
// processParsedEvent: it is transcript-line-driven.
func (t *TranscriptTailer) applyTaskSummary(s *TaskSummary) {
	if s == nil || s.Text == "" {
		return
	}
	if t.lastTaskSummary != nil && s.ObservedAt < t.lastTaskSummary.ObservedAt {
		return
	}
	t.lastTaskSummary = s
}

// IngestTaskSummary feeds an out-of-band task-summary marker into the same
// state as a transcript-scanned one. Used by the Claude Code hook receiver
// (#738): a marker carried on a Bash description reaches the daemon via a
// PreToolUse payload, bypassing the transcript writer that drops mid-task
// text blocks on claude ≥2.1.162. Caller holds the per-tailer lock.
func (t *TranscriptTailer) IngestTaskSummary(s *TaskSummary) {
	t.applyTaskSummary(s)
}

// applyTaskQuestion keeps the latest task-question marker (issue #759). A
// question observed BEFORE the current latest is dropped so an out-of-order
// delivery can't regress a fresher marker. The user-message reset stays in
// processParsedEvent: it is transcript-line-driven.
func (t *TranscriptTailer) applyTaskQuestion(q *TaskQuestion) {
	if q == nil || q.Text == "" {
		return
	}
	if t.lastTaskQuestion != nil && q.ObservedAt < t.lastTaskQuestion.ObservedAt {
		return
	}
	t.lastTaskQuestion = q
}

// applyAwaySummary keeps the latest away_summary recap (issue #979). A recap
// observed BEFORE the current latest is dropped so an out-of-order delivery
// can't regress a fresher one. The user-message reset stays in
// processParsedEvent: it is transcript-line-driven.
func (t *TranscriptTailer) applyAwaySummary(s *AwaySummary) {
	if s == nil || s.Text == "" {
		return
	}
	if t.lastAwaySummary != nil && s.ObservedAt < t.lastAwaySummary.ObservedAt {
		return
	}
	t.lastAwaySummary = s
}

// PurgeBackgroundProcs drops the tracked background processes whose output
// path is in outputs. Called when the detector's liveness probe verdicts them
// dead (no live writer on any probed output file): the transcript never
// recorded a termination — the process died with its parent shell — so
// without this the entries persist in the ledger and resurrect as phantom
// open processes on every daemon restart. Scoped to the probed outputs, not
// purge-all: the probe runs async, and a process spawned after the snapshot
// was taken must survive (its own probe will judge it). Caller (the metrics
// adapter) holds the per-tailer lock, mirroring IngestRateLimit. See #649.
func (t *TranscriptTailer) PurgeBackgroundProcs(outputs []string) {
	if len(outputs) == 0 {
		return
	}
	dead := make(map[string]bool, len(outputs))
	for _, o := range outputs {
		dead[o] = true
	}
	for id, path := range t.openBackgroundProcs {
		if dead[path] {
			delete(t.openBackgroundProcs, id)
		}
	}
}

// PurgeBackgroundProcsByID is PurgeBackgroundProcs's key-keyed counterpart: it
// drops tracked background processes whose background id (a PID, for adapters
// like Gemini CLI that key the open set on the PID with no output path) is in
// ids. Same dead-PID-doesn't-resurrect rationale as PurgeBackgroundProcs.
// See issue #661.
func (t *TranscriptTailer) PurgeBackgroundProcsByID(ids []string) {
	for _, id := range ids {
		delete(t.openBackgroundProcs, id)
	}
}
