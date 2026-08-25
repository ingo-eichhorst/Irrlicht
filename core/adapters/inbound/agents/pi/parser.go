package pi

import (
	"encoding/json"
	"strings"

	"irrlicht/core/pkg/tailer"
)

// Parser implements tailer.TranscriptParser for Pi coding agent transcripts.
// Pi nests role, stopReason, content, and usage inside a "message" object:
//
//	{"type": "message", "message": {"role": "assistant", "stopReason": "stop", ...}}
type Parser struct{}

// ParseLine parses a Pi JSONL line into a normalized ParsedEvent.
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{
		Timestamp: tailer.ParseTimestamp(raw),
	}

	eventType, _ := raw["type"].(string)

	// No type field → skip (shouldn't happen for Pi but be defensive).
	if eventType == "" {
		ev.Skip = true
		return ev
	}

	if handleNonMessageEvent(ev, eventType, raw) {
		return ev
	}

	// All remaining Pi events should be type: "message" with a nested message object.
	if eventType != "message" {
		ev.Skip = true
		return ev
	}

	piMsg, ok := raw["message"].(map[string]interface{})
	if !ok {
		ev.Skip = true
		return ev
	}

	role, _ := piMsg["role"].(string)

	switch role {
	case "assistant":
		parseAssistantMessage(ev, piMsg)

	case "user":
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		ev.UserText = tailer.ExtractUserText(raw) // heuristic summary (#738)

	case "toolResult":
		parseToolResultMessage(ev, piMsg)

	case "bashExecution":
		// User-side shell escape (! command) — skip.
		//
		// go:S1871 — same body as default below, kept as its own case
		// deliberately: this documents "bashExecution" as a recognized,
		// understood role (as opposed to default's true catch-all for roles
		// this parser doesn't know about), so default can later change
		// without silently changing behavior for this one.
		ev.Skip = true

	default:
		ev.Skip = true
	}

	return ev
}

// handleNonMessageEvent handles the Pi top-level event types that aren't a
// "message" envelope. It mutates ev in place and reports whether eventType
// was one of these (in which case ParseLine should return immediately).
func handleNonMessageEvent(ev *tailer.ParsedEvent, eventType string, raw map[string]interface{}) bool {
	switch eventType {
	case "session":
		handleSessionEvent(ev, raw)
		return true

	case "model_change":
		// Model change — extract model info and skip.
		if model, ok := raw["modelId"].(string); ok && model != "" {
			ev.ModelName = tailer.NormalizeModelName(model)
		}
		ev.Skip = true
		return true

	case "thinking_level_change", "branch_summary", "custom":
		// Non-message metadata types — skip.
		ev.Skip = true
		return true

	case "compaction":
		// Compaction is active model work and should promote ready→working.
		// Pi emits this as a top-level event instead of a message block.
		ev.EventType = "assistant"
		return true
	}

	return false
}

// handleSessionEvent fills ev from a Pi "session" header event:
// {"type": "session", "version": 3, "cwd": "..."}.
func handleSessionEvent(ev *tailer.ParsedEvent, raw map[string]interface{}) {
	if cwd, ok := raw["cwd"].(string); ok && cwd != "" {
		ev.CWD = cwd
	}
	ev.Skip = true
}

// parseAssistantMessage fills ev from an assistant-role Pi message.
func parseAssistantMessage(ev *tailer.ParsedEvent, piMsg map[string]interface{}) {
	stopReason, _ := piMsg["stopReason"].(string)
	switch stopReason {
	case "stop":
		ev.EventType = "turn_done" // end-of-turn (primary path for IsAgentDone)
	case "error":
		// #1800. A failed turn is an ENDED turn, and this arm fixes two
		// defects at once. Before it, "error" fell into the mid-turn branch
		// below and emitted "assistant": pi has no inactivity sweep on
		// `working` and writes nothing further after a refusal, so the
		// session stuck in `working` for the rest of its life. And the
		// errorMessage — the only place pi says WHY — was read by nothing.
		ev.EventType = "turn_done"
		ev.SessionError = piSessionError(piMsg)
	default:
		ev.EventType = "assistant" // mid-turn (toolUse, etc.)
	}

	// Extract tool calls from message.content[]; text blocks are scanned
	// in full for the task-estimate marker (issue #558) — the display
	// text below is tail-truncated and would lose early markers.
	extractPiToolCallsAndMarkers(ev, piMsg)

	// Extract assistant text for waiting-state display.
	ev.AssistantText = extractPiAssistantText(piMsg)

	// Model and tokens from the message.
	if model, ok := piMsg["model"].(string); ok && model != "" {
		ev.ModelName = tailer.NormalizeModelName(model)
	}
	if usage, ok := piMsg["usage"].(map[string]interface{}); ok {
		// Tokens for context-utilization display.
		ev.Tokens = tailer.ExtractUsage(usage)
		// Contribution for cost accumulation — uses Pi-specific field names
		// and prefers provider-reported cost when present.
		ev.Contribution = extractPiContribution(ev.ModelName, usage)
	}
}

