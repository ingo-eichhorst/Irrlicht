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
	ev.ModelName, ev.ContextWindow, ev.Tokens = extractCodexMetadata(raw)

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
		parseCodexFunctionCallOutput(ev)

	case "session_meta", "event_msg", "turn_context":
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
		parseCodexFunctionCallOutput(ev)
		return true
	case "web_search_call":
		ev.EventType = "function_call_output"
		ev.ToolUseNames = []string{"web_search"}
		ev.ToolResultCount = 1
		return true
	default:
		return false
	}
}

func parseCodexFunctionCall(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	name, _ := raw["name"].(string)
	ev.EventType = "function_call"
	if name != "" {
		ev.ToolUseNames = []string{name}
	}
	return true
}

func parseCodexFunctionCallOutput(ev *tailer.ParsedEvent) {
	ev.EventType = "function_call_output"
	ev.ToolResultCount = 1
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
func extractCodexMetadata(raw map[string]interface{}) (string, int64, *tailer.TokenSnapshot) {
	var modelName string
	var contextWindow int64
	var tokens *tailer.TokenSnapshot

	// Direct model field.
	if model, ok := raw["model"].(string); ok && model != "" {
		modelName = tailer.NormalizeModelName(model)
	}

	// Payload-wrapped events (event_msg, response_item, etc.).
	if payload, ok := raw["payload"].(map[string]interface{}); ok {
		if model, ok := payload["model"].(string); ok && model != "" {
			modelName = tailer.NormalizeModelName(model)
		}
		// Token info from payload.info.total_token_usage.
		if info, ok := payload["info"].(map[string]interface{}); ok {
			if usage, ok := info["total_token_usage"].(map[string]interface{}); ok {
				tokens = tailer.ExtractUsage(usage)
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

	return modelName, contextWindow, tokens
}
