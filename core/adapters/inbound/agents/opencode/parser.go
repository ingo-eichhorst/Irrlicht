package opencode

import (
	"irrlicht/core/pkg/tailer"
	"time"
)

// Parser implements tailer.TranscriptParser for OpenCode sessions.
//
// OpenCode stores session data in a SQLite database rather than JSONL files.
// Each row in the `part` table has a `data` JSON column that this parser
// interprets. The watcher reads rows from the DB and calls ParseLine with the
// unmarshalled JSON map for each part.
//
// Key part types from OpenCode's schema:
//
//	step-start    — begins an LLM generation step (skip; used for context only)
//	step-finish   — ends a step; reason="stop" signals turn completion
//	text          — assistant text output
//	tool          — tool call; state.status tracks pending→running→completed
//
// Token/cost data lives on step-finish parts and on the parent `message` row
// (role="assistant"). The parser extracts cost and token counts from
// step-finish to populate PerTurnContribution.
//
// The parser is stateless: each part row is self-contained. The watcher
// passes message role context via the synthetic "_role" key in the raw map.
type Parser struct{}

// ParseLine parses a raw map representing one OpenCode part row into a
// normalized ParsedEvent. The map is expected to contain the decoded JSON
// from the `part.data` column. The watcher may inject:
//
//	"_role"    — the role from the parent message row ("user" / "assistant")
//	"_session" — the session ID (for CWD lookup, if needed)
//	"_cwd"     — the session's working directory
//	"_ts"      — epoch-ms timestamp from part.time_updated
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{}

	// Extract synthetic context injected by the watcher.
	if ts, ok := raw["_ts"].(float64); ok && ts > 0 {
		ev.Timestamp = time.UnixMilli(int64(ts))
	}
	if cwd, ok := raw["_cwd"].(string); ok {
		ev.CWD = cwd
	}

	partType, _ := raw["type"].(string)

	switch partType {
	case "step-start":
		ev.Skip = true
		return ev

	case "step-finish":
		return parseStepFinish(raw, ev)

	case "text":
		return parseTextPart(raw, ev)

	case "tool":
		return parseToolPart(raw, ev)

	default:
		// snapshot, file, image, and other part types — skip
		ev.Skip = true
		return ev
	}
}

// parseStepFinish handles the step-finish part type.
// reason="stop"       → agent has finished the turn → emit "turn_done"
// reason="tool-calls" → agent is about to call tools → emit "assistant_message"
//
// Token and cost data from step-finish is used to build a PerTurnContribution.
func parseStepFinish(raw map[string]interface{}, ev *tailer.ParsedEvent) *tailer.ParsedEvent {
	reason, _ := raw["reason"].(string)

	// Extract tokens and cost regardless of reason.
	if tokens, ok := raw["tokens"].(map[string]interface{}); ok {
		snap := &tailer.TokenSnapshot{}
		if v, ok := tokens["input"].(float64); ok {
			snap.Input = int64(v)
		}
		if v, ok := tokens["output"].(float64); ok {
			snap.Output = int64(v)
		}
		if cache, ok := tokens["cache"].(map[string]interface{}); ok {
			if v, ok := cache["read"].(float64); ok {
				snap.CacheRead = int64(v)
			}
			if v, ok := cache["write"].(float64); ok {
				snap.CacheCreation = int64(v)
			}
		}
		if v, ok := tokens["total"].(float64); ok {
			snap.Total = int64(v)
		}
		ev.Tokens = snap

		// Build a PerTurnContribution from the step-finish token data.
		// OpenCode reports per-step tokens (not cumulative), so each
		// step-finish with reason="stop" directly represents a billable turn.
		if reason == "stop" {
			usage := tailer.UsageBreakdown{
				Input:     snap.Input,
				Output:    snap.Output,
				CacheRead: snap.CacheRead,
				// OpenCode's cache.write maps to ephemeral cache creation.
				CacheCreation5m: snap.CacheCreation,
			}
			modelName, _ := raw["_model"].(string)
			cost := extractCost(raw)
			contrib := &tailer.PerTurnContribution{
				Model: modelName,
				Usage: usage,
			}
			if cost > 0 {
				contrib.ProviderCostUSD = &cost
			}
			ev.Contribution = contrib
		}
	}

	switch reason {
	case "stop":
		// Primary done signal — IsAgentDone() fires via this path.
		ev.EventType = "turn_done"
	case "tool-calls":
		// Agent is about to invoke tools; stay in working state.
		ev.EventType = "assistant_message"
	default:
		// "length", "error", etc. — treat as assistant_message for now.
		ev.EventType = "assistant_message"
	}
	return ev
}

// parseTextPart handles text parts — assistant text output during a turn.
func parseTextPart(raw map[string]interface{}, ev *tailer.ParsedEvent) *tailer.ParsedEvent {
	role, _ := raw["_role"].(string)
	if role == "user" {
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		return ev
	}
	// Assistant text part.
	ev.EventType = "assistant_message"
	if text, ok := raw["text"].(string); ok {
		if len(text) > 200 {
			ev.AssistantText = text[:200]
		} else {
			ev.AssistantText = text
		}
		ev.ContentChars = int64(len(text))
	}
	return ev
}

// parseToolPart handles tool parts — tool calls and their results.
// OpenCode updates a single part row as a tool progresses through
// pending → running → completed/error states.
//
// The watcher emits a new ParseLine call for each relevant state transition:
//   - status="pending" or "running" → open tool call → ToolUses
//   - status="completed" or "error" → tool result → ToolResultIDs
func parseToolPart(raw map[string]interface{}, ev *tailer.ParsedEvent) *tailer.ParsedEvent {
	state, _ := raw["state"].(map[string]interface{})
	if state == nil {
		ev.Skip = true
		return ev
	}

	status, _ := state["status"].(string)
	callID, _ := raw["callID"].(string)
	toolName, _ := raw["tool"].(string)

	switch status {
	case "pending", "running":
		ev.EventType = "function_call"
		if callID != "" || toolName != "" {
			ev.ToolUses = []tailer.ToolUse{{ID: callID, Name: toolName}}
		}
	case "completed":
		ev.EventType = "function_call_output"
		if callID != "" {
			ev.ToolResultIDs = []string{callID}
		}
	case "error":
		ev.EventType = "function_call_output"
		ev.IsError = true
		if callID != "" {
			ev.ToolResultIDs = []string{callID}
		}
	default:
		ev.Skip = true
	}
	return ev
}

// extractCost reads the top-level "cost" field from a part data map.
func extractCost(raw map[string]interface{}) float64 {
	if v, ok := raw["cost"].(float64); ok {
		return v
	}
	return 0
}
