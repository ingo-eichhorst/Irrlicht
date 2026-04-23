package tailer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	EstimatedCostUSD    float64        `json:"estimated_cost_usd,omitempty"`
	ModelName           string         `json:"model_name,omitempty"`
	ContextWindow       int64          `json:"context_window,omitempty"`
	ContextUtilization  float64        `json:"context_utilization_percentage,omitempty"`
	PressureLevel       string         `json:"pressure_level,omitempty"` // "safe", "caution", "warning", "critical"

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
	cumByModel        map[string]*UsageBreakdown
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
		SchemaVersion:      1,
		LastOffset:         t.lastOffset,
		CumProviderCostUSD: t.cumProviderCostUSD,
	}
	if len(t.cumByModel) > 0 {
		// Deep-copy so the returned state is not aliased to the live map.
		// The caller (metrics adapter) serialises this to JSON immediately,
		// but a defensive copy ensures correctness if the pattern ever changes.
		s.CumByModel = make(map[string]*UsageBreakdown, len(t.cumByModel))
		for k, v := range t.cumByModel {
			if v != nil {
				copied := *v
				s.CumByModel[k] = &copied
			}
		}
	}
	if pp, ok := t.parser.(ParserStateProvider); ok {
		pl := pp.GetParserLedger()
		s.ParserState = &pl
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
}

// SetSessionStartTime allows preserving the session start time across multiple invocations
func (t *TranscriptTailer) SetSessionStartTime(startTime time.Time) {
	if t.metrics != nil {
		t.metrics.SessionStartAt = startTime
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
	case t.lastOffset > 0:
		// Normal incremental path: never skip ahead of the last processed byte.
		startPos = t.lastOffset
	}

	_, err = file.Seek(startPos, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("failed to seek transcript: %w", err)
	}

	currentOffset := startPos

	scanner := bufio.NewScanner(file)
	// Large tool results (especially from Pi/Codex read/bash output) can exceed
	// bufio.Scanner's 64KB default token size.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		currentOffset += int64(len(scanner.Bytes()) + 1)

		if line == "" {
			continue
		}

		// Quick JSON check.
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			continue
		}

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		// Delegate to format-specific parser.
		parsed := t.parser.ParseLine(raw)
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
		} else if IsUserEventType(parsed.EventType) {
			t.lastWasUserInterrupt = false
		}
		if parsed.IsToolDenial {
			t.lastWasToolDenial = true
		} else if IsUserEventType(parsed.EventType) {
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

	return t.metrics, scanner.Err()
}

// applyMetadata applies model/token/CWD/permission metadata from a parsed event.
func (t *TranscriptTailer) applyMetadata(parsed *ParsedEvent) {
	if parsed.ModelName != "" {
		if strings.Contains(parsed.ModelName, "[1m]") {
			t.contextWindowOverride = 1000000
		}
		t.metrics.ModelName = NormalizeModelName(parsed.ModelName)
	}
	if parsed.ContextWindow > 0 {
		t.contextWindowOverride = parsed.ContextWindow
	}
	if parsed.Tokens != nil {
		if parsed.Tokens.Total > 0 {
			t.metrics.TotalTokens = parsed.Tokens.Total
		}
		// Snapshot fields — always overwritten with latest turn for context utilization.
		if parsed.Tokens.Input > 0 || parsed.Tokens.Output > 0 {
			t.inputTokens = parsed.Tokens.Input
			t.outputTokens = parsed.Tokens.Output
			t.cacheReadTokens = parsed.Tokens.CacheRead
			t.cacheCreationTokens = parsed.Tokens.CacheCreation
		}
	}

	// Cumulative token accumulation for cost calculation.
	if parsed.Contribution != nil {
		// New path (Phase 2+): adapter already handled dedup; we just accumulate.
		c := parsed.Contribution
		if c.ProviderCostUSD != nil {
			// Provider-reported cost: use directly, don't add tokens to cumByModel.
			t.cumProviderCostUSD += *c.ProviderCostUSD
		} else if c.Model != "" || c.Usage.Input > 0 || c.Usage.Output > 0 {
			if t.cumByModel == nil {
				t.cumByModel = make(map[string]*UsageBreakdown)
			}
			bd := t.cumByModel[c.Model]
			if bd == nil {
				bd = &UsageBreakdown{}
				t.cumByModel[c.Model] = bd
			}
			bd.Input += c.Usage.Input
			bd.Output += c.Usage.Output
			bd.CacheRead += c.Usage.CacheRead
			bd.CacheCreation5m += c.Usage.CacheCreation5m
			bd.CacheCreation1h += c.Usage.CacheCreation1h
		}
	} else if parsed.CumulativeTokens != nil {
		// Legacy path: Codex-style authoritative cumulative total.
		// Monotonicity guard: only advance each bucket forward.
		ct := parsed.CumulativeTokens
		if ct.Input > t.cumInputTokens {
			t.cumInputTokens = ct.Input
		}
		if ct.Output > t.cumOutputTokens {
			t.cumOutputTokens = ct.Output
		}
		if ct.CacheRead > t.cumCacheReadTokens {
			t.cumCacheReadTokens = ct.CacheRead
		}
		if ct.CacheCreation > t.cumCacheCreationTokens {
			t.cumCacheCreationTokens = ct.CacheCreation
		}
		t.pendingSnapshot = nil
	} else if parsed.Tokens != nil && parsed.RequestID != "" {
		// Legacy path: Claude Code-style dedup by requestId.
		if parsed.RequestID != t.lastRequestID && t.lastRequestID != "" && t.pendingSnapshot != nil {
			t.cumInputTokens += t.pendingSnapshot.Input
			t.cumOutputTokens += t.pendingSnapshot.Output
			t.cumCacheReadTokens += t.pendingSnapshot.CacheRead
			t.cumCacheCreationTokens += t.pendingSnapshot.CacheCreation
		}
		t.pendingSnapshot = parsed.Tokens
		t.lastRequestID = parsed.RequestID
	} else if parsed.Tokens != nil && (parsed.Tokens.Input > 0 || parsed.Tokens.Output > 0) {
		// Legacy path: Pi-style direct accumulate.
		t.cumInputTokens += parsed.Tokens.Input
		t.cumOutputTokens += parsed.Tokens.Output
		t.cumCacheReadTokens += parsed.Tokens.CacheRead
		t.cumCacheCreationTokens += parsed.Tokens.CacheCreation
	}
	if parsed.CWD != "" {
		t.lastCWD = parsed.CWD
	}
	if parsed.PermissionMode != "" {
		t.metrics.PermissionMode = parsed.PermissionMode
	}
}

