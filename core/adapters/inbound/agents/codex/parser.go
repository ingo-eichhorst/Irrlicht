package codex

import (
	"strings"
	"time"

	"irrlicht/core/pkg/tailer"
	"irrlicht/core/pkg/transcript"
)

// assistantContentContainsBlock returns true when a single `text` /
// `output_text` content block contains both open and close markers, with
// close appearing after open. Bypasses tailer.ExtractAssistantText's
// 200-rune tail truncation, which would drop the leading tag.
func assistantContentContainsBlock(raw map[string]interface{}, open, close string) bool {
	scan := func(arr []interface{}) bool {
		for _, item := range arr {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			bt, _ := block["type"].(string)
			if bt != "text" && bt != "output_text" {
				continue
			}
			text, _ := block["text"].(string)
			openIdx := strings.Index(text, open)
			if openIdx < 0 {
				continue
			}
			if strings.Contains(text[openIdx+len(open):], close) {
				return true
			}
		}
		return false
	}
	if arr, ok := raw["content"].([]interface{}); ok && scan(arr) {
		return true
	}
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if arr, ok := msg["content"].([]interface{}); ok && scan(arr) {
			return true
		}
	}
	return false
}

// Parser implements tailer.TranscriptParser for OpenAI Codex transcripts.
// Codex uses top-level "role" fields on "message" events and separate
// "function_call" / "function_call_output" events for tool calls.
//
// The parser is stateful: it tracks the last seen total_token_usage so it can
// emit per-turn delta contributions rather than cumulative totals.
type Parser struct {
	// cursor tracks the last committed cumulative total from total_token_usage.
	// Deltas (current − cursor) are emitted as PerTurnContribution.
	cursor tailer.UsageBreakdown
}

// ParseLine parses a Codex JSONL line into a normalized ParsedEvent.
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{
		Timestamp: tailer.ParseTimestamp(raw),
	}

	eventType := ""
	if et, ok := raw["type"].(string); ok {
		eventType = et
	}

	// Session header: {"id": "...", "timestamp": "..."} — no type field.
	if eventType == "" {
		ev.Skip = true
		return ev
	}

	// State records: {"record_type": "state"} — skip.
	if _, ok := raw["record_type"]; ok && eventType != "message" {
		ev.Skip = true
		return ev
	}

	// Reasoning events — skip but extract nothing.
	if eventType == "reasoning" {
		ev.Skip = true
		return ev
	}

	ev.CWD = transcript.ExtractCWDFromLine(raw)

	// Model/token extraction from payload-wrapped events.
	var cumBreakdown *tailer.UsageBreakdown
	ev.ModelName, ev.ContextWindow, ev.Tokens, cumBreakdown = extractCodexMetadata(raw)

	// Rate-limit extraction from token_count events. Lives on
	// event_msg.payload.rate_limits and is emitted on every API turn (clean
	// cadence — no zero-delta noise like Claude Code's statusline). Other
	// event types either don't carry the block or carry stale duplicates.
	if eventType == "event_msg" {
		if payload, ok := raw["payload"].(map[string]interface{}); ok {
			if pt, _ := payload["type"].(string); pt == "token_count" {
				ev.RateLimit = extractCodexRateLimits(payload, ev.Timestamp)
			}
		}
	}

	// Emit a Contribution when cumulative usage advances (monotonic delta).
	if cumBreakdown != nil {
		delta := tailer.UsageBreakdown{
			Input:     max(0, cumBreakdown.Input-p.cursor.Input),
			Output:    max(0, cumBreakdown.Output-p.cursor.Output),
			CacheRead: max(0, cumBreakdown.CacheRead-p.cursor.CacheRead),
		}
		if delta.Input > 0 || delta.Output > 0 || delta.CacheRead > 0 {
			ev.Contribution = &tailer.PerTurnContribution{
				Model: ev.ModelName,
				Usage: delta,
			}
			p.cursor = *cumBreakdown
		}
		ev.CumulativeTokens = ev.Tokens
	}

	// Map event types to normalized forms.
	switch eventType {
	case "message":
		if !parseCodexMessage(raw, ev) {
			ev.Skip = true
			return ev
		}

	case "response_item":
		if payload, ok := raw["payload"].(map[string]interface{}); ok {
			if !parseCodexResponseItem(payload, ev) {
				ev.Skip = true
				return ev
			}
		} else {
			ev.Skip = true
			return ev
		}

	case "function_call":
		if !parseCodexFunctionCall(raw, ev) {
			ev.Skip = true
			return ev
		}

	case "function_call_output":
		parseCodexFunctionCallOutput(raw, ev)

	case "event_msg":
		// Most event_msg payloads are metadata (token_count, task_started,
		// exec_command_*) that we skip. Two payloads signal a turn boundary
		// and must be emitted as `turn_done` so IsAgentDone() fires via the
		// primary path: `task_complete` (the canonical "turn finished" signal)
		// and `turn_aborted` (the turn was cancelled via ESC or errored
		// mid-flight — Codex emits it *instead of* task_complete, so without
		// it an interrupted turn never settles and the session sticks in
		// `working` until the process exits or an idle sweep fires).
		//
		// Treating task_complete as terminal also prevents flicker: codex
		// falls into the assistant_message fallback otherwise and
		// flickers working→ready→working every time the agent writes an
		// intermediate assistant message before calling a tool (typical at
		// the start of a turn).
		if payload, ok := raw["payload"].(map[string]interface{}); ok {
			if pt, _ := payload["type"].(string); pt == "task_complete" || pt == "turn_aborted" {
				ev.EventType = "turn_done"
				return ev
			}
		}
		ev.Skip = true
		return ev

	case "session_meta", "turn_context":
		ev.Skip = true
		return ev

	default:
		ev.Skip = true
		return ev
	}

	return ev
}