// piSessionError builds the session-level failure from an assistant message
// whose stopReason is "error".
//
// Never nil: stopReason already IS the verdict, so a message with no
// errorMessage still reports the failure — with an empty Message rather than
// no error at all. Silently returning nil would make a pi release that stops
// writing errorMessage look like a healthy turn.
//
// errorMessage is a DOUBLE-ENCODED JSON string in the recorded fixture:
//
//	"errorMessage": "{\"detail\":\"The 'x' model is not supported …\"}"
//
// so the readable sentence is one unwrap down. The unwrap is best-effort and
// falls back to the raw string, which is still readable prose — never worse
// than what a UI would show today, which is nothing.
//
// Phase is always terminal. Pi records no retry ladder: there is no attempt
// counter, no retry delay, and the message carrying stopReason:"error" closes
// the turn. ErrorPhaseRetrying is unreachable for this adapter, not merely
// unimplemented.
//
// No HTTPStatus: the recorded refusal is a model-unsupported error rendered as
// prose with no status anywhere in the payload. A pointer field left nil is
// #1798's whole reason for making these pointers — "attempt 0 of 0" from data
// that said nothing is the failure mode being avoided.
func piSessionError(piMsg map[string]interface{}) *tailer.SessionError {
	rawMsg, _ := piMsg["errorMessage"].(string)
	se := &tailer.SessionError{
		Phase:   tailer.ErrorPhaseTerminal,
		Class:   "provider",
		Message: strings.TrimSpace(rawMsg),
	}
	var detail struct {
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(rawMsg), &detail); err == nil {
		if detail.Detail != "" {
			se.Message = detail.Detail
		} else if detail.Message != "" {
			se.Message = detail.Message
		}
	}
	return se
}

// extractPiToolCallsAndMarkers walks an assistant message's content[] blocks,
// collecting tool-call uses and scanning text blocks for task-estimate and
// task-summary markers.
func extractPiToolCallsAndMarkers(ev *tailer.ParsedEvent, piMsg map[string]interface{}) {
	contentArr, ok := piMsg["content"].([]interface{})
	if !ok {
		return
	}
	for _, item := range contentArr {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		applyPiContentBlock(ev, block)
	}
}

// applyPiContentBlock handles a single message.content[] block: recording a
// tool-call use, or scanning a text block for task-estimate/summary markers.
func applyPiContentBlock(ev *tailer.ParsedEvent, block map[string]interface{}) {
	switch block["type"] {
	case "toolCall":
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		if name != "" {
			ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: id, Name: name})
		}
	case "text":
		text, ok := block["text"].(string)
		if !ok {
			return
		}
		if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
			ev.TaskEstimate = est
		}
		if s := tailer.ScanTaskSummary(text, ev.Timestamp); s != nil {
			ev.TaskSummary = s
		}
	}
}

// parseToolResultMessage fills ev from a toolResult-role Pi message.
func parseToolResultMessage(ev *tailer.ParsedEvent, piMsg map[string]interface{}) {
	ev.EventType = "tool_result"
	if toolCallID, ok := piMsg["toolCallId"].(string); ok && toolCallID != "" {
		ev.ToolResultIDs = []string{toolCallID}
	}
	if isErr, ok := piMsg["isError"].(bool); ok && isErr {
		ev.IsError = true
	}
}

// extractPiContribution builds a PerTurnContribution from Pi's usage object.
// Pi uses short field names (input/output/cacheRead/cacheWrite) and may also
// include a direct per-turn cost under "cost". When cost is present, it is
// used as the authoritative ProviderCostUSD and token pricing is skipped.
func extractPiContribution(modelName string, usage map[string]interface{}) *tailer.PerTurnContribution {
	contrib := &tailer.PerTurnContribution{Model: modelName, Usage: extractPiUsage(usage)}

	// Provider-reported cost wins over token×price calculation.
	if cost, ok := usage["cost"].(float64); ok && cost > 0 {
		c := cost
		contrib.ProviderCostUSD = &c
	}

	if contrib.Usage.Input == 0 && contrib.Usage.Output == 0 && contrib.ProviderCostUSD == nil {
		return nil
	}
	return contrib
}

// extractPiUsage reads Pi's short-named token fields (input/output/cacheRead/
// cacheWrite) into a format-neutral UsageBreakdown.
func extractPiUsage(usage map[string]interface{}) tailer.UsageBreakdown {
	var u tailer.UsageBreakdown
	if v, ok := usage["input"].(float64); ok {
		u.Input = int64(v)
	}
	if v, ok := usage["output"].(float64); ok {
		u.Output = int64(v)
	}
	if v, ok := usage["cacheRead"].(float64); ok {
		u.CacheRead = int64(v)
	}
	if v, ok := usage["cacheWrite"].(float64); ok {
		// Pi calls them cacheWrite; map to CacheCreation5m (5m is the default).
		u.CacheCreation5m = int64(v)
	}
	return u
}

// extractPiAssistantText extracts text from Pi's nested message.content[] blocks.
func extractPiAssistantText(piMsg map[string]interface{}) string {
	contentArr, ok := piMsg["content"].([]interface{})
	if !ok {
		return ""
	}
	var text string
	for _, item := range contentArr {
		if block, ok := item.(map[string]interface{}); ok {
			if block["type"] == "text" {
				if t, ok := block["text"].(string); ok && t != "" {
					text = t // Use the last text block.
				}
			}
		}
	}
	return tailer.TruncateAssistantText(text)
}