// addMessageEvent adds a new message event and maintains sliding window.
// Tool call counting is NOT done here — it's handled from ParsedEvent deltas
// in TailAndProcess to avoid double-counting.
func (t *TranscriptTailer) addMessageEvent(event MessageEvent) {
	t.metrics.MessageHistory = append(t.metrics.MessageHistory, event)
	t.metrics.LastMessageAt = event.Timestamp
	t.metrics.LastEventType = event.EventType

	if t.metrics.SessionStartAt.IsZero() || event.Timestamp.Before(t.metrics.SessionStartAt) {
		t.metrics.SessionStartAt = event.Timestamp
	}
}

// computeCumulativeTokens aggregates per-model token counts and estimated cost.
// It must run on every TailAndProcess pass — even when no new events were
// processed — so that ledger-rehydrated state is reflected immediately.
func (t *TranscriptTailer) computeCumulativeTokens() {
	if len(t.cumByModel) > 0 || t.cumProviderCostUSD > 0 {
		// New path: price per-model, sum provider-reported costs.
		var totalInput, totalOutput, totalCacheRead, totalCacheCreate int64
		var pricedCost float64
		for modelName, bd := range t.cumByModel {
			totalInput += bd.Input
			totalOutput += bd.Output
			totalCacheRead += bd.CacheRead
			totalCacheCreate += bd.CacheCreation5m + bd.CacheCreation1h
			if t.capacityMgr != nil {
				pricedCost += t.capacityMgr.EstimateCostFromBreakdown(
					modelName, bd.Input, bd.Output, bd.CacheRead, bd.CacheCreation5m, bd.CacheCreation1h)
			}
		}
		// Include the pending contribution from stateful parsers (Claude Code).
		if pc, ok := t.parser.(PendingContributor); ok {
			if pending := pc.PendingContribution(); pending != nil {
				totalInput += pending.Usage.Input
				totalOutput += pending.Usage.Output
				totalCacheRead += pending.Usage.CacheRead
				totalCacheCreate += pending.Usage.CacheCreation5m + pending.Usage.CacheCreation1h
				if t.capacityMgr != nil && pending.Model != "" {
					pricedCost += t.capacityMgr.EstimateCostFromBreakdown(
						pending.Model,
						pending.Usage.Input, pending.Usage.Output, pending.Usage.CacheRead,
						pending.Usage.CacheCreation5m, pending.Usage.CacheCreation1h)
				}
			}
		}
		t.metrics.CumInputTokens = totalInput
		t.metrics.CumOutputTokens = totalOutput
		t.metrics.CumCacheReadTokens = totalCacheRead
		t.metrics.CumCacheCreationTokens = totalCacheCreate
		t.metrics.EstimatedCostUSD = pricedCost + t.cumProviderCostUSD
	} else {
		// Legacy path: scalar accumulators (testParser and pre-Contribution adapters).
		effectiveCumInput := t.cumInputTokens
		effectiveCumOutput := t.cumOutputTokens
		effectiveCumCacheRead := t.cumCacheReadTokens
		effectiveCumCacheCreate := t.cumCacheCreationTokens
		if t.pendingSnapshot != nil {
			effectiveCumInput += t.pendingSnapshot.Input
			effectiveCumOutput += t.pendingSnapshot.Output
			effectiveCumCacheRead += t.pendingSnapshot.CacheRead
			effectiveCumCacheCreate += t.pendingSnapshot.CacheCreation
		}
		t.metrics.CumInputTokens = effectiveCumInput
		t.metrics.CumOutputTokens = effectiveCumOutput
		t.metrics.CumCacheReadTokens = effectiveCumCacheRead
		t.metrics.CumCacheCreationTokens = effectiveCumCacheCreate

		if t.capacityMgr != nil && t.metrics.ModelName != "" {
			t.metrics.EstimatedCostUSD = t.capacityMgr.EstimateCostUSD(
				t.metrics.ModelName, effectiveCumInput, effectiveCumOutput,
				effectiveCumCacheRead, effectiveCumCacheCreate)
		}
	}
}

