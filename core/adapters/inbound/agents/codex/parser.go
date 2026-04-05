package codex

import (
	"encoding/json"
	"strings"

	"irrlicht/core/pkg/tailer"
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

	// CWD extraction from <cwd> XML tags and function_call workdir.
	ev.CWD = extractCodexCWD(raw)

	// Model/token extraction from payload-wrapped events.
	ev.ModelName, ev.ContextWindow, ev.Tokens = extractCodexMetadata(raw)

	// Content character count.
	ev.ContentChars = tailer.ExtractContentChars(raw)

	// Map event types to normalized forms.
	switch eventType {
	case "message":
		role, _ := raw["role"].(string)
		switch role {
		case "assistant":
			ev.EventType = "assistant_message"
			ev.AssistantText = tailer.ExtractAssistantText(raw)
		case "user", "developer":
			ev.EventType = "user_message"
			ev.ClearToolNames = true
		default:
			ev.Skip = true
			return ev
		}

	case "response_item":
		if payload, ok := raw["payload"].(map[string]interface{}); ok {
			if role, ok := payload["role"].(string); ok {
				switch role {
				case "assistant":
					ev.EventType = "assistant_message"
				case "user", "developer":
					ev.EventType = "user_message"
					ev.ClearToolNames = true
				default:
					ev.Skip = true
					return ev
				}
			} else {
				ev.Skip = true
				return ev
			}
		} else {
			ev.Skip = true
			return ev
		}

	case "function_call":
		ev.EventType = "function_call"
		if name, ok := raw["name"].(string); ok {
			ev.ToolUseNames = []string{name}
		}

	case "function_call_output":
		ev.EventType = "function_call_output"
		ev.ToolResultCount = 1

	case "session_meta", "event_msg", "turn_context":
		// Metadata events — extract model/token info (already done above) and skip.
		ev.Skip = true
		return ev

	default:
		ev.Skip = true
		return ev
	}

	return ev
}

// extractCodexCWD extracts the working directory from a Codex event.
// Checks <cwd> XML tags in content blocks and workdir in function_call arguments.
func extractCodexCWD(raw map[string]interface{}) string {
	// <cwd> XML tag in content blocks.
	if content, ok := raw["content"].([]interface{}); ok {
		for _, item := range content {
			if block, ok := item.(map[string]interface{}); ok {
				if text, ok := block["text"].(string); ok {
					if idx := strings.Index(text, "<cwd>"); idx >= 0 {
						end := strings.Index(text[idx:], "</cwd>")
						if end > 5 {
							return strings.TrimSpace(text[idx+5 : idx+end])
						}
					}
				}
			}
		}
	}
	// workdir in function_call arguments.
	if raw["type"] == "function_call" {
		if args, ok := raw["arguments"].(string); ok {
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(args), &parsed) == nil {
				if wd, ok := parsed["workdir"].(string); ok && wd != "" {
					return wd
				}
			}
		}
	}
	return ""
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