// scanCodexTaskEstimate scans the FULL assistant text blocks for the
// task-estimate marker (issue #558) and sets ev.TaskEstimate to the latest
// valid one. It walks the same two content paths as ExtractAssistantText
// (top-level content[] and message.content[]) but over the untruncated text —
// AssistantText keeps only the last 200 runes and would lose early markers.
func scanCodexTaskEstimate(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	scan := func(arr []interface{}) {
		for _, item := range arr {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			bt, _ := block["type"].(string)
			if bt != "text" && bt != "output_text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
					ev.TaskEstimate = est
				}
			}
		}
	}
	if arr, ok := raw["content"].([]interface{}); ok {
		scan(arr)
	}
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if arr, ok := msg["content"].([]interface{}); ok {
			scan(arr)
		}
	}
}

func parseCodexMessage(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	role, _ := raw["role"].(string)
	switch role {
	case "assistant":
		ev.EventType = "assistant_message"
		ev.AssistantText = tailer.ExtractAssistantText(raw)
		scanCodexTaskEstimate(raw, ev)
		// Codex's `<proposed_plan>` block has no structured tool-use; map
		// it to ExitPlanMode so the classifier treats it as user-blocking.
		if assistantContentContainsBlock(raw, "<proposed_plan>", "</proposed_plan>") {
			ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{
				ID:   "codex-proposed-plan",
				Name: "ExitPlanMode",
			})
		}
	case "user", "developer":
		ev.EventType = "user_message"
		ev.ClearToolNames = true
	default:
		return false
	}
	return true
}

func parseCodexResponseItem(payload map[string]interface{}, ev *tailer.ParsedEvent) bool {
	payloadType, _ := payload["type"].(string)
	switch payloadType {
	case "message":
		return parseCodexMessage(payload, ev)
	case "function_call", "custom_tool_call":
		return parseCodexFunctionCall(payload, ev)
	case "function_call_output", "custom_tool_call_output":
		parseCodexFunctionCallOutput(payload, ev)
		return true
	case "web_search_call":
		// Self-closing tool: both opens and closes in the same event.
		ev.EventType = "function_call_output"
		id, _ := payload["id"].(string)
		ev.ToolUses = []tailer.ToolUse{{ID: id, Name: "web_search"}}
		ev.ToolResultIDs = []string{id}
		return true
	default:
		return false
	}
}

func parseCodexFunctionCall(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	name, _ := raw["name"].(string)
	callID, _ := raw["call_id"].(string)
	ev.EventType = "function_call"
	if name != "" || callID != "" {
		ev.ToolUses = []tailer.ToolUse{{ID: callID, Name: name}}
	}
	return true
}

