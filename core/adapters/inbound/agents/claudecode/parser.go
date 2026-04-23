package claudecode

import (
	"strings"

	"irrlicht/core/pkg/tailer"
	"irrlicht/core/pkg/transcript"
)

// eventTypeAssistantStreaming is emitted for intermediate Claude Code assistant
// messages (thinking blocks, partial text) that should not trigger IsAgentDone().
const eventTypeAssistantStreaming = "assistant_streaming"

// Parser implements tailer.TranscriptParser for Claude Code transcripts.
// Claude Code events use top-level "type" fields ("user", "assistant", "system")
// and embed tool calls inside message.content[] arrays.
//
// The parser is stateful: it tracks the last requestId to deduplicate streaming
// events within one API turn and expose the pending contribution to the tailer.
type Parser struct {
	lastAssistantMsgID string
	// Cost deduplication state: Claude Code emits multiple streaming events with
	// the same requestId per turn (partial output, then final with full tokens).
	// We keep the latest usage for the current requestId as pendingContrib;
	// when the requestId changes we emit it as a completed Contribution.
	lastRequestID   string
	pendingContrib  *tailer.PerTurnContribution
}

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
		// Task-notification events are Claude Code's authoritative "subagent
		// done" signal on the parent transcript (origin.kind="task-notification"
		// with an XML payload as message.content string). The subagent's own
		// JSONL is structurally ambiguous when --continue kills the parent
		// mid-stream, so this parent-side event is the only reliable signal
		// without timing heuristics. Skip=true so the line does not feed
		// LastEventType / interrupt flags. See issue #134.
		if origin, ok := raw["origin"].(map[string]interface{}); ok {
			if kind, _ := origin["kind"].(string); kind == "task-notification" {
				if msg, ok := raw["message"].(map[string]interface{}); ok {
					if content, ok := msg["content"].(string); ok {
						ev.SubagentCompletions = append(ev.SubagentCompletions, tailer.SubagentCompletion{
							AgentID:   extractXMLField(content, "task-id"),
							ToolUseID: extractXMLField(content, "tool-use-id"),
							Status:    extractXMLField(content, "status"),
						})
					}
				}
				ev.Skip = true
				return ev
			}
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
			// User interrupts come in two flavors that look similar but mean
			// different things:
			//   - "[Request interrupted by user]" — ESC during text generation.
			//     The agent's turn is over; the classifier should transition
			//     to ready. Marked with IsUserInterrupt.
			//   - "[Request interrupted by user for tool use]" — the user
			//     denied a permission prompt for a tool call. The agent's
			//     turn is NOT over: it typically responds with an alternate
			//     approach. Marked with IsToolDenial; the cancellation rule
			//     must NOT fire (otherwise the session bounces working →
			//     ready → working on every denial).
			//
			// Neither sets IsError — that's reserved for tool_result.is_error
			// (grep with no matches, build failures, etc.). See issue #102
			// Bug B and the follow-up split for the denial flicker.
			if contentArr, ok := msg["content"].([]interface{}); ok {
				for _, item := range contentArr {
					if block, ok := item.(map[string]interface{}); ok {
						if block["type"] == "text" {
							if text, ok := block["text"].(string); ok {
								if strings.HasPrefix(text, "[Request interrupted by user for tool use") {
									ev.IsToolDenial = true
									break
								}
								if strings.HasPrefix(text, "[Request interrupted by user") {
									ev.IsUserInterrupt = true
									break
								}
							}
						}
					}
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

	// Token extraction — set Tokens for context-utilization display.
	ev.Tokens = extractClaudeCodeTokens(raw)

	// Cost contribution: deduplicate by requestId and emit a PerTurnContribution
	// when the turn changes. Claude Code streams multiple events per API turn;
	// only the final event's token counts are authoritative.
	if reqID, ok := raw["requestId"].(string); ok && reqID != "" {
		ev.RequestID = reqID // retain for legacy path during transition
		if reqID != p.lastRequestID {
			// New turn started — emit the previous turn's contribution.
			if p.lastRequestID != "" && p.pendingContrib != nil {
				ev.Contribution = p.pendingContrib
			}
			p.lastRequestID = reqID
			p.pendingContrib = nil
		}
		// Update pending with latest usage for this turn.
		if ev.Tokens != nil {
			p.pendingContrib = &tailer.PerTurnContribution{
				Model: ev.ModelName,
				Usage: extractAnthropicUsageBreakdown(raw),
			}
		}
	}

	// Content character count for token estimation.
	ev.ContentChars = tailer.ExtractContentChars(raw)

	// Intermediate streaming messages from Claude Code (thinking blocks,
	// partial text before tool_use) are written as separate JSONL lines
	// within one API response. Using eventTypeAssistantStreaming for these
	// prevents IsAgentDone() from falsely returning true between tool calls.
	//
	// We use an allow-list of terminal stop_reasons rather than a deny-list
	// of intermediate ones — any stop_reason NOT known to be terminal is
	// assumed to be mid-turn. This protects against Bug D in issue #102,
	// where `max_tokens` (agent hit thinking budget, will continue) was
	// classified as "done" because the previous deny-list only handled nil.
	//
	// Terminal stop_reasons (this message is complete):
	//   - end_turn       normal completion → agent's turn is over
	//   - stop_sequence  stop token matched → turn is over
	//   - refusal        agent refused to answer → turn is over
	//   - tool_use       message ends because a tool was called → turn NOT
	//                    over, but the message is complete. IsAgentDone()
	//                    downstream uses HasOpenToolCall to stay in working.
	//
	// Everything else (nil, max_tokens, pause_turn, unknown) is treated as
	// streaming/mid-turn. max_tokens in particular was Bug D in #102: an
	// agent that hits its thinking budget mid-turn emits a thinking-only
	// assistant message with stop_reason=max_tokens, which used to classify
	// as "assistant" and trip IsAgentDone() → spurious ready. Any future
	// stop_reason Claude adds defaults to "assume streaming", which is the
	// safe side of the error.
	if eventType == "assistant" {
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			stopReason, _ := msg["stop_reason"].(string)
			msgID, _ := msg["id"].(string)

			switch stopReason {
			case "end_turn", "stop_sequence", "refusal", "tool_use":
				// Terminal for this message — keep eventType as "assistant".
			default:
				eventType = eventTypeAssistantStreaming
			}

			if msgID != "" {
				p.lastAssistantMsgID = msgID
			}
		}
	}

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
						id, _ := block["id"].(string)
						name, _ := block["name"].(string)
						if name != "" {
							ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: id, Name: name})
						}
					case "tool_result":
						if toolUseID, ok := block["tool_use_id"].(string); ok && toolUseID != "" {
							ev.ToolResultIDs = append(ev.ToolResultIDs, toolUseID)
						}
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
	case "assistant", eventTypeAssistantStreaming, "assistant_message", "assistant_output":
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
// Used for context-utilization display (Tokens field on ParsedEvent).
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

