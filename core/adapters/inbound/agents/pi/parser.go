package pi

import (
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

	eventType := ""
	if et, ok := raw["type"].(string); ok {
		eventType = et
	}

	// No type field → skip (shouldn't happen for Pi but be defensive).
	if eventType == "" {
		ev.Skip = true
		return ev
	}

	// Session header: {"type": "session", "version": 3, "cwd": "..."}
	if eventType == "session" {
		if cwd, ok := raw["cwd"].(string); ok && cwd != "" {
			ev.CWD = cwd
		}
		ev.Skip = true
		return ev
	}

	// Model change — extract model info and skip.
	if eventType == "model_change" {
		if model, ok := raw["modelId"].(string); ok && model != "" {
			ev.ModelName = tailer.NormalizeModelName(model)
		}
		ev.Skip = true
		return ev
	}

	// Non-message metadata types — skip.
	switch eventType {
	case "thinking_level_change", "branch_summary", "custom":
		ev.Skip = true
		return ev
	}

	// Compaction is active model work and should promote ready→working.
	// Pi emits this as a top-level event instead of a message block.
	if eventType == "compaction" {
		ev.EventType = "assistant"
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
		stopReason, _ := piMsg["stopReason"].(string)
		if stopReason == "stop" {
			ev.EventType = "turn_done" // end-of-turn (primary path for IsAgentDone)
		} else {
			ev.EventType = "assistant" // mid-turn (toolUse, etc.)
		}

		// Extract tool calls from message.content[].
		if contentArr, ok := piMsg["content"].([]interface{}); ok {
			for _, item := range contentArr {
				if block, ok := item.(map[string]interface{}); ok {
					if block["type"] == "toolCall" {
						id, _ := block["id"].(string)
						name, _ := block["name"].(string)
						if name != "" {
							ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: id, Name: name})
						}
					}
				}
			}
		}

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

	case "user":
		ev.EventType = "user_message"
		ev.ClearToolNames = true

	case "toolResult":
		ev.EventType = "tool_result"
		if toolCallID, ok := piMsg["toolCallId"].(string); ok && toolCallID != "" {
			ev.ToolResultIDs = []string{toolCallID}
		}
		if isErr, ok := piMsg["isError"].(bool); ok && isErr {
			ev.IsError = true
		}

	case "bashExecution":
		// User-side shell escape (! command) — skip.
		ev.Skip = true
		return ev

	default:
		ev.Skip = true
		return ev
	}

	// Content character count.
	ev.ContentChars = tailer.ExtractContentChars(raw)

	return ev
}

// extractPiContribution builds a PerTurnContribution from Pi's usage object.
// Pi uses short field names (input/output/cacheRead/cacheWrite) and may also
// include a direct per-turn cost under "cost". When cost is present, it is
// used as the authoritative ProviderCostUSD and token pricing is skipped.
func extractPiContribution(modelName string, usage map[string]interface{}) *tailer.PerTurnContribution {
	contrib := &tailer.PerTurnContribution{Model: modelName}

	// Pi token fields.
	if v, ok := usage["input"].(float64); ok {
		contrib.Usage.Input = int64(v)
	}
	if v, ok := usage["output"].(float64); ok {
		contrib.Usage.Output = int64(v)
	}
	if v, ok := usage["cacheRead"].(float64); ok {
		contrib.Usage.CacheRead = int64(v)
	}
	if v, ok := usage["cacheWrite"].(float64); ok {
		// Pi calls them cacheWrite; map to CacheCreation5m (5m is the default).
		contrib.Usage.CacheCreation5m = int64(v)
	}

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
	if len([]rune(text)) > 200 {
		return string([]rune(text)[:200])
	}
	return text
}
