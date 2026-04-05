package claudecode

import (
	"strings"

	"irrlicht/core/pkg/tailer"
	"irrlicht/core/pkg/transcript"
)

// Parser implements tailer.TranscriptParser for Claude Code transcripts.
// Claude Code events use top-level "type" fields ("user", "assistant", "system")
// and embed tool calls inside message.content[] arrays.
type Parser struct{}

// ParseLine parses a Claude Code JSONL line into a normalized ParsedEvent.
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{
		Timestamp: tailer.ParseTimestamp(raw),
	}

	eventType := "unknown"
	if et, ok := raw["type"].(string); ok {
		eventType = et
	} else if _, ok := raw["user_input"]; ok {
		eventType = "user_message"
	} else if _, ok := raw["assistant_output"]; ok {
		eventType = "assistant_message"
	} else if _, ok := raw["tool_call"]; ok {
		eventType = "tool_call"
	}

	// CWD from top-level field.
	if cwd := transcript.ExtractCWDFromLine(raw); cwd != "" {
		ev.CWD = cwd
	}

	// System events: turn_duration and stop_hook_summary are authoritative
	// "agent is done" signals. Return them as turn_done with no MessageEvent.
	if eventType == "system" {
		if subtype, _ := raw["subtype"].(string); subtype == "turn_duration" || subtype == "stop_hook_summary" {
			ev.EventType = "turn_done"
			// Don't skip — the tailer needs to set LastEventType to turn_done.
			// But it's not a message event, so we mark it specially.
			return ev
		}
		ev.Skip = true
		return ev
	}

	// Local commands (shell escapes, /context, etc.) write user events but
	// don't trigger agent turns. Skip them to avoid affecting state detection.
	if eventType == "user" {
		if isMeta, ok := raw["isMeta"].(bool); ok && isMeta {
			ev.Skip = true
			return ev
		}
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				if strings.HasPrefix(content, "<local-command") ||
					strings.HasPrefix(content, "<command-name>") ||
					strings.HasPrefix(content, "<bash-input>") ||
					strings.HasPrefix(content, "<bash-stdout>") {
					ev.Skip = true
					return ev
				}
			}
		}
	}

	// Permission mode events.
	if eventType == "permission-mode" {
		if mode, ok := raw["permissionMode"].(string); ok {
			ev.PermissionMode = mode
		}
		ev.Skip = true
		return ev
	}

	// Model extraction.
	ev.ModelName, ev.ContextWindow = extractClaudeCodeModel(raw)

	// Token extraction.
	ev.Tokens = extractClaudeCodeTokens(raw)

	// Content character count for token estimation.
	ev.ContentChars = tailer.ExtractContentChars(raw)

	// Filter non-message events.
	if !isClaudeCodeMessageEvent(eventType) {
		ev.Skip = true
		return ev
	}

	ev.EventType = eventType

	// Scan message.content[] for embedded tool_use and tool_result blocks.
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if contentArr, ok := msg["content"].([]interface{}); ok {
			for _, item := range contentArr {
				if block, ok := item.(map[string]interface{}); ok {
					switch block["type"] {
					case "tool_use":
						if name, ok := block["name"].(string); ok {
							ev.ToolUseNames = append(ev.ToolUseNames, name)
						}
					case "tool_result":
						ev.ToolResultCount++
						if isErr, ok := block["is_error"].(bool); ok && isErr {
							ev.IsError = true
						}
					}
				}
			}
		}
	}

	// Track assistant text for waiting-state display.
	switch eventType {
	case "assistant", "assistant_message", "assistant_output":
		ev.AssistantText = tailer.ExtractAssistantText(raw)
	case "user", "user_message", "user_input":
		ev.ClearToolNames = true
	}

	return ev
}

// extractClaudeCodeModel extracts model name and context window from a Claude Code event.
func extractClaudeCodeModel(raw map[string]interface{}) (string, int64) {
	var modelName string
	var contextWindow int64

	// Check direct fields.
	if model, ok := raw["model"].(string); ok {
		modelName = model
	} else if request, ok := raw["request"].(map[string]interface{}); ok {
		if model, ok := request["model"].(string); ok {
			modelName = model
		}
	} else if metadata, ok := raw["metadata"].(map[string]interface{}); ok {
		if model, ok := metadata["model"].(string); ok {
			modelName = model
		}
	}

	// message.model (assistant messages).
	if modelName == "" {
		if message, ok := raw["message"].(map[string]interface{}); ok {
			if model, ok := message["model"].(string); ok {
				modelName = model
			}
		}
	}

	// Extended context detection.
	if strings.Contains(modelName, "[1m]") {
		contextWindow = 1000000
	}

	// context_management.context_window (most accurate source).
	if cm, ok := raw["context_management"].(map[string]interface{}); ok {
		if cw, ok := cm["context_window"].(float64); ok && cw > 0 {
			contextWindow = int64(cw)
		}
	}

	if modelName != "" {
		modelName = tailer.NormalizeModelName(modelName)
	}
	return modelName, contextWindow
}

// extractClaudeCodeTokens extracts token info from a Claude Code event.
func extractClaudeCodeTokens(raw map[string]interface{}) *tailer.TokenSnapshot {
	// Check usage field (Claude API format).
	if usage, ok := raw["usage"].(map[string]interface{}); ok {
		return tailer.ExtractUsage(usage)
	}
	// Check message.usage (Claude Code format).
	if message, ok := raw["message"].(map[string]interface{}); ok {
		if usage, ok := message["usage"].(map[string]interface{}); ok {
			return tailer.ExtractUsage(usage)
		}
	}
	// Check response.usage.
	if response, ok := raw["response"].(map[string]interface{}); ok {
		if usage, ok := response["usage"].(map[string]interface{}); ok {
			return tailer.ExtractUsage(usage)
		}
	}
	return nil
}

// isClaudeCodeMessageEvent returns true for event types that count as messages.
func isClaudeCodeMessageEvent(eventType string) bool {
	switch eventType {
	case "user_message", "assistant_message", "tool_call", "tool_result",
		"user_input", "assistant_output", "user", "assistant", "tool_use", "message":
		return true
	}
	return false
}