func parseCodexFunctionCallOutput(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	ev.EventType = "function_call_output"
	if callID, ok := raw["call_id"].(string); ok && callID != "" {
		ev.ToolResultIDs = []string{callID}
	}
}

// extractCodexMetadata extracts model, context window, and token info from Codex events.
// Returns (modelName, contextWindow, lastTurnTokens, cumulativeBreakdown).
// lastTurnTokens = last_token_usage (for context utilization display);
// cumulativeBreakdown = total_token_usage as UsageBreakdown (for cost delta calculation).
func extractCodexMetadata(raw map[string]interface{}) (string, int64, *tailer.TokenSnapshot, *tailer.UsageBreakdown) {
	var modelName string
	var contextWindow int64
	var tokens *tailer.TokenSnapshot
	var cumBreakdown *tailer.UsageBreakdown

	// Direct model field.
	if model, ok := raw["model"].(string); ok && model != "" {
		modelName = tailer.NormalizeModelName(model)
	}

	// Payload-wrapped events (event_msg, response_item, etc.).
	if payload, ok := raw["payload"].(map[string]interface{}); ok {
		if model, ok := payload["model"].(string); ok && model != "" {
			modelName = tailer.NormalizeModelName(model)
		}
		// Token info from payload.info.
		// IMPORTANT: codex emits two usage blocks on every token_count event:
		//   - total_token_usage: cumulative running total across all turns in
		//     the session (sum of input+output for every turn). This grows
		//     unbounded and quickly exceeds the model context window.
		//   - last_token_usage: per-turn snapshot. last_token_usage.input_tokens
		//     is the prompt size for the most recent turn = current context
		//     window usage. This matches the per-turn semantics Claude Code's
		//     parser already produces.
		// We use last_token_usage for context utilization (stays in [0, 100%])
		// and total_token_usage for cumulative cost calculation.
		if info, ok := payload["info"].(map[string]interface{}); ok {
			if usage, ok := info["last_token_usage"].(map[string]interface{}); ok {
				tokens = tailer.ExtractUsage(usage)
			}
			if usage, ok := info["total_token_usage"].(map[string]interface{}); ok {
				cumBreakdown = extractOpenAIUsageBreakdown(usage)
			}
			if cw, ok := info["model_context_window"].(float64); ok && cw > 0 {
				contextWindow = int64(cw)
			}
		}
		// model_context_window directly on payload (task_started).
		if cw, ok := payload["model_context_window"].(float64); ok && cw > 0 {
			contextWindow = int64(cw)
		}
		// Direct usage on payload.
		if tokens == nil {
			if usage, ok := payload["usage"].(map[string]interface{}); ok {
				tokens = tailer.ExtractUsage(usage)
			}
		}
	}

	// Message-level usage (Codex responses API format).
	if usage, ok := raw["usage"].(map[string]interface{}); ok {
		tokens = tailer.ExtractUsage(usage)
	}

	return modelName, contextWindow, tokens, cumBreakdown
}

// GetParserLedger implements tailer.ParserStateProvider. Saves the cumulative
// total_token_usage cursor so per-turn deltas are computed correctly after restart.
func (p *Parser) GetParserLedger() tailer.ParserLedger {
	return tailer.ParserLedger{CumCursor: &p.cursor}
}

// SetParserLedger implements tailer.ParserStateProvider. Restores the cursor
// so the first delta after restart is relative to the last committed total.
func (p *Parser) SetParserLedger(l tailer.ParserLedger) {
	if l.CumCursor != nil {
		p.cursor = *l.CumCursor
	}
}