// PendingContribution returns the in-progress turn's contribution so the tailer
// can include it in the live cost display before the next turn begins.
func (p *Parser) PendingContribution() *tailer.PerTurnContribution {
	return p.pendingContrib
}

// GetParserLedger implements tailer.ParserStateProvider. Saves lastRequestID so
// the dedup cursor resumes at the correct turn boundary after a daemon restart.
func (p *Parser) GetParserLedger() tailer.ParserLedger {
	return tailer.ParserLedger{LastRequestID: p.lastRequestID}
}

// SetParserLedger implements tailer.ParserStateProvider. Restores the dedup
// cursor; pendingContrib is intentionally not restored because the partial turn
// will be re-emitted as a new Contribution when the next requestId arrives.
func (p *Parser) SetParserLedger(l tailer.ParserLedger) {
	p.lastRequestID = l.LastRequestID
}

// extractAnthropicUsageBreakdown builds a UsageBreakdown from a Claude Code
// event, including Anthropic's nested 5m/1h cache-write sub-rates when present.
func extractAnthropicUsageBreakdown(raw map[string]interface{}) tailer.UsageBreakdown {
	// Find the usage map (same search order as extractClaudeCodeTokens).
	var usage map[string]interface{}
	if u, ok := raw["usage"].(map[string]interface{}); ok {
		usage = u
	} else if msg, ok := raw["message"].(map[string]interface{}); ok {
		if u, ok := msg["usage"].(map[string]interface{}); ok {
			usage = u
		}
	} else if resp, ok := raw["response"].(map[string]interface{}); ok {
		if u, ok := resp["usage"].(map[string]interface{}); ok {
			usage = u
		}
	}
	if usage == nil {
		return tailer.UsageBreakdown{}
	}

	bd := tailer.UsageBreakdown{}
	if v, ok := usage["input_tokens"].(float64); ok {
		bd.Input = int64(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		bd.Output = int64(v)
	}
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		bd.CacheRead = int64(v)
	}

	// Anthropic ephemeral cache writes: prefer nested sub-rates when present.
	// Fallback: treat the flat cache_creation_input_tokens as 5m writes.
	if cc, ok := usage["cache_creation"].(map[string]interface{}); ok {
		if v, ok := cc["ephemeral_5m_input_tokens"].(float64); ok {
			bd.CacheCreation5m = int64(v)
		}
		if v, ok := cc["ephemeral_1h_input_tokens"].(float64); ok {
			bd.CacheCreation1h = int64(v)
		}
	}
	if bd.CacheCreation5m == 0 && bd.CacheCreation1h == 0 {
		if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
			bd.CacheCreation5m = int64(v)
		}
	}
	return bd
}

// CountOpenSubagents returns the number of in-process Claude Code sub-agents
// that are NOT already tracked as file-based child sessions. Current Claude
// Code writes an isSidechain transcript under `<parent>/subagents/agent-*.jsonl`
// for every Agent tool call (including Explore/Plan), so the fswatcher picks
// them up as child SessionStates and the file-based path alone is the single
// source of truth. Counting open Agent tool_use entries in LastOpenToolNames
// as well would double-count each running subagent.
//
// We keep this function as the seam the adapter exposes to the metrics layer
// so that if a future Claude Code revision reintroduces truly in-process
// subagents that don't create transcripts, we only need to change this file.
func CountOpenSubagents(m *tailer.SessionMetrics) int {
	return 0
}

// extractXMLField pulls the inner text of <tag>...</tag> from a flat XML blob.
// Used to read task-id, tool-use-id, and status from task-notification events.
// Returns "" if the tag is missing or malformed.
func extractXMLField(xml, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(xml, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(xml[start:], close)
	if end < 0 {
		return ""
	}
	return xml[start : start+end]
}

// isClaudeCodeMessageEvent returns true for event types that count as messages.
func isClaudeCodeMessageEvent(eventType string) bool {
	switch eventType {
	case "user_message", "assistant_message", "tool_call", "tool_result",
		"user_input", "assistant_output", "user", "assistant", "tool_use", "message",
		eventTypeAssistantStreaming:
		return true
	}
	return false
}
