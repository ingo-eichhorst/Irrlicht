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

// maxTranscriptLineSize caps a single JSONL line at 64 MB. Lines beyond this
// are skipped so a malformed or pathological transcript can never wedge the
// tailer (issue #270).
const maxTranscriptLineSize = 64 * 1024 * 1024

var (
	errLineTooLong  = errors.New("transcript line exceeds size cap")
	errPartialAtEOF = errors.New("transcript ends mid-line")
)

// MessageEvent represents a single message event from transcript
type MessageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Content   string    `json:"content,omitempty"`
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
	ModelName              string  `json:"model_name,omitempty"`
	ContextWindow          int64   `json:"context_window,omitempty"`
	ContextUtilization     float64 `json:"context_utilization_percentage,omitempty"`
	PressureLevel          string  `json:"pressure_level,omitempty"` // "safe", "caution", "warning", "critical"

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

	// SubagentCompletions surfaces parent-side "subagent done" signals
	// discovered during the most recent TailAndProcess() pass. Cleared at
	// the start of every pass so the detector drains fresh events only.
	// See issue #134.
	SubagentCompletions []SubagentCompletion `json:"-"`

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

	// Tasks is the current task list for this session, accumulated from
	// TaskCreate / TaskUpdate tool_use events in the Claude Code transcript.
	// Nil for sessions that have not called TaskCreate.
	Tasks []Task `json:"tasks,omitempty"`
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

	// contentChars accumulates character count from message content for
	// token estimation when explicit token counts aren't available.
	contentChars int64

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

	// tasks accumulates the session's task list from TaskCreate / TaskUpdate
	// tool_use events parsed by the Claude Code adapter.
	tasks []Task
	// taskSeq is the next sequential ID to assign on TaskCreate.
	// Invariant: taskSeq == len(tasks) always (tasks are never removed).
	taskSeq int
}

// NewTranscriptTailer creates a new tailer for the given transcript path.
// The parser handles format-specific line parsing; adapter is used for model
// config fallback.
func NewTranscriptTailer(path string, parser TranscriptParser, adapter string) *TranscriptTailer {
	return &TranscriptTailer{
		path:          path,
		lastOffset:    0,
		capacityMgr:   capacity.DefaultCapacityManager(),
		parser:        parser,
		adapter:       adapter,
		openToolCalls: make(map[string]string),
		cumByModel:    make(map[string]*UsageBreakdown),
		metrics: &SessionMetrics{
			MessageHistory: make([]MessageEvent, 0),
			SessionStartAt: time.Time{},
		},
		windowSize: 60 * time.Second,
	}
}

// GetLedgerState returns the durable accumulation state of the tailer so it
// can be persisted to disk and rehydrated after a daemon restart.
func (t *TranscriptTailer) GetLedgerState() LedgerState {
	s := LedgerState{
		SchemaVersion:      2,
		LastOffset:         t.lastOffset,
		CumProviderCostUSD: t.cumProviderCostUSD,
	}
	if len(t.cumByModel) > 0 {
		// Direct assignment is safe: the caller JSON-marshals immediately
		// while holding the per-tailer lock, so TailAndProcess cannot run
		// concurrently and mutate the map during the marshal.
		s.CumByModel = t.cumByModel
	}
	if pp, ok := t.parser.(ParserStateProvider); ok {
		pl := pp.GetParserLedger()
		s.ParserState = &pl
	}
	if len(t.tasks) > 0 {
		s.Tasks = append([]Task(nil), t.tasks...)
	}
	return s
}