// computeMetrics calculates messages per minute and elapsed time
func (t *TranscriptTailer) computeMetrics() {
	// Cumulative cost/token aggregation must run regardless of whether any new
	// events were processed this pass — the tailer may have been rehydrated from
	// a ledger with a non-zero cumByModel and then polled with no new transcript
	// content (e.g., immediately after daemon restart before the agent produces
	// more output). Skipping this would return CumInputTokens=0 in that window.
	t.computeCumulativeTokens()

	if len(t.metrics.MessageHistory) == 0 {
		t.metrics.MessagesPerMinute = 0
		t.metrics.ElapsedSeconds = 0
		t.metrics.TotalEventCount = 0
		t.metrics.RecentEventCount = 0
		t.metrics.RecentEventWindowStart = time.Time{}
		return
	}

	currentTime := time.Now()
	latestTime := t.metrics.LastMessageAt
	if latestTime.IsZero() {
		latestTime = currentTime
	}

	if !t.metrics.SessionStartAt.IsZero() {
		t.metrics.ElapsedSeconds = int64(latestTime.Sub(t.metrics.SessionStartAt).Seconds())
	}

	t.metrics.TotalEventCount = int64(len(t.metrics.MessageHistory))

	fiveMinutesAgo := currentTime.Add(-5 * time.Minute)
	windowStart := fiveMinutesAgo
	if t.metrics.SessionStartAt.After(fiveMinutesAgo) {
		windowStart = t.metrics.SessionStartAt
	}
	t.metrics.RecentEventWindowStart = windowStart

	recentEventCount := int64(0)
	for _, msg := range t.metrics.MessageHistory {
		if msg.Timestamp.After(windowStart) || msg.Timestamp.Equal(windowStart) {
			recentEventCount++
		}
	}
	t.metrics.RecentEventCount = recentEventCount

	// Open tool calls are derived directly from the id-keyed map — the only
	// source of truth. See the openToolCalls field comment for history (#102,
	// #114, #117).
	openCalls := len(t.openToolCalls)
	t.metrics.OpenToolCallCount = openCalls
	t.metrics.HasOpenToolCall = openCalls > 0
	names := make([]string, 0, openCalls)
	for _, name := range t.openToolCalls {
		names = append(names, name)
	}
	t.metrics.LastOpenToolNames = names
	t.metrics.LastWasUserInterrupt = t.lastWasUserInterrupt
	t.metrics.LastWasToolDenial = t.lastWasToolDenial
	t.metrics.LastCWD = t.lastCWD
	t.metrics.LastAssistantText = t.lastAssistantText

	// Token snapshot (latest turn — for context utilization display).
	t.metrics.InputTokens = t.inputTokens
	t.metrics.OutputTokens = t.outputTokens
	t.metrics.CacheReadTokens = t.cacheReadTokens
	t.metrics.CacheCreationTokens = t.cacheCreationTokens


	// Sliding window for messages per minute.
	legacyWindowStart := latestTime.Add(-t.windowSize)
	messageCount := 0
	filteredHistory := make([]MessageEvent, 0, len(t.metrics.MessageHistory))
	for _, msg := range t.metrics.MessageHistory {
		if msg.Timestamp.After(legacyWindowStart) || msg.Timestamp.Equal(legacyWindowStart) {
			filteredHistory = append(filteredHistory, msg)
			messageCount++
		}
	}
	t.metrics.MessageHistory = filteredHistory

	if messageCount > 0 {
		if len(filteredHistory) > 1 {
			timeSpan := latestTime.Sub(filteredHistory[0].Timestamp)
			if timeSpan > 0 {
				t.metrics.MessagesPerMinute = float64(messageCount) / timeSpan.Minutes()
			} else {
				t.metrics.MessagesPerMinute = float64(messageCount)
			}
		} else {
			t.metrics.MessagesPerMinute = float64(messageCount) / t.windowSize.Minutes()
		}
	} else {
		t.metrics.MessagesPerMinute = 0
	}
}

