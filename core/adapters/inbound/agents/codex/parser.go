package codex

import (
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/pkg/transcript"
)

// Parser implements tailer.TranscriptParser for OpenAI Codex transcripts.
// Codex uses top-level "role" fields on "message" events and separate
// "function_call" / "function_call_output" events for tool calls.
type Parser struct{}

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
	ev.ModelName, ev.ContextWindow, ev.Tokens, ev.CumulativeTokens = extractCodexMetadata(raw)

	// Content character count.
	ev.ContentChars = extractCodexContentChars(raw)

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
		// exec_command_*) that we skip. The one exception is `task_complete`:
		// this is Codex's canonical "turn finished" signal and must be emitted
		// as `turn_done` so IsAgentDone() fires via the primary path.
		//
		// Without this, codex falls into the assistant_message fallback and
		// flickers working→ready→working every time the agent writes an
		// intermediate assistant message before calling a tool (typical at
		// the start of a turn).
		if payload, ok := raw["payload"].(map[string]interface{}); ok {
			if pt, _ := payload["type"].(string); pt == "task_complete" {
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

func parseCodexMessage(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	role, _ := raw["role"].(string)
	switch role {
	case "assistant":
		ev.EventType = "assistant_message"
		ev.AssistantText = tailer.ExtractAssistantText(raw)
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

func extractCodexContentChars(raw map[string]interface{}) int64 {
	if payload, ok := raw["payload"].(map[string]interface{}); ok {
		return extractCodexContentChars(payload)
	}
	chars := tailer.ExtractContentChars(raw)
	if message, ok := raw["message"].(string); ok {
		chars += int64(len(message))
	}
	return chars
}

// extractCodexMetadata extracts model, context window, and token info from Codex events.
// Returns (modelName, contextWindow, lastTurnTokens, cumulativeTokens).
// lastTurnTokens = last_token_usage (for context utilization);
// cumulativeTokens = total_token_usage (for cost calculation).
func extractCodexMetadata(raw map[string]interface{}) (string, int64, *tailer.TokenSnapshot, *tailer.TokenSnapshot) {
	var modelName string
	var contextWindow int64
	var tokens *tailer.TokenSnapshot
	var cumTokens *tailer.TokenSnapshot

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
				cumTokens = tailer.ExtractUsage(usage)
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

	return modelName, contextWindow, tokens, cumTokens
}