// SetLedgerState rehydrates accumulation state from a previously persisted
// ledger. Must be called before the first TailAndProcess; a no-op if the
// tailer has already processed any lines.
func (t *TranscriptTailer) SetLedgerState(s LedgerState) {
	if t.lastOffset != 0 {
		return
	}
	t.lastOffset = s.LastOffset
	if len(s.CumByModel) > 0 {
		// Deep-copy so the caller's map doesn't alias the tailer's.
		t.cumByModel = make(map[string]*UsageBreakdown, len(s.CumByModel))
		for k, v := range s.CumByModel {
			if v != nil {
				copied := *v
				t.cumByModel[k] = &copied
			}
		}
	}
	t.cumProviderCostUSD = s.CumProviderCostUSD
	if s.ParserState != nil {
		if pp, ok := t.parser.(ParserStateProvider); ok {
			pp.SetParserLedger(*s.ParserState)
		}
	}
	if len(s.Tasks) > 0 {
		t.tasks = append([]Task(nil), s.Tasks...)
		t.taskSeq = len(t.tasks)
	}
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

	// Reset per-pass flag. Set below when a user-blocking tool is observed
	// both open and close within this single pass (the collapsed-window
	// case from issue #150).
	sawUserBlockingClosedThisPass := false

	// Per-pass signals must be cleared so the detector only drains events
	// discovered in this scan (see issue #134).
	t.metrics.SubagentCompletions = nil

	startPos := int64(0)
	switch {
	case fileSize < t.lastOffset:
		// File rotated/truncated — reset cumulative accumulators to avoid
		// double-counting tokens from the previous file.
		startPos = 0
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
	case t.lastOffset > 0:
		// Normal incremental path: never skip ahead of the last processed byte.
		startPos = t.lastOffset
	}

	_, err = file.Seek(startPos, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("failed to seek transcript: %w", err)
	}

	currentOffset := startPos

	// bufio.Reader (not Scanner) so a single oversized JSONL line can't wedge
	// the tailer. Lines above maxTranscriptLineSize are skipped: the offset is
	// advanced past them and processing continues. See issue #270.
	reader := bufio.NewReaderSize(file, 64*1024)

	rawLineParser, isRawLine := t.parser.(RawLineParser)

	var loopErr error
	for {
		raw, consumed, lineErr := readLineCapped(reader, maxTranscriptLineSize)
		if errors.Is(lineErr, io.EOF) || errors.Is(lineErr, errPartialAtEOF) {
			// EOF (clean) or partial trailing line — stop without advancing
			// past the partial bytes; they'll be re-read next tick once more
			// data is appended.
			break
		}
		if errors.Is(lineErr, errLineTooLong) {
			log.Printf("irrlicht/tailer: skipping oversized line at offset %d (%d bytes) in %s", currentOffset, consumed, t.path)
			currentOffset += consumed
			continue
		}
		if lineErr != nil {
			loopErr = lineErr
			break
		}

		currentOffset += consumed
		line := strings.TrimSpace(string(raw))

		if line == "" {
			continue
		}

		var parsed *ParsedEvent
		if isRawLine {
			// Markdown / non-JSONL formats: parser sees the trimmed line directly.
			parsed = rawLineParser.ParseLineRaw(line)
		} else {
			// Quick JSON check.
			if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
				continue
			}

			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				continue
			}

			// Delegate to format-specific parser.
			parsed = t.parser.ParseLine(raw)
		}
		if parsed == nil || parsed.Skip {
			// Even for skipped events, apply metadata that the parser extracted
			// (e.g. model from model_change, CWD from session header) and drain
			// SubagentCompletions — task-notification lines are deliberately
			// marked Skip=true so they don't pollute message-event tracking,
			// but the completion signal must still surface to the detector
			// (issue #134).
			if parsed != nil {
				t.applyMetadata(parsed)
				if len(parsed.SubagentCompletions) > 0 {
					t.metrics.SubagentCompletions = append(t.metrics.SubagentCompletions, parsed.SubagentCompletions...)
				}
			}
			continue
		}
		if len(parsed.SubagentCompletions) > 0 {
			t.metrics.SubagentCompletions = append(t.metrics.SubagentCompletions, parsed.SubagentCompletions...)
		}

		// Apply tool tracking deltas from the parser. openToolCalls is an
		// id-keyed map — tool_use events insert by ID (idempotent: duplicate
		// IDs from multi-line splits overwrite), tool_result events delete by
		// ID (orphan IDs with no matching entry are harmless no-ops). This
		// eliminates the FIFO's structural weakness where out-of-order or
		// orphan results could pop unrelated entries. See issue #117.
		for _, tu := range parsed.ToolUses {
			if tu.ID != "" {
				t.openToolCalls[tu.ID] = tu.Name
			}
		}
		for _, id := range parsed.ToolResultIDs {
			if name, ok := t.openToolCalls[id]; ok && isUserBlockingToolName(name) {
				sawUserBlockingClosedThisPass = true
			}
			delete(t.openToolCalls, id)
		}
		if parsed.ClearToolNames && len(parsed.ToolResultIDs) == 0 {
			t.openToolCalls = make(map[string]string)
		}
		for _, d := range parsed.TaskDeltas {
			switch d.Op {
			case TaskOpCreate:
				t.taskSeq++
				t.tasks = append(t.tasks, Task{
					ID:          strconv.Itoa(t.taskSeq),
					Subject:     d.Subject,
					Description: d.Description,
					ActiveForm:  d.ActiveForm,
					Status:      TaskStatusPending,
				})
			case TaskOpUpdate:
				for i := range t.tasks {
					if t.tasks[i].ID == d.ID {
						if d.Status != "" {
							t.tasks[i].Status = d.Status
						}
						break
					}
				}
			}
		}

		// turn_done is Claude Code's authoritative end-of-turn signal. By
		// definition most tool_use events opened during the turn have
		// already received their tool_result, so anything still in
		// openToolCalls is a stale leak. Sweeping here lets the classifier
		// see HasOpenToolCall=false and transition working → ready.
		//
		// Some tools survive the sweep (see surviveTurnDone): Agent
		// (sub-agent still running), AskUserQuestion, and ExitPlanMode
		// (user-blocking tools whose result arrives only after the user
		// responds). Preserving them ensures NeedsUserAttention() returns
		// true so the classifier transitions to "waiting" instead of
		// "ready".
		if parsed.EventType == "turn_done" && len(t.openToolCalls) > 0 {
			for id, name := range t.openToolCalls {
				if !surviveTurnDone(name) {
					delete(t.openToolCalls, id)
				}
			}
		}
		// IsUserInterrupt and IsToolDenial each set their own sticky flag;
		// any subsequent user event that isn't itself the same kind clears
		// it. The two flags are tracked independently because only ESC
		// feeds the classifier's cancellation rule — denials are recorded
		// for observability but don't end the agent's turn.
		// parsed.IsError is for tool_result errors — not used by the
		// classifier, so we don't track it.
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

		// Apply metadata.
		t.applyMetadata(parsed)

		// Track assistant text.
		if parsed.AssistantText != "" {
			t.lastAssistantText = parsed.AssistantText
		}
		if parsed.ClearToolNames {
			t.lastAssistantText = ""
		}

		// Accumulate content chars.
		t.contentChars += parsed.ContentChars

		t.addMessageEvent(MessageEvent{
			Timestamp: parsed.Timestamp,
			EventType: parsed.EventType,
		})
	}

	t.lastOffset = currentOffset

	// Compute current metrics.
	t.computeMetrics()
	t.metrics.SawUserBlockingToolClosedThisPass = sawUserBlockingClosedThisPass

	// Model config fallback.
	if t.metrics.ModelName == "" {
		if defaultModel := getDefaultModelFromConfig(t.adapter); defaultModel != "" {
			t.metrics.ModelName = defaultModel
		}
	}

	t.computeContextUtilization()

	return t.metrics, loopErr
}