// GetMetrics returns current computed metrics
func (t *TranscriptTailer) GetMetrics() *SessionMetrics {
	if t.metrics == nil {
		return &SessionMetrics{}
	}
	return t.metrics
}

// ResetOffset resets the file offset and cumulative cost accumulators
// (useful for testing or file rotation).
func (t *TranscriptTailer) ResetOffset() {
	t.lastOffset = 0
	t.cumInputTokens = 0
	t.cumOutputTokens = 0
	t.cumCacheReadTokens = 0
	t.cumCacheCreationTokens = 0
	t.lastRequestID = ""
	t.pendingSnapshot = nil
	t.cumByModel = make(map[string]*UsageBreakdown)
	t.cumProviderCostUSD = 0
}

// computeContextUtilization calculates context utilization percentage and pressure level.
func (t *TranscriptTailer) computeContextUtilization() {
	if t.metrics.TotalTokens == 0 || t.metrics.ModelName == "" {
		t.metrics.ContextUtilization = 0.0
		t.metrics.PressureLevel = "unknown"
		return
	}

	var effectiveContextWindow int64

	if t.contextWindowOverride > 0 {
		effectiveContextWindow = t.contextWindowOverride
	}

	if effectiveContextWindow <= 0 && t.capacityMgr != nil {
		effectiveContextWindow = t.capacityMgr.GetModelCapacity(t.metrics.ModelName).ContextWindow
	}

	// Unknown model: no context window data available — report raw tokens only.
	if effectiveContextWindow <= 0 {
		t.metrics.ContextWindow = 0
		t.metrics.ContextUtilization = 0
		t.metrics.PressureLevel = "unknown"
		return
	}

	utilizationPercentage := (float64(t.metrics.TotalTokens) / float64(effectiveContextWindow)) * 100

	pressureLevel := "safe"
	if utilizationPercentage >= 90 {
		pressureLevel = "critical"
	} else if utilizationPercentage >= 80 {
		pressureLevel = "warning"
	} else if utilizationPercentage >= 60 {
		pressureLevel = "caution"
	}

	t.metrics.ContextWindow = effectiveContextWindow
	t.metrics.ContextUtilization = utilizationPercentage
	t.metrics.PressureLevel = pressureLevel
}

// surviveTurnDone returns true for tools whose tool_result arrives after the
// turn_done event. These must not be swept from openToolCalls:
//
//   - Agent: sub-agents can still be running when the parent's turn ends.
//   - AskUserQuestion, ExitPlanMode: user-blocking tools whose result only
//     arrives after the user responds. Also listed in session.isUserBlockingTool;
//     the overlap is intentional — the two predicates serve different purposes.
func surviveTurnDone(name string) bool {
	switch name {
	case "Agent", "AskUserQuestion", "ExitPlanMode":
		return true
	}
	return false
}

// isUserBlockingToolName returns true for tools that always block the agent
// until the user responds: AskUserQuestion and ExitPlanMode. Used by
// TailAndProcess to flag same-pass open+close of these tools so the daemon
// can synthesise the collapsed working→waiting transition (issue #150).
// Kept local to the tailer to avoid a domain-package import; the canonical
// list also lives at session.isUserBlockingTool.
func isUserBlockingToolName(name string) bool {
	switch name {
	case "AskUserQuestion", "ExitPlanMode":
		return true
	}
	return false
}

// --- Model config fallback ---

// getDefaultModelFromConfig reads the default model from the appropriate config
// based on adapter name.
func getDefaultModelFromConfig(adapter string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch adapter {
	case "pi":
		return getPiModel(homeDir)
	case "codex":
		return getCodexModel(homeDir)
	default:
		return getClaudeModel(homeDir)
	}
}

func getClaudeModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	if model, ok := settings["model"].(string); ok {
		return NormalizeModelName(model)
	}
	return ""
}

func getCodexModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".codex", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "model") && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if strings.TrimSpace(parts[0]) == "model" {
				model := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				if model != "" {
					return model
				}
			}
		}
	}
	return ""
}

func getPiModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".pi", "agent", "settings.json"))
	if err != nil {
		return ""
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	if model, ok := settings["defaultModel"].(string); ok {
		return NormalizeModelName(model)
	}
	return ""
}