// extractCodexRateLimits parses payload.rate_limits from a Codex token_count
// event into a tailer.RateLimitSnapshot. Returns nil when no rate_limits
// block is present (older transcripts, or pre-first-response events).
//
// Handles three observed schema versions:
//
//   - v1 (Oct 2025): primary/secondary with window_minutes + resets_in_seconds
//     (relative). 82 samples carry off-by-one minutes (299, 10079); we keep
//     them verbatim — the matching logic downstream tolerates ±1.
//   - v2 (Nov–Dec 2025): adds plan_type + credits, uses resets_at (absolute).
//   - v3 (Jan 2026+): adds limit_id, limit_name, rate_limit_reached_type.
//
// sampledAt is the event's wall-clock time; used as the snapshot timestamp
// and as the anchor when converting v1's relative resets_in_seconds to
// absolute epoch seconds.
func extractCodexRateLimits(payload map[string]interface{}, sampledAt time.Time) *tailer.RateLimitSnapshot {
	rl, ok := payload["rate_limits"].(map[string]interface{})
	if !ok {
		return nil
	}
	snap := &tailer.RateLimitSnapshot{SampledAt: sampledAt.Unix()}

	if v, ok := rl["plan_type"].(string); ok {
		snap.PlanType = v
	}
	if v, ok := rl["rate_limit_reached_type"].(string); ok {
		snap.ReachedType = v
	}
	if c, ok := rl["credits"].(map[string]interface{}); ok {
		creds := &tailer.CreditsSnapshot{}
		if v, ok := c["has_credits"].(bool); ok {
			creds.HasCredits = v
		}
		if v, ok := c["unlimited"].(bool); ok {
			creds.Unlimited = v
		}
		if v, ok := c["balance"].(float64); ok {
			creds.Balance = v
		}
		snap.Credits = creds
	}

	// Windows: read primary/secondary in that order so the slice is stable.
	if w := extractCodexRateLimitWindow(rl["primary"], sampledAt); w != nil {
		snap.Windows = append(snap.Windows, *w)
	}
	if w := extractCodexRateLimitWindow(rl["secondary"], sampledAt); w != nil {
		snap.Windows = append(snap.Windows, *w)
	}
	if len(snap.Windows) == 0 && snap.PlanType == "" && snap.Credits == nil {
		// Nothing useful to surface — block was empty.
		return nil
	}
	return snap
}

// extractCodexRateLimitWindow parses one window (primary or secondary) from
// the rate_limits block. Returns nil when the value is missing or not a map.
func extractCodexRateLimitWindow(raw interface{}, sampledAt time.Time) *tailer.RateLimitWindow {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	w := &tailer.RateLimitWindow{}
	if v, ok := m["used_percent"].(float64); ok {
		w.UsedPercent = v
	}
	if v, ok := m["window_minutes"].(float64); ok {
		w.WindowMinutes = int(v)
	}
	if v, ok := m["resets_at"].(float64); ok && v > 0 {
		w.ResetsAt = int64(v)
	} else if v, ok := m["resets_in_seconds"].(float64); ok && v > 0 {
		// v1 schema: relative seconds. Anchor to the event timestamp so
		// downstream consumers see a consistent absolute epoch.
		w.ResetsAt = sampledAt.Add(time.Duration(v) * time.Second).Unix()
	}
	return w
}

// extractOpenAIUsageBreakdown parses an OpenAI-style usage map into a UsageBreakdown,
// including nested input_tokens_details.cached_tokens for accurate cache-hit pricing.
func extractOpenAIUsageBreakdown(usage map[string]interface{}) *tailer.UsageBreakdown {
	bd := &tailer.UsageBreakdown{}
	hasData := false

	if v, ok := usage["input_tokens"].(float64); ok {
		bd.Input = int64(v)
		hasData = true
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		bd.Output = int64(v)
		hasData = true
	}

	// OpenAI Responses API: cached tokens are nested inside input_tokens_details.
	// Prefer that over flat cache_read_input_tokens (which OpenAI doesn't use).
	if details, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
		if v, ok := details["cached_tokens"].(float64); ok && v > 0 {
			bd.CacheRead = int64(v)
			// Deduct from Input to avoid double-counting (cached tokens are
			// already included in input_tokens by OpenAI).
			bd.Input -= bd.CacheRead
			if bd.Input < 0 {
				bd.Input = 0
			}
		}
	}
	// Fallback for older Codex format.
	if bd.CacheRead == 0 {
		if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			if v, ok := details["cached_tokens"].(float64); ok {
				bd.CacheRead = int64(v)
			}
		}
	}

	if !hasData {
		return nil
	}
	return bd
}