// readLineCapped reads a single '\n'-terminated line from r. The returned
// slice does not include the trailing '\n' (or '\r' before it); consumed is
// the total bytes drawn from r — the caller advances the file offset by that
// amount whenever the result represents a fully-handled line (success or
// errLineTooLong).
//
// Outcomes:
//   - (line, consumed, nil): full line read.
//   - (nil, consumed, errLineTooLong): line exceeded max and ended with '\n';
//     bytes were discarded and consumed reflects the skip distance.
//   - (nil, 0, io.EOF): clean EOF, no bytes read.
//   - (nil, 0, errPartialAtEOF): EOF reached before '\n' with bytes pending —
//     either an in-progress line below the cap or one that has already grown
//     past the cap but the writer hasn't flushed '\n' yet. Caller stops
//     without advancing so the bytes are re-read once more data is appended;
//     when the line eventually completes it is reported as either a success
//     or errLineTooLong with a single accurate consumed count.
//   - (nil, 0, err): other I/O error.
func readLineCapped(r *bufio.Reader, max int64) ([]byte, int64, error) {
	var (
		buf      []byte
		consumed int64
		skipping bool
	)
	for {
		chunk, err := r.ReadSlice('\n')
		consumed += int64(len(chunk))

		switch err {
		case nil:
			if skipping {
				return nil, consumed, errLineTooLong
			}
			line := chunk[:len(chunk)-1] // drop '\n'
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if buf == nil {
				out := make([]byte, len(line))
				copy(out, line)
				return out, consumed, nil
			}
			buf = append(buf, line...)
			return buf, consumed, nil
		case bufio.ErrBufferFull:
			if skipping {
				continue
			}
			if consumed > max {
				buf = nil // release accumulated bytes for GC
				skipping = true
				continue
			}
			buf = append(buf, chunk...)
		case io.EOF:
			if consumed == 0 {
				return nil, 0, io.EOF
			}
			// Partial line at EOF — leave bytes for the next tail pass. This
			// covers both small in-progress lines and oversized lines whose
			// '\n' hasn't been flushed yet; in the latter case we'll re-read
			// the same prefix on subsequent ticks until the line completes,
			// at which point it surfaces as a single errLineTooLong with the
			// real total size. Without this, the tail beyond `consumed` would
			// be silently consumed at the JSON-validity check next tick.
			return nil, 0, errPartialAtEOF
		default:
			return nil, 0, err
		}
	}
}
